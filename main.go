package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"

	"k8s.io/client-go/1.5/tools/clientcmd"

	"github.com/hashicorp/logutils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/utilitywarehouse/go-operational/op"
)

var (
	appGitHash = "master"

	kubeConfig           = flag.String("kubernetes-config", "", "path to the kubeconfig file, if unspecified then in-cluster config will be used")
	publicSelectorString = flag.String("public-ingress-selector", "", "selector for ingresses that should point to the public target")
	targetPublic         = flag.String("target-public", "", "target hostname for public ingresses")
	targetPrivate        = flag.String("target-private", "", "target hostnam for private ingresses")
	r53ZoneID            = flag.String("route53-zone-id", "", "route53 hosted DNS zone id")
	debugLogs            = flag.Bool("debug", false, "enables debug logs")
	dryRun               = flag.Bool("dry-run", false, "if set, ingress53 will not make any Route53 changes")

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

	prometheus.MustRegister(metricUpdatesApplied)
	prometheus.MustRegister(metricUpdatesReceived)
	prometheus.MustRegister(metricUpdatesRejected)
}

func main() {
	ro := registratorOptions{
		PrivateHostname:        *targetPrivate,
		PublicHostname:         *targetPublic,
		PublicResourceSelector: *publicSelectorString,
		Route53ZoneID:          *r53ZoneID,
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
