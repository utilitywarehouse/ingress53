package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/miekg/dns"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/watch"
	"k8s.io/client-go/1.5/rest"
)

var (
	errRegistratorMissingOption = errors.New("missing required registrator option")
	errDNSEmptyAnswer           = errors.New("DNS nameserver returned an empty answer")
	defaultResyncPeriod         = 15 * time.Minute
	defaultBatchProcessCycle    = 5 * time.Second
	dnsClient                   = &dns.Client{}
)

type dnsZone interface {
	UpsertCnames(records []cnameRecord) error
	DeleteCnames(records []cnameRecord) error
	Domain() string
	ListNameservers() []string
}

type cnameChange struct {
	Action string
	Record cnameRecord
}

type cnameRecord struct {
	Hostname string
	Target   string
}

type registrator struct {
	dnsZone
	*ingressWatcher
	options     registratorOptions
	sats        []selectorAndTarget
	updateQueue chan cnameChange
}

type registratorOptions struct {
	AWSSessionOptions *session.Options
	KubernetesConfig  *rest.Config
	Targets           []string // required
	LabelName         string   // required
	DefaultTarget     string   // required
	Route53ZoneID     string   // required
	ResyncPeriod      time.Duration
}

type selectorAndTarget struct {
	Selector labels.Selector
	Target   string
}

func newRegistrator(zoneID string, targets []string, labelName, defaultTarget string) (*registrator, error) {
	return newRegistratorWithOptions(
		registratorOptions{
			Route53ZoneID: zoneID,
			Targets:       targets,
			LabelName:     labelName,
			DefaultTarget: defaultTarget,
		})
}

func newRegistratorWithOptions(options registratorOptions) (*registrator, error) {
	// check required options are set
	if len(options.Targets) == 0 || options.Route53ZoneID == "" || options.LabelName == "" {
		return nil, errRegistratorMissingOption
	}

	var sats []selectorAndTarget

	for _, target := range options.Targets {
		var sb bytes.Buffer
		sb.WriteString(options.LabelName)
		sb.WriteString("=")
		sb.WriteString(target)

		s, err := labels.Parse(sb.String())
		if err != nil {
			return nil, err
		}
		sats = append(sats, selectorAndTarget{Selector: s, Target: target})
	}

	if options.AWSSessionOptions == nil {
		options.AWSSessionOptions = &session.Options{}
	}

	if options.KubernetesConfig == nil {
		c, err := rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
		options.KubernetesConfig = c
	}

	if options.ResyncPeriod == 0 {
		options.ResyncPeriod = defaultResyncPeriod
	}

	return &registrator{
		options:     options,
		sats:        sats,
		updateQueue: make(chan cnameChange, 64),
	}, nil
}

func (r *registrator) Start() error {
	sess, err := session.NewSessionWithOptions(*r.options.AWSSessionOptions)
	if err != nil {
		return err
	}
	dns, err := newRoute53Zone(r.options.Route53ZoneID, route53.New(sess))
	if err != nil {
		return err
	}
	r.dnsZone = dns
	log.Println("[INFO] setup route53 session")

	kubeClient, err := kubernetes.NewForConfig(r.options.KubernetesConfig)
	if err != nil {
		return err
	}
	r.ingressWatcher = newIngressWatcher(kubeClient, r.handler, r.options.ResyncPeriod)
	log.Println("[INFO] setup kubernetes ingress watcher")

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.processUpdateQueue()
	}()

	r.ingressWatcher.Start()

	wg.Wait()

	return nil
}

