//go:build integration

package controller_test

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// harborProxy stands between the operator and the shared Harbor. It can answer 503 to every request, as a Harbor whose
// core is down would, and it records every request it gets. The shared Harbor itself is never stopped.
type harborProxy struct {
	server *httptest.Server
	down   atomic.Bool

	mutex    sync.Mutex
	requests []proxiedRequest
}

type proxiedRequest struct {
	path string
	at   time.Time
}

func newHarborProxy(t *testing.T, target string) *harborProxy {
	t.Helper()

	parsed, err := url.Parse(target)
	require.NoError(t, err)

	proxy := &harborProxy{}
	forward := httputil.NewSingleHostReverseProxy(parsed)
	proxy.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxy.mutex.Lock()
		proxy.requests = append(proxy.requests, proxiedRequest{path: request.URL.Path, at: time.Now()})
		proxy.mutex.Unlock()

		if proxy.down.Load() {
			http.Error(writer, "Harbor is down", http.StatusServiceUnavailable)

			return
		}

		request.Host = parsed.Host
		forward.ServeHTTP(writer, request)
	}))
	t.Cleanup(proxy.server.Close)

	return proxy
}

// since returns the requests received at or after start.
func (p *harborProxy) since(start time.Time) []proxiedRequest {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var found []proxiedRequest

	for _, request := range p.requests {
		if !request.at.Before(start) {
			found = append(found, request)
		}
	}

	return found
}

func TestHarborUnavailableBacksOffAndRecovers(t *testing.T) {
	t.Parallel()

	instance, _, name := projectHarbor(t)
	proxy := newHarborProxy(t, instance.URL)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "unavailable")

	proxied := *instance
	proxied.URL = proxy.server.URL
	connection := readyConnection(t, cl, &proxied, ns, nil)

	// The project resyncs every 2 seconds, so it calls Harbor soon after Harbor goes down.
	project := newProject(ns, name, connection)
	require.NoError(t, cl.Create(t.Context(), project))
	projectKey := client.ObjectKeyFromObject(project)
	awaitProject(t, cl, projectKey, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	downAt := time.Now()

	proxy.down.Store(true)

	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonHarborUnavailable)
	gated := awaitProject(t, cl, projectKey, metav1.ConditionFalse, harborv1alpha1.ReasonHarborUnavailable)

	require.Eventually(t, func() bool {
		require.NoError(t, cl.Get(t.Context(), projectKey, gated))

		return strings.Contains(readyMessage(gated), "is not ready")
	}, projectWait, 200*time.Millisecond, "the project waits for its connection instead of calling Harbor")

	// Spec changes while Harbor is down make the project reconcile again, but not call Harbor.
	start := time.Now()
	window := 16 * time.Second

	for _, description := range []string{"first", "second", "third"} {
		updateProject(t, cl, projectKey, func(changed *harborv1alpha1.HarborProject) {
			changed.Annotations = map[string]string{"test/touch": description}
		})
		time.Sleep(window / 4)
	}

	time.Sleep(window / 4)

	for _, request := range proxy.since(start) {
		assert.Equal(t, "/api/v2.0/systeminfo", request.path, "only the connection probes Harbor while it is down")
	}

	// Going down costs a few immediate calls (the project's failing call and the probes its status changes trigger),
	// then the probe backs off from 5 seconds: 5, 10, 20 seconds and so on. Retrying from milliseconds instead would
	// make more than a dozen calls in the same time.
	elapsed := time.Since(downAt)
	all := proxy.since(downAt)
	t.Logf("%d calls in the %s since Harbor went down", len(all), elapsed)
	assert.LessOrEqual(t, len(all), 5+int(elapsed/(5*time.Second)), "calls in the %s since Harbor went down: %v", elapsed, all)

	proxy.down.Store(false)

	// The connection is probed again within one backoff step (at most 5 minutes), and the project follows it.
	require.Eventually(t, func() bool {
		var current harborv1alpha1.HarborConnection
		require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current))
		status, reason := readyReason(&current)

		return status == metav1.ConditionTrue && reason == harborv1alpha1.ReasonReady
	}, 6*time.Minute, time.Second, "the connection becomes ready once Harbor is back")
	awaitProject(t, cl, projectKey, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
}
