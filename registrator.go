package main

import (
	"errors"
	"log"
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
)

type dnsZone interface {
	UpsertCnames(records []cnameRecord) error
	DeleteCnames(records []cnameRecord) error
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

	return &registrator{options: options, publicSelector: publicSelector}, nil

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

	r.ingressWatcher.Start()
	return nil
}

func (r *registrator) handler(eventType watch.EventType, oldIngress *v1beta1.Ingress, newIngress *v1beta1.Ingress) {
	switch eventType {
	case watch.Added:
		hostnames := getHostnamesFromIngress(newIngress)
		target := r.getTargetForIngress(newIngress)
		if len(hostnames) > 0 {
			log.Printf("[INFO] updating %d records for ingress %s, pointing to %s", len(hostnames), newIngress.Name, target)
			if !*dryRun {
				for _, h := range hostnames {
					if err := r.UpsertCname(h, target); err != nil {
						log.Printf("[ERROR] error while updating CNAME record '%s': %+v", h, err)
					}
				}
			}
		}
	case watch.Modified:
		newHostnames := getHostnamesFromIngress(newIngress)
		target := r.getTargetForIngress(newIngress)
		if len(newHostnames) > 0 {
			log.Printf("[INFO] updating %d records for ingress %s, pointing to %s", len(newHostnames), newIngress.Name, target)
			if !*dryRun {
				for _, h := range newHostnames {
					if err := r.UpsertCname(h, target); err != nil {
						log.Printf("[ERROR] error while updating CNAME record '%s': %+v", h, err)
					}
				}
			}
		}

		oldHostnames := getHostnamesFromIngress(oldIngress)
		diffHostnames := diffStringSlices(oldHostnames, newHostnames)
		if len(diffHostnames) > 0 {
			log.Printf("[INFO] deleting %d old hostnames for ingress '%s'\n", len(diffHostnames), oldIngress.Name)
			if !*dryRun {
				for _, h := range diffHostnames {
					if err := r.DeleteCname(h); err != nil {
						log.Printf("[ERROR] error while deleting CNAME record '%s': %+v", h, err)
					}
				}
			}
		}
	case watch.Deleted:
		hostnames := getHostnamesFromIngress(oldIngress)
		if len(hostnames) > 0 {
			log.Printf("[INFO] deleting %d hostnames for ingress '%s'\n", len(hostnames), oldIngress.Name)
			if !*dryRun {
				for _, h := range hostnames {
					if err := r.DeleteCname(h); err != nil {
						log.Printf("[ERROR] error while deleting CNAME record '%s': %+v", h, err)
					}
				}
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