func (r *registrator) handler(eventType watch.EventType, oldIngress *v1beta1.Ingress, newIngress *v1beta1.Ingress) {
	log.Printf("[DEBUG] received %s event", eventType)
	switch eventType {
	case watch.Added:
		hostnames := getHostnamesFromIngress(newIngress)
		target := r.getTargetForIngress(newIngress)
		metricUpdatesReceived.WithLabelValues(newIngress.Name, "add").Inc()
		if len(hostnames) > 0 && target != "" {
			log.Printf("[DEBUG] queued update of %d records for ingress %s, pointing to %s", len(hostnames), newIngress.Name, target)
			r.queueUpdates(route53.ChangeActionUpsert, hostnames, target)
		}
	case watch.Modified:
		newHostnames := getHostnamesFromIngress(newIngress)
		newTarget := r.getTargetForIngress(newIngress)
		metricUpdatesReceived.WithLabelValues(newIngress.Name, "modify").Inc()
		if len(newHostnames) > 0 && newTarget != "" {
			log.Printf("[DEBUG] queued update of %d records for ingress %s, pointing to %s", len(newHostnames), newIngress.Name, newTarget)
			r.queueUpdates(route53.ChangeActionUpsert, newHostnames, newTarget)
		}
		oldHostnames := getHostnamesFromIngress(oldIngress)
		oldTarget := r.getTargetForIngress(oldIngress)
		diffHostnames := diffStringSlices(oldHostnames, newHostnames)
		if len(diffHostnames) > 0 && oldTarget != "" {
			log.Printf("[DEBUG] queued deletion of %d records for ingress %s", len(diffHostnames), oldIngress.Name)
			r.queueUpdates(route53.ChangeActionDelete, diffHostnames, oldTarget)
		}
	case watch.Deleted:
		hostnames := getHostnamesFromIngress(oldIngress)
		target := r.getTargetForIngress(oldIngress)
		metricUpdatesReceived.WithLabelValues(oldIngress.Name, "delete").Inc()
		if len(hostnames) > 0 && target != "" {
			log.Printf("[DEBUG] queued deletion of %d records for ingress %s", len(hostnames), oldIngress.Name)
			r.queueUpdates(route53.ChangeActionDelete, hostnames, target)
		}
	}
}

func (r *registrator) queueUpdates(action string, hostnames []string, target string) {
	for _, h := range hostnames {
		r.updateQueue <- cnameChange{action, cnameRecord{h, target}}
	}
}

