package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/aws/aws-sdk-go/service/route53/route53iface"
)

type mockRoute53API struct {
	route53iface.Route53API
	getZoneResp   *route53.GetHostedZoneOutput
	getZoneErr    error
	getChangeResp *route53.GetChangeOutput
	getChangeErr  error
	listZonesResp *route53.ListHostedZonesByNameOutput
	listZonesErr  error
	changeRRResp  *route53.ChangeResourceRecordSetsOutput
	changeRRErr   error
}

func (m mockRoute53API) ListHostedZonesByName(in *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
	return m.listZonesResp, m.listZonesErr
}

func (m mockRoute53API) GetHostedZone(in *route53.GetHostedZoneInput) (*route53.GetHostedZoneOutput, error) {
	return m.getZoneResp, m.getZoneErr
}

func (m mockRoute53API) ChangeResourceRecordSets(in *route53.ChangeResourceRecordSetsInput) (*route53.ChangeResourceRecordSetsOutput, error) {
	return m.changeRRResp, m.changeRRErr
}

func (m mockRoute53API) GetChange(in *route53.GetChangeInput) (*route53.GetChangeOutput, error) {
	return m.getChangeResp, m.getChangeErr
}

func mockRoute53Timers() func() {
	dwi := defaultRoute53ZoneWaitWatchInterval
	dwt := defaultRoute53ZoneWaitWatchTimeout
	defaultRoute53ZoneWaitWatchInterval = 1 * time.Second
	defaultRoute53ZoneWaitWatchTimeout = 2 * time.Second
	return func() {
		defaultRoute53ZoneWaitWatchInterval = dwi
		defaultRoute53ZoneWaitWatchTimeout = dwt
	}
}

var (
	errTestRoute53ZoneMock = errors.New("test error")

	testRoute53ZoneListZonesOK = &route53.ListHostedZonesByNameOutput{
		DNSName: aws.String("example.com."),
		HostedZones: []*route53.HostedZone{
			&route53.HostedZone{
				ResourceRecordSetCount: aws.Int64(1),
				CallerReference:        aws.String(""),
				Config: &route53.HostedZoneConfig{
					Comment:     aws.String(""),
					PrivateZone: aws.Bool(false),
				},
				Id:   aws.String("/hostedzone/XXXXXXXXXXXXXX"),
				Name: aws.String("example.com."),
			},
		},
		NextHostedZoneId: aws.String(""),
		MaxItems:         aws.String("1"),
		NextDNSName:      aws.String(""),
		IsTruncated:      aws.Bool(false),
	}

	testRoute53ZoneGetZoneOK = &route53.GetHostedZoneOutput{
		HostedZone: &route53.HostedZone{
			ResourceRecordSetCount: aws.Int64(1),
			CallerReference:        aws.String(""),
			Config: &route53.HostedZoneConfig{
				Comment:     aws.String(""),
				PrivateZone: aws.Bool(false),
			},
			Id:   aws.String("/hostedzone/XXXXXXXXXXXXXX"),
			Name: aws.String("example.com."),
		},
		DelegationSet: &route53.DelegationSet{
			NameServers: []*string{
				aws.String("0.ns.example.com"),
				aws.String("1.ns.example.com"),
				aws.String("2.ns.example.com"),
				aws.String("3.ns.example.com"),
			},
			CallerReference: aws.String(""),
			Id:              aws.String("/delegationset/XXXXXXXXXXXXXX"),
		},
	}

	testRoute53ZoneChangeRROK = &route53.ChangeResourceRecordSetsOutput{
		ChangeInfo: &route53.ChangeInfo{
			Comment:     aws.String(""),
			Id:          aws.String("123456789"),
			Status:      aws.String(route53.ChangeStatusPending),
			SubmittedAt: aws.Time(time.Now()),
		},
	}

	testRoute53ZoneGetChangeOK = &route53.GetChangeOutput{
		ChangeInfo: &route53.ChangeInfo{
			Comment:     aws.String(""),
			Id:          aws.String("123456789"),
			Status:      aws.String(route53.ChangeStatusInsync),
			SubmittedAt: aws.Time(time.Now()),
		},
	}

	testRoute53ZoneGetChangePending = &route53.GetChangeOutput{
		ChangeInfo: &route53.ChangeInfo{
			Comment:     aws.String(""),
			Id:          aws.String("123456789"),
			Status:      aws.String(route53.ChangeStatusPending),
			SubmittedAt: aws.Time(time.Now()),
		},
	}

	expectedNameservers = []string{
		"0.ns.example.com",
		"1.ns.example.com",
		"2.ns.example.com",
		"3.ns.example.com",
	}
)

