package main

import (
	"errors"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/aws/aws-sdk-go/service/route53/route53iface"
	"github.com/golang/glog"
)

var (
	errRoute53NoHostedZoneFound = errors.New("could not find a Route53 hosted zone")
	errRoute53WaitWatchTimedOut = errors.New("timed out waiting for changes to be applied")

	defaultRoute53RecordTTL             int64 = 60
	defaultRoute53ZoneWaitWatchInterval       = 10 * time.Second
	defaultRoute53ZoneWaitWatchTimeout        = 2 * time.Minute
)

type route53Zone struct {
	api         route53iface.Route53API
	Name        string
	ID          string
	Nameservers []string
}

func newRoute53Zone(zoneName string, route53session route53iface.Route53API) (*route53Zone, error) {
	ret := &route53Zone{
		api: route53session,
	}

	if err := ret.setZone(zoneName); err != nil {
		return nil, err
	}

	return ret, nil
}

func (z *route53Zone) UpsertCname(recordName string, value string) error {
	glog.Infof("updating Route53 record '%s'", recordName)

	resp, err := z.api.ChangeResourceRecordSets(&route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String(route53.ChangeActionUpsert),
					ResourceRecordSet: &route53.ResourceRecordSet{
						Name:            aws.String(recordName),
						TTL:             aws.Int64(defaultRoute53RecordTTL),
						Type:            aws.String(route53.RRTypeCname),
						ResourceRecords: []*route53.ResourceRecord{{Value: aws.String(value)}},
					},
				},
			},
			Comment: aws.String("Managed by ingress-route53-registrator"),
		},
		HostedZoneId: aws.String(z.ID),
	})
	if err != nil {
		return err
	}

	glog.Infof("waiting for Route53 changes to apply ...")
	return z.waitForChange(*resp.ChangeInfo.Id)
}

func (z *route53Zone) waitForChange(changeID string) error {
	timeout := time.NewTimer(defaultRoute53ZoneWaitWatchTimeout)
	tick := time.NewTicker(defaultRoute53ZoneWaitWatchInterval)
	defer func() {
		timeout.Stop()
		tick.Stop()
	}()

	var err error
	var change *route53.GetChangeOutput

	for {
		select {
		case <-tick.C:
			change, err = z.api.GetChange(&route53.GetChangeInput{Id: aws.String(changeID)})
			if err != nil {
				return err
			}

			if *change.ChangeInfo.Status == route53.ChangeStatusInsync {
				return nil
			}
		case <-timeout.C:
			return errRoute53WaitWatchTimedOut
		}
	}
}

func (z *route53Zone) setZone(name string) error {
	zones, err := z.api.ListHostedZonesByName(&route53.ListHostedZonesByNameInput{
		DNSName:  aws.String(name),
		MaxItems: aws.String("1"),
	})
	if err != nil {
		return err
	}

	if len(zones.HostedZones) == 0 || *zones.HostedZones[0].Name != name {
		return errRoute53NoHostedZoneFound
	}

	glog.Infof("selected Route53 hosted zone '%s'", zones.HostedZones[0].Id)

	zone, err := z.api.GetHostedZone(&route53.GetHostedZoneInput{
		Id: zones.HostedZones[0].Id,
	})
	if err != nil {
		return err
	}

	z.Name = name
	z.ID = *zone.HostedZone.Id
	z.Nameservers = make([]string, len(zone.DelegationSet.NameServers))
	for i, ns := range zone.DelegationSet.NameServers {
		z.Nameservers[i] = *ns
	}

	return nil
}