func (r *registrator) processUpdateQueue() {
	ret := []cnameChange{}
	for {
		select {
		case t := <-r.updateQueue:
			if len(ret) > 0 && ((ret[0].Action == route53.ChangeActionDelete && t.Action != route53.ChangeActionDelete) || (ret[0].Action != route53.ChangeActionDelete && t.Action == route53.ChangeActionDelete)) {
				r.applyBatch(ret)
				ret = []cnameChange{}
			}
			ret = append(ret, t)
		case <-r.stopChannel:
			if len(ret) > 0 {
				r.applyBatch(ret)
				ret = []cnameChange{}
			}
			return
		default:
			if len(ret) > 0 {
				r.applyBatch(ret)
				ret = []cnameChange{}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (r *registrator) applyBatch(changes []cnameChange) {
	action := changes[0].Action
	records := make([]cnameRecord, len(changes))
	for i, c := range changes {
		records[i] = c.Record
	}
	pruned := r.pruneBatch(action, records)
	if len(pruned) == 0 {
		return
	}

	hostnames := make([]string, len(pruned))
	for i, p := range pruned {
		hostnames[i] = p.Hostname
	}

	if action == route53.ChangeActionDelete {
		log.Printf("[INFO] deleting %d records: %+v", len(pruned), hostnames)
		if !*dryRun {
			if err := r.DeleteCnames(pruned); err != nil {
				log.Printf("[ERROR] error deleting records: %+v", err)
			} else {
				log.Printf("[INFO] records were deleted")
				for _, p := range pruned {
					metricUpdatesApplied.WithLabelValues(p.Hostname, "delete").Inc()
				}
			}
		}
	} else {
		log.Printf("[INFO] modifying %d records: %+v", len(pruned), hostnames)
		if !*dryRun {
			if err := r.UpsertCnames(pruned); err != nil {
				log.Printf("[ERROR] error modifying records: %+v", err)
			} else {
				log.Printf("[INFO] records were modified")
				for _, p := range pruned {
					metricUpdatesApplied.WithLabelValues(p.Hostname, "upsert").Inc()
				}
			}
		}
	}
}

func (r *registrator) getTargetForIngress(ingress *v1beta1.Ingress) string {
	for _, sat := range r.sats {
		if sat.Selector.Matches(labels.Set(ingress.Labels)) {
			return sat.Target
		}
	}

	if r.options.DefaultTarget != "" {
		log.Printf("[DEBUG] didn't find a valid selector for ingress: %s, using default: %s", ingress.Name, r.options.DefaultTarget)
		return r.options.DefaultTarget
	} else {
		// no valid selector and no default target specified. Do nothing
		log.Printf("[DEBUG] cannot compute target for ingress: %s, invalid selector and no default target set", ingress.Name, r.options.DefaultTarget)
		return ""
	}
}

func (r *registrator) pruneBatch(action string, records []cnameRecord) []cnameRecord {
	pruned := []cnameRecord{}
	for _, u := range records {
		if !r.canHandleRecord(u.Hostname) {
			metricUpdatesRejected.Inc()
			log.Printf("[INFO] cannot handle dns record '%s', will ignore it", u.Hostname)
			continue
		}

		t, err := resolveCname(fmt.Sprintf("%s.", strings.Trim(u.Hostname, ".")), r.ListNameservers())
		switch action {
		case route53.ChangeActionDelete:
			if err != errDNSEmptyAnswer {
				log.Printf("[DEBUG] error resolving '%s': %+v, will try to update the record", u.Hostname, err)
				pruned = append(pruned, u)
			}
		case route53.ChangeActionUpsert:
			if err != nil {
				log.Printf("[DEBUG] error resolving '%s': %+v, will try to update the record", u.Hostname, err)
				pruned = append(pruned, u)
			} else if strings.Trim(t, ".") != u.Target {
				pruned = append(pruned, u)
			}
		}
	}
	pruned = uniqueRecords(pruned)
	return pruned
}

func (r *registrator) canHandleRecord(record string) bool {
	zone := strings.Trim(r.Domain(), ".")
	record = strings.Trim(record, ".")
	matches, err := regexp.MatchString(fmt.Sprintf("^[^.]+\\.%s$", strings.Replace(zone, ".", "\\.", -1)), record)
	if err != nil {
		log.Printf("[DEBUG] regexp match error, will not handle record '%s': %+v", record, err)
	}
	return matches
}

func resolveCname(name string, nameservers []string) (string, error) {
	m := dns.Msg{}
	m.SetQuestion(name, dns.TypeCNAME)

	var retError error
	var retTarget string

	for _, nameserver := range nameservers {
		r, _, err := dnsClient.Exchange(&m, nameserver)
		if err != nil {
			retError = err
			continue
		}

		if len(r.Answer) == 0 {
			retError = errDNSEmptyAnswer
			continue
		}

		retTarget = r.Answer[0].(*dns.CNAME).Target
		retError = nil
		break
	}

	return retTarget, retError
}

func diffStringSlices(a []string, b []string) []string {
	ret := []string{}

	for _, va := range a {
		exists := false
		for _, vb := range b {
			if va == vb {
				exists = true
				break
			}
		}

		if !exists {
			ret = append(ret, va)
		}
	}

	return ret
}

func uniqueRecords(records []cnameRecord) []cnameRecord {
	uniqueRecords := []cnameRecord{}
	rejectedRecords := []string{}

	for i, r1 := range records {
		if stringInSlice(r1.Hostname, rejectedRecords) || recordHostnameInSlice(r1.Hostname, uniqueRecords) {
			continue
		}

		duplicates := []cnameRecord{}
		for j, r2 := range records {
			if i != j && r1.Hostname == r2.Hostname {
				duplicates = append(duplicates, r2)
			}
		}

		if recordTargetsAllMatch(r1.Target, duplicates) {
			uniqueRecords = append(uniqueRecords, r1)
		} else {
			rejectedRecords = append(rejectedRecords, r1.Hostname)
		}
	}

	if len(rejectedRecords) > 0 {
		metricUpdatesRejected.Add(float64(len(rejectedRecords)))
		log.Printf("[INFO] refusing to modify the following records: [%s]: claimed by multiple ingress pointing to different controllers", strings.Join(rejectedRecords, ", "))
	}

	return uniqueRecords
}

func stringInSlice(s string, slice []string) bool {
	for _, x := range slice {
		if s == x {
			return true
		}
	}
	return false
}

func recordHostnameInSlice(h string, records []cnameRecord) bool {
	for _, x := range records {
		if h == x.Hostname {
			return true
		}
	}
	return false
}

func recordTargetsAllMatch(target string, records []cnameRecord) bool {
	for _, r := range records {
		if target != r.Target {
			return false
		}
	}
	return true
}
