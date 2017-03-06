package main

import (
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/watch"
	"k8s.io/client-go/1.5/rest"
)

const (
	testPrivateTarget   string = "private.cluster-entrypoint.com"
	testPublicTarget    string = "public.cluster-entrypoint.com"
	testTargetLabelName string = "ingress53.target"
	testIgnoreLabelName string = "ingress53.ignore"
)

func TestNewRegistrator_defaults(t *testing.T) {
	_, err := newRegistrator("z", []string{testPrivateTarget, testPublicTarget}, testTargetLabelName, testIgnoreLabelName, testPrivateTarget)
	if err == nil || err.Error() != "unable to load in-cluster configuration, KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT must be defined" {
		t.Errorf("newRegistrator did not return expected error")
	}

	// missing options
	_, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}})
	if err != errRegistratorMissingOption {
		t.Errorf("newRegistrator did not return expected error")
	}

	// invalid label name
	_, err = newRegistrator("z", []string{testPrivateTarget, testPublicTarget}, "!^7", testIgnoreLabelName, "")
	if err == nil {
		t.Errorf("newRegistrator did not return expected error")
	}

	// invalid label name
	_, err = newRegistrator("z", []string{testPrivateTarget, testPublicTarget}, testTargetLabelName, "!^7", "")
	if err == nil {
		t.Errorf("newRegistrator did not return expected error")
	}

	// working
	_, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, Targets: []string{testPrivateTarget, testPublicTarget}, TargetLabelName: testTargetLabelName, IgnoreLabelName: testIgnoreLabelName, Route53ZoneID: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
}

func TestRegistrator_GetTargetForIngress(t *testing.T) {
	// ingress ab
	r, err := newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, Targets: []string{testPrivateTarget, testPublicTarget}, TargetLabelName: testTargetLabelName, IgnoreLabelName: testIgnoreLabelName, Route53ZoneID: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}

	target := r.getTargetForIngress(privateIngressHostsAB)
	if target != testPrivateTarget {
		t.Errorf("getTargetForIngress returned unexpected value")
	}

	// ingress c
	r, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, Targets: []string{testPrivateTarget, testPublicTarget}, TargetLabelName: testTargetLabelName, IgnoreLabelName: testIgnoreLabelName, Route53ZoneID: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
	target = r.getTargetForIngress(publicIngressHostC)
	if target != testPublicTarget {
		t.Errorf("getTargetForIngress returned unexpected value")
	}

	// ingress default
	r, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, Targets: []string{testPrivateTarget, testPublicTarget}, DefaultTarget: testPrivateTarget, TargetLabelName: testTargetLabelName, IgnoreLabelName: testIgnoreLabelName, Route53ZoneID: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
	target = r.getTargetForIngress(ingressNoLabels)
	if target != testPrivateTarget {
		t.Errorf("getTargetForIngress returned unexpected value")
	}

	// ingress target not registered with ingress53
	r, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, Targets: []string{testPrivateTarget, testPublicTarget}, TargetLabelName: testTargetLabelName, IgnoreLabelName: testIgnoreLabelName, Route53ZoneID: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
	target = r.getTargetForIngress(nonRegisteredIngress)
	if target != "" {
		t.Errorf("getTargetForIngress returned unexpected value")
	}
}

type mockDNSZone struct {
	zoneData    map[string]string
	domain      string
	nameservers []string
}

func (m *mockDNSZone) UpsertCnames(records []cnameRecord) error {
	for _, r := range records {
		m.zoneData[r.Hostname] = r.Target
	}
	return nil
}

func (m *mockDNSZone) DeleteCnames(records []cnameRecord) error {
	for _, r := range records {
		delete(m.zoneData, r.Hostname)
	}
	return nil
}

func (m *mockDNSZone) Domain() string { return m.domain }

func (m *mockDNSZone) ListNameservers() []string { return m.nameservers }

func (m *mockDNSZone) startMockDNSServer() (*dns.Server, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		msg := new(dns.Msg)
		msg.SetReply(req)
		msg.Authoritative = true
		if target, ok := m.zoneData[strings.Trim(req.Question[0].Name, ".")]; ok {
			msg.Answer = append(msg.Answer, &dns.CNAME{
				Hdr: dns.RR_Header{
					Name:   req.Question[0].Name,
					Rrtype: dns.TypeCNAME,
					Class:  dns.ClassINET,
					Ttl:    0,
				},
				Target: fmt.Sprintf("%s.", target),
			})
		}
		w.WriteMsg(msg)
	})

	server := &dns.Server{
		PacketConn:   pc,
		ReadTimeout:  time.Hour,
		WriteTimeout: time.Hour,
		Handler:      mux,
	}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = waitLock.Unlock

	go func() {
		server.ActivateAndServe()
		pc.Close()
	}()

	waitLock.Lock()

	m.nameservers = []string{pc.LocalAddr().String()}

	return server, nil
}

