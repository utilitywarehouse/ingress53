package main

import (
	"reflect"
	"testing"

	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/watch"
	"k8s.io/client-go/1.5/rest"
)

func TestNewRegistrator_defaults(t *testing.T) {
	_, err := newRegistrator("z", "a", "b", "")
	if err == nil || err.Error() != "unable to load in-cluster configuration, KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT must be defined" {
		t.Errorf("newRegistrator did not return expected error")
	}

	// missing options
	_, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}})
	if err != errRegistratorMissingOption {
		t.Errorf("newRegistrator did not return expected error")
	}

	// invalid selector
	_, err = newRegistrator("z", "a", "b", "a^b")
	if err == nil {
		t.Errorf("newRegistrator did not return expected error")
	}

	// working
	_, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, PublicHostname: "a", PrivateHostname: "b", Route53ZoneName: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
}

func TestRegistrator_GetTargetForIngress(t *testing.T) {
	// empty selector
	r, err := newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, PublicHostname: "a", PrivateHostname: "b", Route53ZoneName: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
	if r.getTargetForIngress(testIngressB) != "b" {
		t.Errorf("getTargetForIngress returned unexpected value")
	}

	// proper selector
	r, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, PublicHostname: "a", PrivateHostname: "b", Route53ZoneName: "c", PublicResourceSelector: "public=true"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
	if r.getTargetForIngress(testIngressB) != "a" {
		t.Errorf("getTargetForIngress returned unexpected value")
	}
}

type mockDNSZone struct {
	zoneData map[string]string
}

func (m *mockDNSZone) UpsertCname(recordName string, value string) error {
	m.zoneData[recordName] = value
	return nil
}

func (m *mockDNSZone) DeleteCname(recordName string) error {
	delete(m.zoneData, recordName)
	return nil
}

type mockEvent struct {
	et  watch.EventType
	old *v1beta1.Ingress
	new *v1beta1.Ingress
}

func TestRegistratorHandler(t *testing.T) {
	s, _ := labels.Parse("public=true")
	mdz := &mockDNSZone{zoneData: map[string]string{}}
	r := &registrator{dnsZone: mdz, publicSelector: s, options: registratorOptions{PrivateHostname: "priv.example.com", PublicHostname: "pub.example.com", Route53ZoneName: "c"}}

	testCases := []struct {
		events []mockEvent
		data   map[string]string
	}{
		{
			[]mockEvent{
				{
					watch.Added,
					nil,
					testIngressA,
				},
			},
			map[string]string{
				"foo1.example.com": "priv.example.com",
				"foo2.example.com": "priv.example.com",
			},
		},
		{
			[]mockEvent{
				{
					watch.Added,
					nil,
					testIngressA,
				},
				{
					watch.Deleted,
					testIngressA,
					nil,
				},
			},
			map[string]string{},
		},
		{
			[]mockEvent{
				{
					watch.Added,
					nil,
					testIngressA,
				},
				{
					watch.Modified,
					testIngressA,
					testIngressB,
				},
			},
			map[string]string{
				"bar.example.com": "pub.example.com",
			},
		},
	}

	for i, test := range testCases {
		for _, e := range test.events {
			r.handler(e.et, e.old, e.new)
		}

		if !reflect.DeepEqual(mdz.zoneData, test.data) {
			t.Errorf("handler produced unexcepted zone data for test case #%02d: %+v", i, mdz.zoneData)
		}
	}

}
