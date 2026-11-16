//go:build integration

package controller_test

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// TestConnectionProbeBacksOffWhenHarborResetsConnections uses a listener that resets every TCP connection, so the
// client error text holds a new source port on each probe.
func TestConnectionProbeBacksOffWhenHarborResetsConnections(t *testing.T) {
	t.Parallel()

	cl := startOperator(t)

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
			// Read the request first so the client sees the reset on read.
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, _ = conn.Read(make([]byte, 4096))
			_ = conn.(*net.TCPConn).SetLinger(0)
			_ = conn.Close()
		}
	}()

	namespace := newNamespace(t, cl, "reset")
	require.NoError(t, cl.Create(t.Context(), credentials(namespace, "harbor-creds", "someone", "pw")))

	connection := newConnection(namespace, "harbor", "http://"+listener.Addr().String(), "harbor-creds")
	require.NoError(t, cl.Create(t.Context(), connection))

	time.Sleep(12 * time.Second)

	// Backoff steps of 5 s and 10 s allow about three probes in 12 s; a hot loop makes thousands.
	assert.LessOrEqual(t, accepted.Load(), int64(8))

	var events corev1.EventList
	require.NoError(t, cl.List(t.Context(), &events, client.InNamespace(namespace)))

	warnings := 0

	for _, event := range events.Items {
		if event.Type == corev1.EventTypeWarning && event.InvolvedObject.Name == connection.Name {
			warnings++
		}
	}

	assert.LessOrEqual(t, warnings, 1)

	var current harborv1alpha1.HarborConnection
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current))
}