type mockEvent struct {
	et  watch.EventType
	old *v1beta1.Ingress
	new *v1beta1.Ingress
}

func TestRegistratorHandler(t *testing.T) {
	privateSelector, _ := labels.Parse(fmt.Sprintf("%s=%s", testTargetLabelName, testPrivateTarget))
	publicSelector, _ := labels.Parse(fmt.Sprintf("%s=%s", testTargetLabelName, testPublicTarget))
	ignoreSelector, _ := labels.Parse(fmt.Sprintf("%s=true", testIgnoreLabelName))
	sats := []selectorAndTarget{selectorAndTarget{Selector: privateSelector, Target: testPrivateTarget}, selectorAndTarget{Selector: publicSelector, Target: testPublicTarget}}

	mdz := &mockDNSZone{}
	server, err := mdz.startMockDNSServer()
	defer server.Shutdown()
	if err != nil {
		t.Fatalf("dnstest: unable to run test server: %v", err)
	}

	r := &registrator{
		dnsZone:        mdz,
		sats:           sats,
		ignoreSelector: ignoreSelector,
		updateQueue:    make(chan cnameChange, 16),
		ingressWatcher: &ingressWatcher{
			stopChannel: make(chan struct{}),
		},
		options: registratorOptions{
			Targets:         []string{testPrivateTarget, testPublicTarget},
			TargetLabelName: testTargetLabelName,
			IgnoreLabelName: testIgnoreLabelName,
			Route53ZoneID:   "c",
		},
	}

	testCases := []struct {
		domain string
		events []mockEvent
		data   map[string]string
	}{
		{
			"",
			[]mockEvent{},
			map[string]string{},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostsAB},
			},
			map[string]string{
				"a.example.com": testPrivateTarget,
				"b.example.com": testPrivateTarget,
			},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostAIgnored},
			},
			map[string]string{},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostsAB},
				{watch.Deleted, privateIngressHostsAB, nil},
			},
			map[string]string{},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostsAB},
				{watch.Modified, privateIngressHostsAB, publicIngressHostC},
			},
			map[string]string{
				"c.example.com": testPublicTarget,
			},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostsAB},
				{watch.Deleted, privateIngressHostsAB, nil},
				{watch.Added, nil, publicIngressHostC},
			},
			map[string]string{
				"c.example.com": testPublicTarget,
			},
		},
		{
			"an.example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostsAB},
			},
			map[string]string{},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostE},
			},
			map[string]string{
				"e.example.com": testPrivateTarget,
			},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostE},
				{watch.Added, nil, privateIngressHostEDup},
			},
			map[string]string{
				"e.example.com": testPrivateTarget,
			},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostE},
				{watch.Added, nil, publicIngressHostEDup},
			},
			map[string]string{},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, privateIngressHostE},
				{watch.Deleted, privateIngressHostsAB, nil},
				{watch.Modified, privateIngressHostE, publicIngressHostEDup},
			},
			map[string]string{
				"e.example.com": testPublicTarget,
			},
		},
	}

	for i, test := range testCases {
		r.ingressWatcher.stopChannel = make(chan struct{})
		mdz.domain = test.domain
		mdz.zoneData = map[string]string{}
		r.updateQueue = make(chan cnameChange, 16)
		for _, e := range test.events {
			r.handler(e.et, e.old, e.new)
		}
		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.processUpdateQueue()
		}()
		time.Sleep(1000 * time.Millisecond) // XXX
		close(r.stopChannel)
		wg.Wait()
		if !reflect.DeepEqual(mdz.zoneData, test.data) {
			t.Errorf("handler produced unexcepted zone data for test case #%02d: %+v, expected: %+v", i, mdz.zoneData, test.data)
		}
	}
}

func TestRegistrator_canHandleRecord(t *testing.T) {
	testCases := []struct {
		record   string
		expected bool
	}{
		{"example.com", false},             // apex
		{"test.example.org", false},        // different zone
		{"wrong.test.example.com.", false}, // too deep
		{"test.example.com", true},
		{"test.example.com.", true},
	}
	defer mockRoute53Timers()()
	r := registrator{dnsZone: &mockDNSZone{domain: "example.com"}}

	for i, tc := range testCases {
		v := r.canHandleRecord(tc.record)
		if v != tc.expected {
			t.Errorf("newRoute53Zone returned unexpected value for test case #%02d: %v", i, v)
		}
	}
}

func setupMockDNSRecord(mux *dns.ServeMux, name string, target string) {
	mux.HandleFunc(name, func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		m.Authoritative = true
		m.Answer = append(m.Answer, &dns.CNAME{
			Hdr: dns.RR_Header{
				Name:   req.Question[0].Name,
				Rrtype: dns.TypeCNAME,
				Class:  dns.ClassINET,
				Ttl:    0,
			},
			Target: target,
		})
		w.WriteMsg(m)
	})
}

