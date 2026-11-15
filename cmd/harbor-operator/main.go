// Command harbor-operator runs the controller manager that keeps Harbor projects, robot accounts, policies and
// registries in line with their custom resources.
package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (for example OIDC) so that kubeconfig based runs work.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		secureMetrics        bool
		enableHTTP2          bool
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for the controller manager, so that only one replica is active at a time.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"Serve the metrics endpoint over HTTPS with authentication and authorization. "+
			"Use --metrics-secure=false to serve plain HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "Enable HTTP/2 for the metrics server.")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog := ctrl.Log.WithName("setup")

	runtimeScheme, err := scheme.New()
	if err != nil {
		setupLog.Error(err, "unable to build the scheme")
		os.Exit(1)
	}

	var tlsOpts []func(*tls.Config)
	if !enableHTTP2 {
		// HTTP/2 stays off by default because of the HTTP/2 Stream Cancellation and Rapid Reset CVEs
		// (GHSA-qppj-fm5r-hxr3, GHSA-4374-p667-p6c8).
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		})
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		// Only authenticated and authorized clients may read the metrics.
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	ctx := ctrl.SetupSignalHandler()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        runtimeScheme,
		Metrics:                       metricsServerOptions,
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "harbor-operator.harbor.soliddowant.dev",
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to create the manager")
		os.Exit(1)
	}

	if err := controller.IndexFields(ctx, mgr.GetFieldIndexer()); err != nil {
		setupLog.Error(err, "unable to register field indexes")
		os.Exit(1)
	}

	if err := ownership.SetupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		setupLog.Error(err, "unable to register ownership indexes")
		os.Exit(1)
	}

	clients := harbor.NewClientCache(mgr.GetClient())

	if err := (&controller.HarborConnectionReconciler{Client: mgr.GetClient(), Clients: clients}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up the HarborConnection controller")
		os.Exit(1)
	}

	projects := &controller.HarborProjectReconciler{Client: mgr.GetClient(), Clients: clients, Recorder: mgr.GetEventRecorder("harbor-operator")}
	if err := projects.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up the HarborProject controller")
		os.Exit(1)
	}

	robots := &controller.HarborProjectRobotReconciler{Client: mgr.GetClient(), Clients: clients, Recorder: mgr.GetEventRecorder("harbor-operator")}
	if err := robots.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up the HarborProjectRobot controller")
		os.Exit(1)
	}

	retentions := &controller.HarborRetentionPolicyReconciler{Client: mgr.GetClient(), Clients: clients, Recorder: mgr.GetEventRecorder("harbor-operator")}
	if err := retentions.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up the HarborRetentionPolicy controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up the health check")
		os.Exit(1)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up the ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "version", version)

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running the manager")
		os.Exit(1)
	}
}
