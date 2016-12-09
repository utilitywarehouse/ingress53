package main

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/1.5/kubernetes/fake"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/watch"
)

var (
	testIngressA = &v1beta1.Ingress{
		ObjectMeta: v1.ObjectMeta{
			Name:      "exampleA",
			Namespace: api.NamespaceDefault,
			Labels:    map[string]string{},
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{
				{Host: "foo1.example.com"},
				{Host: "foo2.example.com"},
			},
		},
	}

	testIngressB = &v1beta1.Ingress{
		ObjectMeta: v1.ObjectMeta{
			Name:      "exampleB",
			Namespace: api.NamespaceDefault,
			Labels: map[string]string{
				"public": "true",
			},
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{
				{Host: "bar.example.com"},
			},
		},
	}

	testIngressB2 = &v1beta1.Ingress{
		ObjectMeta: v1.ObjectMeta{
			Name:      "exampleB",
			Namespace: api.NamespaceDefault,
			Labels: map[string]string{
				"public": "true",
			},
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{
				{Host: "bar2.example.com"},
			},
		},
	}
)

func Test_getHostnamesFromIngress(t *testing.T) {
	testCases := []struct {
		Spec     v1beta1.IngressSpec
		Expected []string
	}{
		// single value
		{
			Spec: v1beta1.IngressSpec{
				Rules: []v1beta1.IngressRule{
					{Host: "foo.example.com"},
				},
			},
			Expected: []string{"foo.example.com"},
		},
		// two values
		{
			Spec: v1beta1.IngressSpec{
				Rules: []v1beta1.IngressRule{
					{Host: "foo.example.com"},
					{Host: "bar.example.com"},
				},
			},
			Expected: []string{"foo.example.com", "bar.example.com"},
		},
		// duplicate
		{
			Spec: v1beta1.IngressSpec{
				Rules: []v1beta1.IngressRule{
					{Host: "foo.example.com"},
					{Host: "foo.example.com"},
				},
			},
			Expected: []string{"foo.example.com"},
		},
	}

	for i, tc := range testCases {
		ingress := &v1beta1.Ingress{Spec: tc.Spec}
		hostnames := getHostnamesFromIngress(ingress)

		if !reflect.DeepEqual(hostnames, tc.Expected) {
			t.Errorf("getHostnamesFromIngress returned unexpected results for test case #%02d: %+v", i, hostnames)
		}
	}
}

type testIngressEvent struct {
	et  watch.EventType
	old *v1beta1.Ingress
	new *v1beta1.Ingress
}

func newTestIngressWatcherClient(initial ...v1beta1.Ingress) (*fake.Clientset, *watch.FakeWatcher) {
	client := fake.NewSimpleClientset(&v1beta1.IngressList{Items: initial})
	watcher, _ := client.Extensions().Ingresses(api.NamespaceDefault).Watch(api.ListOptions{})
	return client, watcher.(*watch.FakeWatcher)
}

func waitForTrue(test func() bool, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	ticker := time.NewTicker(timeout / 100)
	defer func() {
		timer.Stop()
		ticker.Stop()
	}()
	for {
		select {
		case <-ticker.C:
			if test() {
				return nil
			}
		case <-timer.C:
			return fmt.Errorf("timed out")
		}
	}
}

func TestIngressWatcher(t *testing.T) {
	expected := []testIngressEvent{
		{watch.Added, nil, testIngressA},
		{watch.Added, nil, testIngressB},
		{watch.Deleted, testIngressA, nil},
		{watch.Modified, testIngressB, testIngressB2},
	}

	client, watcher := newTestIngressWatcherClient(*testIngressA, *testIngressB)

	pM := &sync.Mutex{}
	processed := []testIngressEvent{}
	iw := newIngressWatcher(client, func(t watch.EventType, o, n *v1beta1.Ingress) {
		pM.Lock()
		processed = append(processed, testIngressEvent{t, o, n})
		pM.Unlock()
	}, 0)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		iw.Start()
	}()

	// because the events are processed asynchronously, using a wait function
	// here guarantees that they will arrive in the correct order and enables
	// testing for equality
	pLenIs := func(n int) func() bool {
		return func() bool {
			pM.Lock()
			defer pM.Unlock()
			if len(processed) == n {
				return true
			}
			return false
		}
	}
	if err := waitForTrue(pLenIs(2), 10*time.Second); err != nil {
		t.Fatalf("timed out waiting for ingressWatcher to process events")
	}
	watcher.Delete(testIngressA)
	if err := waitForTrue(pLenIs(3), 10*time.Second); err != nil {
		t.Fatalf("timed out waiting for ingressWatcher to process events")
	}
	watcher.Modify(testIngressB2)
	if err := waitForTrue(pLenIs(4), 10*time.Second); err != nil {
		t.Fatalf("timed out waiting for ingressWatcher to process events")
	}

	iw.Stop()
	wg.Wait()

	pM.Lock()
	if !reflect.DeepEqual(processed, expected) {
		t.Errorf("ingressWatcher did not produce expected results: %+v != %+v", processed, expected)
	}
	pM.Unlock()
}
