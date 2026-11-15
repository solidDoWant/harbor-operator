//go:build integration

package controller_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

var (
	// sharedClient talks to the API server of the operator that TestMain started; nil when envtest is unavailable.
	sharedClient   client.Client
	sharedStartErr error
)

// TestMain starts one API server and one operator for every test in the package. Each test works in its own
// namespaces and under its own Harbor names, so tests do not see each other's resources.
func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	environment, restConfig, err := startEnvironment()
	if err != nil {
		sharedStartErr = err

		return m.Run()
	}

	defer func() { _ = environment.Stop() }()

	stop, direct, err := runOperator(restConfig, operatorOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting the operator: %v\n", err)

		return 1
	}

	defer stop()

	sharedClient = direct

	return m.Run()
}

func startEnvironment() (*envtest.Environment, *rest.Config, error) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}

	restConfig, err := environment.Start()
	if err != nil {
		return nil, nil, err
	}

	return environment, restConfig, nil
}

// runOperator runs every controller in-process against restConfig. It returns a function that stops the operator and a
// direct (uncached) client.
func runOperator(restConfig *rest.Config, options operatorOptions) (func(), client.Client, error) {
	runtimeScheme, err := scheme.New()
	if err != nil {
		return nil, nil, err
	}

	// Without a logger controller-runtime captures a stack trace on every log call, which slows reconciles badly
	// under parallel load.
	if options.logger.GetSink() == nil {
		options.logger = logr.Discard()
	}

	ctx, cancel := context.WithCancel(context.Background())

	manager, err := ctrl.NewManager(restConfig, ctrl.Options{Scheme: runtimeScheme, Logger: options.logger, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: ptr(true)}})
	if err != nil {
		cancel()

		return nil, nil, err
	}

	robotClient := manager.GetClient()
	if options.wrapRobotClient != nil {
		robotClient = options.wrapRobotClient(robotClient)
	}

	recorder := manager.GetEventRecorder("harbor-operator")
	clients := harbor.NewClientCache(manager.GetClient())
	wakeups := controller.NewConnectionWakeups()

	setup := []error{
		controller.IndexFields(ctx, manager.GetFieldIndexer()),
		ownership.SetupIndexes(ctx, manager.GetFieldIndexer()),
		(&controller.HarborConnectionReconciler{Client: manager.GetClient(), Clients: clients, Recorder: recorder, Wakeups: wakeups}).SetupWithManager(manager),
		(&controller.HarborProjectReconciler{Client: manager.GetClient(), Clients: clients, Recorder: recorder, Wakeups: wakeups, MaxBackoff: 2 * time.Second}).SetupWithManager(manager),
		(&controller.HarborProjectRobotReconciler{Client: robotClient, Clients: clients, Recorder: recorder, Wakeups: wakeups}).SetupWithManager(manager),
		(&controller.HarborSystemRobotReconciler{Client: manager.GetClient(), Clients: clients, Recorder: recorder, Wakeups: wakeups}).SetupWithManager(manager),
		(&controller.HarborRetentionPolicyReconciler{Client: manager.GetClient(), Clients: clients, Recorder: recorder, Wakeups: wakeups}).SetupWithManager(manager),
		(&controller.HarborRegistryReconciler{Client: manager.GetClient(), Clients: clients, Recorder: recorder, Wakeups: wakeups, MaxBackoff: 2 * time.Second}).SetupWithManager(manager),
		(&controller.HarborImmutabilityPolicyReconciler{Client: manager.GetClient(), Clients: clients, Recorder: recorder, Wakeups: wakeups}).SetupWithManager(manager),
	}

	for _, err := range setup {
		if err != nil {
			cancel()

			return nil, nil, err
		}
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = manager.Start(ctx)
	}()

	direct, err := client.New(restConfig, client.Options{Scheme: runtimeScheme})
	if err != nil {
		cancel()
		<-done

		return nil, nil, err
	}

	return func() { cancel(); <-done }, direct, nil
}
