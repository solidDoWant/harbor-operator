package harbor_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// requestCounts returns the number of observations of the request histogram by operation and result.
func requestCounts(t *testing.T) map[[2]string]uint64 {
	t.Helper()

	families, err := metrics.Registry.Gather()
	require.NoError(t, err)

	counts := map[[2]string]uint64{}

	for _, family := range families {
		if family.GetName() != "harbor_operator_harbor_requests_seconds" {
			continue
		}

		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}

			assert.Len(t, labels, 2, "only operation and result labels")

			counts[[2]string{labels["operation"], labels["result"]}] = metric.GetHistogram().GetSampleCount()
		}
	}

	return counts
}

func TestHarborRequestsAreObservedByOperationAndResult(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	api, err := harbor.NewClient(harbor.ConnectionConfig{URL: server.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	before := requestCounts(t)

	_, _ = api.GetSystemInfo(t.Context())

	status = http.StatusNotFound
	_, _ = api.GetProject(t.Context(), 7)

	status = http.StatusServiceUnavailable
	_ = api.CheckCredentials(t.Context())

	server.Close()

	_, _ = api.GetRegistry(t.Context(), 3)

	after := requestCounts(t)

	for key, want := range map[[2]string]uint64{
		{"GetSystemInfo", harbor.ResultOK}:                      1,
		{"GetProject", harbor.ResultClientError}:                1,
		{"GetCurrentUserPermissions", harbor.ResultUnavailable}: 1,
		{"GetRegistry", harbor.ResultUnavailable}:               1,
	} {
		assert.Equal(t, before[key]+want, after[key], key)
	}
}

func TestRequestSeriesStayWithinBudget(t *testing.T) {
	series := len(harbor.Operations()) * len(harbor.Results) * (len(harbor.RequestBuckets) + 3)

	assert.LessOrEqual(t, series, 1500)
}
