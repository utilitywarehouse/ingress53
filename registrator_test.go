package main

import (
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

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
	_, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, PublicHostname: "a", PrivateHostname: "b", Route53ZoneID: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
}

func TestRegistrator_GetTargetForIngress(t *testing.T) {
	// empty selector
	r, err := newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, PublicHostname: "a", PrivateHostname: "b", Route53ZoneID: "c"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
	if r.getTargetForIngress(testIngressB) != "b" {
		t.Errorf("getTargetForIngress returned unexpected value")
	}

	// proper selector
	r, err = newRegistratorWithOptions(registratorOptions{KubernetesConfig: &rest.Config{}, PublicHostname: "a", PrivateHostname: "b", Route53ZoneID: "c", PublicResourceSelector: "public=true"})
	if err != nil {
		t.Errorf("newRegistrator returned an unexpected error: %+v", err)
	}
	if r.getTargetForIngress(testIngressB) != "a" {
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

type mockEvent struct {
	et  watch.EventType
	old *v1beta1.Ingress
	new *v1beta1.Ingress
}

func TestRegistratorHandler(t *testing.T) {
	servers, serverAddresses, err := startMockDNSServerFleet(map[string]string{"baz.example.com.": "pub.example.com."})
	defer stopMockDNSServerFleet(servers)
	if err != nil {
		t.Fatalf("dnstest: unable to run test server: %v", err)
	}

	s, _ := labels.Parse("public=true")
	mdz := &mockDNSZone{nameservers: serverAddresses}
	r := &registrator{
		dnsZone:        mdz,
		publicSelector: s,
		updateQueue:    make(chan cnameRecord, 16),
		ingressWatcher: &ingressWatcher{
			stopChannel: make(chan struct{}),
		},
		options: registratorOptions{
			PrivateHostname: "priv.example.com",
			PublicHostname:  "pub.example.com",
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
				{watch.Added, nil, testIngressA},
			},
			map[string]string{
				"foo1.example.com": "priv.example.com",
				"foo2.example.com": "priv.example.com",
			},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, testIngressA},
				{watch.Deleted, testIngressA, nil},
			},
			map[string]string{},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, testIngressA},
				{watch.Modified, testIngressA, testIngressB},
			},
			map[string]string{
				"bar.example.com": "pub.example.com",
			},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, testIngressA},
				{watch.Deleted, testIngressA, nil},
				{watch.Added, nil, testIngressB},
			},
			map[string]string{
				"bar.example.com": "pub.example.com",
			},
		},
		{
			"an.example.com.",
			[]mockEvent{
				{watch.Added, nil, testIngressA},
			},
			map[string]string{},
		},
		{
			"example.com.",
			[]mockEvent{
				{watch.Added, nil, testIngressC},
			},
			map[string]string{
				"baz.example.com": "priv.example.com",
			},
		},
	}

	for i, test := range testCases {
		r.ingressWatcher.stopChannel = make(chan struct{})
		mdz.domain = test.domain
		mdz.zoneData = map[string]string{}
		r.updateQueue = make(chan cnameRecord, 16)
		for _, e := range test.events {
			r.handler(e.et, e.old, e.new)
		}
		go r.processUpdateQueue()
		time.Sleep(1000 * time.Millisecond) // XXX
		close(r.stopChannel)
		if !reflect.DeepEqual(mdz.zoneData, test.data) {
			t.Errorf("handler produced unexcepted zone data for test case #%02d: %+v", i, mdz.zoneData)
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
