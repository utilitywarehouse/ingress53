package main

import (
	"reflect"
	"testing"

	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
)

//	v1beta1fake "k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1/fake"

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