func TestRoute53Zone_UpsertCname(t *testing.T) {
	testCases := []struct {
		listZonesErr      error
		listZonesResponse *route53.ListHostedZonesByNameOutput
		getZoneErr        error
		getZoneResponse   *route53.GetHostedZoneOutput

		changeRRErr       error
		changeRRResponse  *route53.ChangeResourceRecordSetsOutput
		getChangeErr      error
		getChangeResponse *route53.GetChangeOutput

		recordName  string
		zoneName    string
		recordValue string

		expectedNewErr    error
		expectedUpsertErr error
	}{
		{ // error in list zones
			errTestRoute53ZoneMock,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			"test.example.com",
			"example.com.",
			"cname.example.com",
			errTestRoute53ZoneMock,
			nil,
		},
		{ // zone not found
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			"",
			"test.example.com",
			"cname.example.com",
			errRoute53NoHostedZoneFound,
			nil,
		},
		{ // error in get zone
			nil,
			testRoute53ZoneListZonesOK,
			errTestRoute53ZoneMock,
			nil,
			nil,
			nil,
			nil,
			nil,
			"test.example.com",
			"example.com.",
			"cname.example.com",
			errTestRoute53ZoneMock,
			nil,
		},
		{ // error record is apex
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			testRoute53ZoneGetZoneOK,
			errTestRoute53ZoneMock,
			nil,
			nil,
			nil,
			"example.com",
			"example.com.",
			"cname.example.com",
			nil,
			errRoute53RecordNotInZone,
		},
		{ // error in record zone
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			testRoute53ZoneGetZoneOK,
			errTestRoute53ZoneMock,
			nil,
			nil,
			nil,
			"test.example.org",
			"example.com.",
			"cname.example.com",
			nil,
			errRoute53RecordNotInZone,
		},
		{ // error record is invalid
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			testRoute53ZoneGetZoneOK,
			errTestRoute53ZoneMock,
			nil,
			nil,
			nil,
			"wrong.test.example.com",
			"example.com.",
			"cname.example.com",
			nil,
			errRoute53RecordNotInZone,
		},
		{ // error in change request
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			testRoute53ZoneGetZoneOK,
			errTestRoute53ZoneMock,
			nil,
			nil,
			nil,
			"test.example.com",
			"example.com.",
			"cname.example.com",
			nil,
			errTestRoute53ZoneMock,
		},
		{ // error in get change request
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			testRoute53ZoneGetZoneOK,
			nil,
			testRoute53ZoneChangeRROK,
			errTestRoute53ZoneMock,
			nil,
			"test.example.com",
			"example.com.",
			"cname.example.com",
			nil,
			errTestRoute53ZoneMock,
		},
		{ // timeout in get change
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			testRoute53ZoneGetZoneOK,
			nil,
			testRoute53ZoneChangeRROK,
			nil,
			testRoute53ZoneGetChangePending,
			"test.example.com",
			"example.com.",
			"cname.example.com",
			nil,
			errRoute53WaitWatchTimedOut,
		},
		{ // works end to end
			nil,
			testRoute53ZoneListZonesOK,
			nil,
			testRoute53ZoneGetZoneOK,
			nil,
			testRoute53ZoneChangeRROK,
			nil,
			testRoute53ZoneGetChangeOK,
			"test.example.com",
			"example.com.",
			"cname.example.com",
			nil,
			nil,
		},
	}

	defer mockRoute53Timers()()

	for _, tc := range testCases {
		p, err := newRoute53Zone(tc.zoneName, &mockRoute53API{
			getZoneResp:   tc.getZoneResponse,
			getZoneErr:    tc.getZoneErr,
			getChangeResp: tc.getChangeResponse,
			getChangeErr:  tc.getChangeErr,
			listZonesResp: tc.listZonesResponse,
			listZonesErr:  tc.listZonesErr,
			changeRRResp:  tc.changeRRResponse,
			changeRRErr:   tc.changeRRErr,
		})
		if err != tc.expectedNewErr {
			t.Errorf("newRoute53Zone returned unexpected error: %+v", err)
		}

		if tc.expectedNewErr != nil {
			continue
		}

		if p.Name != "example.com." {
			t.Errorf("Route53Zone has unexpected Name: %+v", p.Name)
		}

		if p.ID != "/hostedzone/XXXXXXXXXXXXXX" {
			t.Errorf("Route53Zone has unexpected ID: %+v", p.ID)
		}

		if !reflect.DeepEqual(p.Nameservers, expectedNameservers) {
			t.Errorf("Route53Zone has unexpected Nameservers: %+v", p.Nameservers)
		}

		if err := p.UpsertCname(tc.recordName, tc.recordValue); err != tc.expectedUpsertErr {
			t.Errorf("Route53Zone.UpsertCname returned unexpected error: %+v", err)
		}
	}
}
