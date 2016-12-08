package main

import (
	"log"
	"time"

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
	log.Println("[INFO] starting ingress watcher")

	// TODO: change controller startup when it's fixed upstream
	// The controller is started like so: wait.Until(c.processLoop, time.Second, stopCh)
	// However, the processLoop function does not ever return which means that
	// `wait.Until()` is unable to exit cleanly when `stopCh` is closed.
	// Closing `stopCh`, however, will stop the controller from processing
	// events since the internal reflector is stopped properly. This is why the
	// controller is started in a go func below.
	go controller.Run(iw.stopChannel)
	<-iw.stopChannel

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
