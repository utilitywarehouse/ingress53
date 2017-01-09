package main

import (
	"errors"
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/aws/aws-sdk-go/service/route53/route53iface"
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

func newRoute53Zone(zoneID string, route53session route53iface.Route53API) (*route53Zone, error) {
	ret := &route53Zone{
		api: route53session,
	}

	if err := ret.setZone(zoneID); err != nil {
		return nil, err
	}

	return ret, nil
}

func (z *route53Zone) UpsertCnames(records []cnameRecord) error {
	return z.changeCnames(route53.ChangeActionUpsert, records)
}

func (z *route53Zone) DeleteCnames(records []cnameRecord) error {
	return z.changeCnames(route53.ChangeActionDelete, records)
}

func (z *route53Zone) changeCnames(action string, records []cnameRecord) error {
	changes := make([]*route53.Change, len(records))
	if action == route53.ChangeActionDelete {
		for _, r := range records {
			changes = append(changes, &route53.Change{
				Action: aws.String(action),
				ResourceRecordSet: &route53.ResourceRecordSet{
					Name: aws.String(r.Hostname),
				},
			})
		}
	} else {
		for _, r := range records {
			changes = append(changes, &route53.Change{
				Action: aws.String(action),
				ResourceRecordSet: &route53.ResourceRecordSet{
					Name:            aws.String(r.Hostname),
					TTL:             aws.Int64(defaultRoute53RecordTTL),
					Type:            aws.String(route53.RRTypeCname),
					ResourceRecords: []*route53.ResourceRecord{{Value: aws.String(r.Target)}},
				},
			})
		}
	}

	resp, err := z.api.ChangeResourceRecordSets(&route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Changes: changes,
			Comment: aws.String("updated by ingress53"),
		},
		HostedZoneId: aws.String(z.ID),
	})
	if err != nil {
		return err
	}

	log.Println("[DEBUG] route53 changes have been submitted, waiting for nameservers to sync")

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

			log.Printf("[DEBUG] route53 changes are still being applied, waiting for %s", defaultRoute53ZoneWaitWatchInterval.String())
		case <-timeout.C:
			return errRoute53WaitWatchTimedOut
		}
	}
}

func (z *route53Zone) setZone(id string) error {
	zone, err := z.api.GetHostedZone(&route53.GetHostedZoneInput{
		Id: aws.String(id),
	})
	if err != nil {
		return err
	}

	z.Name = *zone.HostedZone.Name
	z.ID = *zone.HostedZone.Id
	z.Nameservers = make([]string, len(zone.DelegationSet.NameServers))
	for i, ns := range zone.DelegationSet.NameServers {
		z.Nameservers[i] = *ns
	}

	return nil
}

func (z *route53Zone) Domain() string {
	return z.Name
}

func (z *route53Zone) ListNameservers() []string {
	return z.Nameservers
}
