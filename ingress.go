package main

import (
	"log"
	"time"

	"k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type eventHandlerFunc func(eventType watch.EventType, oldIngress *v1beta1.Ingress, newIngress *v1beta1.Ingress)

type ingressWatcher struct {
	client        kubernetes.Interface
	eventHandler  eventHandlerFunc
	resyncPeriod  time.Duration
	labelSelector string
	stopChannel   chan struct{}
}

func newIngressWatcher(client kubernetes.Interface, eventHandler eventHandlerFunc, labelSelector string, resyncPeriod time.Duration) *ingressWatcher {
	return &ingressWatcher{
		client:        client,
		eventHandler:  eventHandler,
		resyncPeriod:  resyncPeriod,
		labelSelector: labelSelector,
		stopChannel:   make(chan struct{}),
	}
}

func (iw *ingressWatcher) Start() {
	lw := &cache.ListWatch{
		ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = iw.labelSelector
			return iw.client.Extensions().Ingresses(v1.NamespaceAll).List(options)
		},
		WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = iw.labelSelector
			return iw.client.Extensions().Ingresses(v1.NamespaceAll).Watch(options)
		},
	}

	eh := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			iw.eventHandler(watch.Added, nil, obj.(*v1beta1.Ingress))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			iw.eventHandler(watch.Modified, oldObj.(*v1beta1.Ingress), newObj.(*v1beta1.Ingress))
		},
		DeleteFunc: func(obj interface{}) {
			iw.eventHandler(watch.Deleted, obj.(*v1beta1.Ingress), nil)
		},
	}

	_, controller := cache.NewInformer(lw, &v1beta1.Ingress{}, iw.resyncPeriod, eh)
	log.Println("[INFO] starting ingress watcher")
	controller.Run(iw.stopChannel)
	log.Println("[INFO] ingress watcher stopped")
}

func (iw *ingressWatcher) Stop() {
	log.Println("[INFO] stopping ingress watcher ...")
	close(iw.stopChannel)
}

func getHostnamesFromIngress(ingress *v1beta1.Ingress) []string {
	hostnames := []string{}

	for _, rule := range ingress.Spec.Rules {
		found := false

		for _, h := range hostnames {
			if h == rule.Host {
				found = true
				break
			}
		}

		if !found {
			hostnames = append(hostnames, rule.Host)
		}
	}

	return hostnames
}
