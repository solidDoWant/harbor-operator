//go:build bughunt

package bughunt

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

// resettingHarbor accepts TCP connections and resets them at once, like a load balancer whose backend is gone.
func resettingHarbor(t *testing.T) (string, *atomic.Int64) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	var accepted atomic.Int64

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			accepted.Add(1)
			// Read the request first so the client sees a reset on read, not on write.
			buffer := make([]byte, 4096)
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, _ = conn.Read(buffer)
			_ = conn.(*net.TCPConn).SetLinger(0)
			_ = conn.Close()
		}
	}()

	return "http://" + listener.Addr().String(), &accepted
}

func TestConnectionProbeOfResettingHarborBacksOff(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	restConfig, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = environment.Stop() })

	runtimeScheme, err := scheme.New()
	require.NoError(t, err)

	manager, err := ctrl.NewManager(restConfig, ctrl.Options{Scheme: runtimeScheme, Logger: logr.Discard(), Metrics: metricsserver.Options{BindAddress: "0"}})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, controller.IndexFields(ctx, manager.GetFieldIndexer()))
	clients := harbor.NewClientCache(manager.GetClient())
	require.NoError(t, (&controller.HarborConnectionReconciler{Client: manager.GetClient(), Clients: clients,
		Recorder: manager.GetEventRecorder("harbor-operator"), Wakeups: controller.NewConnectionWakeups()}).SetupWithManager(manager))

	go func() { _ = manager.Start(ctx) }()

	cl, err := client.New(restConfig, client.Options{Scheme: runtimeScheme})
	require.NoError(t, err)

	url, accepted := resettingHarbor(t)

	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "creds"},
		StringData: map[string]string{"username": "admin", "password": "pw"}}))
	connection := &harborv1alpha1.HarborConnection{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "reset"},
		Spec: harborv1alpha1.HarborConnectionSpec{URL: url, CredentialsSecretRef: harborv1alpha1.LocalSecretReference{Name: "creds"}}}
	require.NoError(t, cl.Create(t.Context(), connection))

	window := 20 * time.Second
	time.Sleep(window)

	var current harborv1alpha1.HarborConnection
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current))
	t.Logf("status: %+v", current.Status.Conditions)

	var events corev1.EventList
	require.NoError(t, cl.List(t.Context(), &events, client.InNamespace("default")))
	t.Logf("%d events in default", len(events.Items))
	t.Logf("%d probes in %s", accepted.Load(), window)

	// Backoff 5s, 10s, 20s: at most a handful of probes in 20 seconds.
	require.LessOrEqual(t, accepted.Load(), int64(6))
}
