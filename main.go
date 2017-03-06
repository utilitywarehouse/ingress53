package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"k8s.io/client-go/1.5/tools/clientcmd"

	"github.com/hashicorp/logutils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/utilitywarehouse/go-operational/op"
)

// Define a type named "strslice" as a slice of strings
type strslice []string

// Now, for our new type, implement the two methods of
// the flag.Value interface...
// The first method is String() string
func (s *strslice) String() string {
	return fmt.Sprint(*s)
}

// Set is the method to set the flag value, part of the flag.Value interface.
// Set's argument is a string to be parsed to set the flag.
// It's a comma-separated list, so we split it.
func (s *strslice) Set(value string) error {
	for _, target := range strings.Split(value, ",") {
		*s = append(*s, target)
	}
	return nil
}

var (
	appGitHash = "master"

	// Define a flag to accumulate durations. Because it has a special type,
	// we need to use the Var function and therefore create the flag during
	// init.
	targets strslice

	kubeConfig      = flag.String("kubernetes-config", "", "path to the kubeconfig file, if unspecified then in-cluster config will be used")
	targetLabelName = flag.String("target-label", "ingress53.target", "Kubernetes key of the label that specifies the target type")
	ingoreLabelName = flag.String("ignore-label", "ingress53.ignore", "Kubernetes key of the label that determines whether an ingress should be ignored")
	defaultTarget   = flag.String("default-target", "", "Default target to use in the absense of matching labels")
	r53ZoneID       = flag.String("route53-zone-id", "", "route53 hosted DNS zone id")
	debugLogs       = flag.Bool("debug", false, "enables debug logs")
	dryRun          = flag.Bool("dry-run", false, "if set, ingress53 will not make any Route53 changes")

	metricUpdatesApplied = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "ingress53",

			Subsystem: "route53",
			Name:      "updates_applied",
			Help:      "number of route53 updates",
		},
		[]string{"hostname", "action"},
	)

	metricUpdatesReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "ingress53",
			Subsystem: "kubernetes",
			Name:      "updates_received",
			Help:      "number of route53 updates",
		},
		[]string{"ingress", "action"},
	)

	metricUpdatesRejected = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "ingress53",
			Subsystem: "kubernetes",
			Name:      "updates_rejected",
			Help:      "number of route53 updates rejected",
		},
	)
)

func main() {
	prometheus.MustRegister(metricUpdatesApplied)
	prometheus.MustRegister(metricUpdatesReceived)
	prometheus.MustRegister(metricUpdatesRejected)

	flag.Var(&targets, "target", "List of endpoints (ELB) targets to map ingress records to")
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

	ro := registratorOptions{
		Targets:         targets,
		TargetLabelName: *targetLabelName,
		IgnoreLabelName: *ingoreLabelName,
		DefaultTarget:   *defaultTarget,
		Route53ZoneID:   *r53ZoneID,
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

	go func() {
		log.Printf("[INFO] starting HTTP endpoints ...")

		mux := http.NewServeMux()
		mux.Handle("/__/", op.NewHandler(
			op.NewStatus("ingress53", "ingress53 updates Route53 DNS records based on the ingresses available on the kubernetes cluster it runs on").
				AddOwner("infrastructure", "#infra").
				AddLink("github", "https://github.com/utilitywarehouse/ingress53").
				SetRevision(appGitHash).
				AddChecker("running", func(cr *op.CheckResponse) { cr.Healthy("service is running") }).
				ReadyAlways(),
		))
		mux.Handle("/metrics", promhttp.Handler())

		if err := http.ListenAndServe(":5000", mux); err != nil {
			log.Printf("[ERROR] could not start HTTP router: %+v", err)
			os.Exit(1)
		}
	}()

	if err := r.Start(); err != nil {
		log.Printf("[ERROR] registrator returned an error: %+v", err)
		os.Exit(1)
	}
}
