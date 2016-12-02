package main

import (
	"fmt"
	"time"

	"github.com/golang/glog"

	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/runtime"
	"k8s.io/client-go/1.5/pkg/watch"
	"k8s.io/client-go/1.5/tools/cache"
)

type eventHandlerFunc func(eventType watch.EventType, oldIngress *v1beta1.Ingress, newIngress *v1beta1.Ingress)

type ingressWatcher struct {
	client       kubernetes.Interface
	eventHandler eventHandlerFunc
	resyncPeriod time.Duration
	stopChannel  chan struct{}
}

func newIngressWatcher(client kubernetes.Interface, eventHandler eventHandlerFunc, resyncPeriod time.Duration) *ingressWatcher {
	return &ingressWatcher{
		client:       client,
		eventHandler: eventHandler,
		resyncPeriod: resyncPeriod,
		stopChannel:  make(chan struct{}),
	}
}

func (iw *ingressWatcher) Start() {
	lw := &cache.ListWatch{
		ListFunc: func(options api.ListOptions) (runtime.Object, error) {
			return iw.client.Extensions().Ingresses(api.NamespaceAll).List(options)
		},
		WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
			return iw.client.Extensions().Ingresses(api.NamespaceAll).Watch(options)
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

	glog.V(2).Infof("starting ingress watcher controller")

	controller.Run(iw.stopChannel)

	fmt.Println("yo")
	glog.V(2).Infof("ingress watcher controller stopped")
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