func startMockDNSServer(laddr string, records map[string]string) (*dns.Server, string, error) {
	pc, err := net.ListenPacket("udp", laddr)
	if err != nil {
		return nil, "", err
	}

	mux := dns.NewServeMux()
	for n, r := range records {
		setupMockDNSRecord(mux, n, r)
	}

	server := &dns.Server{
		PacketConn:   pc,
		ReadTimeout:  time.Hour,
		WriteTimeout: time.Hour,
		Handler:      mux,
	}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = waitLock.Unlock

	go func() {
		server.ActivateAndServe()
		pc.Close()
	}()

	waitLock.Lock()
	return server, pc.LocalAddr().String(), nil
}

func startMockDNSServerFleet(records map[string]string) ([]*dns.Server, []string, error) {
	servers := []*dns.Server{}
	serverAddresses := []string{}

	s, addr, err := startMockDNSServer("127.0.0.1:0", records)
	if err != nil {
		return nil, nil, err
	}
	servers = append(servers, s)
	serverAddresses = append(serverAddresses, addr)

	s, addr, err = startMockDNSServer("127.0.0.1:0", records)
	if err != nil {
		return nil, nil, err
	}
	servers = append(servers, s)
	serverAddresses = append(serverAddresses, addr)

	s, addr, err = startMockDNSServer("127.0.0.1:0", records)
	if err != nil {
		return nil, nil, err
	}
	servers = append(servers, s)
	serverAddresses = append(serverAddresses, addr)

	s, addr, err = startMockDNSServer("127.0.0.1:0", records)
	if err != nil {
		return nil, nil, err
	}
	servers = append(servers, s)
	serverAddresses = append(serverAddresses, addr)

	return servers, serverAddresses, nil
}

func startMockSemiBrokenDNSServerFleet(records map[string]string) ([]*dns.Server, []string, error) {
	servers := []*dns.Server{&dns.Server{}}
	serverAddresses := []string{"127.0.0.1:10000"}

	s, addr, err := startMockDNSServer("127.0.0.1:0", nil)
	if err != nil {
		return nil, nil, err
	}
	servers = append(servers, s)
	serverAddresses = append(serverAddresses, addr)

	s, addr, err = startMockDNSServer("127.0.0.1:0", records)
	if err != nil {
		return nil, nil, err
	}
	servers = append(servers, s)
	serverAddresses = append(serverAddresses, addr)

	s, addr, err = startMockDNSServer("127.0.0.1:0", records)
	if err != nil {
		return nil, nil, err
	}
	servers = append(servers, s)
	serverAddresses = append(serverAddresses, addr)

	return servers, serverAddresses, nil
}

func stopMockDNSServerFleet(servers []*dns.Server) {
	for _, s := range servers {
		s.Shutdown()
	}
}

func TestDNSClient_ResolveCname_noServer(t *testing.T) {
	_, err := resolveCname("example.com.", []string{"127.0.0.1:65111"})
	if err == nil {
		t.Fatalf("Client.ResolveA should have returned an error")
	}
}

func TestDNSClient_ResolveCname_empty(t *testing.T) {
	servers, serverAddresses, err := startMockDNSServerFleet(map[string]string{})
	defer stopMockDNSServerFleet(servers)
	if err != nil {
		t.Fatalf("dnstest: unable to run test server: %v", err)
	}

	_, err = resolveCname("example.com.", serverAddresses)
	if err != errDNSEmptyAnswer {
		t.Fatalf("Client.ResolveA should have returned an empty answer error")
	}
}

func TestDNSClient_ResolveCname_broken(t *testing.T) {
	servers, serverAddresses, err := startMockSemiBrokenDNSServerFleet(map[string]string{"example.com.": "target.example.com."})
	defer stopMockDNSServerFleet(servers)
	if err != nil {
		t.Fatalf("dnstest: unable to run test server: %v", err)
	}

	resp, err := resolveCname("example.com.", serverAddresses)
	if err != nil {
		t.Fatalf("Client.ResolveA returned unexpected error: %+v", err)
	}

	if resp != "target.example.com." {
		t.Fatalf("Client.ResolveA returned unexpected response")
	}
}

func TestDiffStringSlices(t *testing.T) {
	testCases := []struct {
		A []string
		B []string
		R []string
	}{
		{
			A: []string{"A"},
			B: []string{"A"},
			R: []string{},
		},
		{
			A: []string{"A"},
			B: []string{"B"},
			R: []string{"A"},
		},
		{
			A: []string{},
			B: []string{"B"},
			R: []string{},
		},
		{
			A: []string{"A"},
			B: []string{},
			R: []string{"A"},
		},
		{
			A: []string{"A", "B", "C"},
			B: []string{"B", "D", "A"},
			R: []string{"C"},
		},
	}

	for i, test := range testCases {
		r := diffStringSlices(test.A, test.B)
		if !reflect.DeepEqual(r, test.R) {
			t.Errorf("diffStringSlices returned unexpected result for test case #%02d: %+v", i, r)
		}
	}

}
