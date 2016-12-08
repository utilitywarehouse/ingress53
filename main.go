package main

import (
	"flag"
	"log"
	"os"
	"os/signal"

	"k8s.io/client-go/1.5/tools/clientcmd"

	"github.com/hashicorp/logutils"
)

var (
	kubeConfig             = flag.String("kubernetes-config", "", "path to the kubeconfig file, if unspecified then in-cluster config will be used")
	lbPublicSelectorString = flag.String("kubernetes-public-ingress-selector", "", "selector for ingresses that are handled by the public ELB")
	lbPublicHostname       = flag.String("elb-hostname-public", "", "hostname of the ELB for public ingresses")
	lbPrivateHostname      = flag.String("elb-hostname-private", "", "hostname of the ELB for private ingresses")
	r53ZoneName            = flag.String("route53-zone-name", "", "Route53 DNS zone name")
	debugLogs              = flag.Bool("debug", false, "enables debug logs")
	dryRun                 = flag.Bool("dry-run", false, "if set, the registrator will not make any Route53 changes")
)

func init() {
	flag.Parse()

	luf := &logutils.LevelFilter{
		Levels:   []logutils.LogLevel{"DEBUG", "INFO", "ERROR"},
		MinLevel: logutils.LogLevel("INFO"),
		Writer:   os.Stdout,
	}

	if *debugLogs {
		luf.MinLevel = logutils.LogLevel("DEBUG")
	}

	log.SetOutput(luf)
}

func main() {
	ro := registratorOptions{
		PrivateHostname:        *lbPrivateHostname,
		PublicHostname:         *lbPublicHostname,
		PublicResourceSelector: *lbPublicSelectorString,
		Route53ZoneName:        *r53ZoneName,
	}

	if *kubeConfig != "" {
		config, err := clientcmd.BuildConfigFromFlags("", *kubeConfig)
		if err != nil {
			log.Printf("[ERROR] could not create kubernetes client: %+v", err)
			os.Exit(1)
		}
		ro.KubernetesConfig = config
	}

	r, err := newRegistratorWithOptions(ro)
	if err != nil {
		log.Printf("[ERROR] could not create registrator: %+v", err)
		os.Exit(1)
	}

	sigChannel := make(chan os.Signal, 1)
	signal.Notify(sigChannel, os.Interrupt)
	go func() {
		<-sigChannel
		log.Println("[INFO] interrupt singal: shutting down ...")
		r.Stop()
	}()

	if err := r.Start(); err != nil {
		log.Printf("[INFO] registrator returned an error: %+v", err)
		os.Exit(1)
	}
}
