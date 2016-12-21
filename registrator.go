package main

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/watch"
	"k8s.io/client-go/1.5/rest"
)

var (
	errRegistratorMissingOption = errors.New("missing required registrator option")
	defaultResyncPeriod         = 15 * time.Minute
	defaultBatchProcessCycle    = 5 * time.Second
)

type dnsZone interface {
	UpsertCnames(records []cnameRecord) error
	DeleteCnames(records []cnameRecord) error
	Domain() string
}

type cnameRecord struct {
	Hostname string
	Target   string
}

type registrator struct {
	dnsZone
	*ingressWatcher
	options        registratorOptions
	publicSelector labels.Selector
	updateQueue    chan cnameRecord
}

type registratorOptions struct {
	AWSSessionOptions      *session.Options
	KubernetesConfig       *rest.Config
	PrivateHostname        string // required
	PublicHostname         string // required
	PublicResourceSelector string
	Route53ZoneID          string // required
	ResyncPeriod           time.Duration
}

func newRegistrator(zoneID, publicHostname, privateHostname, publicSelector string) (*registrator, error) {
	return newRegistratorWithOptions(registratorOptions{
		PrivateHostname:        privateHostname,
		PublicHostname:         publicHostname,
		PublicResourceSelector: publicSelector,
		Route53ZoneID:          zoneID,
	})
}

func newRegistratorWithOptions(options registratorOptions) (*registrator, error) {
	if options.PrivateHostname == "" || options.PublicHostname == "" || options.Route53ZoneID == "" {
		return nil, errRegistratorMissingOption
	}

	var publicSelector labels.Selector
	if options.PublicResourceSelector == "" {
		publicSelector = labels.Nothing()
	} else {
		s, err := labels.Parse(options.PublicResourceSelector)
		if err != nil {
			return nil, err
		}
		publicSelector = s
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
		options:        options,
		publicSelector: publicSelector,
		updateQueue:    make(chan cnameRecord, 64),
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
		if len(hostnames) > 0 {
			log.Printf("[DEBUG] queued update of %d records for ingress %s, pointing to %s", len(hostnames), newIngress.Name, target)
			r.queueUpdates(hostnames, target)
		}
	case watch.Modified:
		newHostnames := getHostnamesFromIngress(newIngress)
		target := r.getTargetForIngress(newIngress)
		if len(newHostnames) > 0 {
			log.Printf("[DEBUG] queued update of %d records for ingress %s, pointing to %s", len(newHostnames), newIngress.Name, target)
			r.queueUpdates(newHostnames, target)
		}
		oldHostnames := getHostnamesFromIngress(oldIngress)
		diffHostnames := diffStringSlices(oldHostnames, newHostnames)
		if len(diffHostnames) > 0 {
			log.Printf("[DEBUG] queued deletion of %d records for ingress %s", len(diffHostnames), oldIngress.Name)
			r.queueUpdates(diffHostnames, "")
		}
	case watch.Deleted:
		hostnames := getHostnamesFromIngress(oldIngress)
		if len(hostnames) > 0 {
			log.Printf("[DEBUG] queued deletion of %d records for ingress %s", len(hostnames), oldIngress.Name)
			r.queueUpdates(hostnames, "")
		}
	}
}

func (r *registrator) queueUpdates(hostnames []string, target string) {
	for _, h := range hostnames {
		r.updateQueue <- cnameRecord{h, target}
	}
}

func (r *registrator) processUpdateQueue() {
	ret := []cnameRecord{}
	for {
		select {
		case t := <-r.updateQueue:
			if len(ret) > 0 && ((ret[0].Target == "" && t.Target != "") || (ret[0].Target != "" && t.Target == "")) {
				r.applyBatch(ret)
				ret = []cnameRecord{}
			}
			ret = append(ret, t)
		case <-r.stopChannel:
			if len(ret) > 0 {
				r.applyBatch(ret)
				ret = []cnameRecord{}
			}
			return
		default:
			if len(ret) > 0 {
				r.applyBatch(ret)
				ret = []cnameRecord{}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (r *registrator) applyBatch(records []cnameRecord) {
	pruned := r.pruneBatch(records)
	if len(pruned) == 0 {
		return
	}

	if pruned[0].Target == "" {
		log.Printf("[INFO] deleting %d records", len(pruned))
		if !*dryRun {
			if err := r.DeleteCnames(pruned); err != nil {
				log.Printf("[ERROR] error deleting records: %+v", err)
			}
		}
	} else {
		log.Printf("[INFO] modifying %d records", len(pruned))
		if !*dryRun {
			if err := r.UpsertCnames(pruned); err != nil {
				log.Printf("[ERROR] error modifying records: %+v", err)
			}
		}
	}
}

func (r *registrator) getTargetForIngress(ingress *v1beta1.Ingress) string {
	if r.publicSelector.Matches(labels.Set(ingress.Labels)) {
		return r.options.PublicHostname
	}
	return r.options.PrivateHostname
}

func (r *registrator) pruneBatch(records []cnameRecord) []cnameRecord {
	pruned := []cnameRecord{}
	for _, u := range records {
		if !r.canHandleRecord(u.Hostname) {
			log.Printf("[ERROR] cannot handle dns record '%s', will ignore it", u.Hostname)
		} else {
			pruned = append(pruned, u)
		}
	}
	return pruned
}

func (r *registrator) canHandleRecord(record string) bool {
	zone := strings.Trim(r.Domain(), ".")
	record = strings.Trim(record, ".")

	if record == zone {
		return false
	}

	zoneSuffix := "." + zone
	if !strings.HasSuffix(record, zoneSuffix) {
		return false
	}

	return !strings.Contains(strings.TrimSuffix(record, zoneSuffix), ".")
}
