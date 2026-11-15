package harbor

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Results of a Harbor request, as the result label of the request histogram.
const (
	ResultOK          = "ok"
	ResultClientError = "client_error"
	ResultUnavailable = "unavailable"
)

// Results lists every value of the result label.
var Results = []string{ResultOK, ResultClientError, ResultUnavailable}

// operationRoutes maps a method and a path pattern to the operation label. A star matches one path segment.
var operationRoutes = []struct{ method, pattern, operation string }{
	{http.MethodGet, "/users/current/permissions", "GetCurrentUserPermissions"},
	{http.MethodPost, "/projects", "CreateProject"},
	{http.MethodGet, "/projects", "ListProjects"},
	{http.MethodGet, "/projects/*", "GetProject"},
	{http.MethodPut, "/projects/*", "UpdateProject"},
	{http.MethodDelete, "/projects/*", "DeleteProject"},
	{http.MethodGet, "/projects/*/immutabletagrules", "ListImmutabilityRules"},
	{http.MethodPost, "/projects/*/immutabletagrules", "CreateImmutabilityRule"},
	{http.MethodPut, "/projects/*/immutabletagrules/*", "UpdateImmutabilityRule"},
	{http.MethodDelete, "/projects/*/immutabletagrules/*", "DeleteImmutabilityRule"},
	{http.MethodGet, "/quotas", "ListQuotas"},
	{http.MethodPut, "/quotas/*", "UpdateQuota"},
	{http.MethodPost, "/registries", "CreateRegistry"},
	{http.MethodGet, "/registries", "ListRegistries"},
	{http.MethodPost, "/registries/ping", "PingRegistry"},
	{http.MethodGet, "/registries/*", "GetRegistry"},
	{http.MethodPut, "/registries/*", "UpdateRegistry"},
	{http.MethodDelete, "/registries/*", "DeleteRegistry"},
	{http.MethodPost, "/retentions", "CreateRetention"},
	{http.MethodGet, "/retentions/*", "GetRetention"},
	{http.MethodPut, "/retentions/*", "UpdateRetention"},
	{http.MethodGet, "/retentions/*/executions", "ListRetentionExecutions"},
	{http.MethodPost, "/retentions/*/executions", "TriggerRetentionExecution"},
	{http.MethodPost, "/robots", "CreateRobot"},
	{http.MethodGet, "/robots", "ListRobots"},
	{http.MethodGet, "/robots/*", "GetRobot"},
	{http.MethodPut, "/robots/*", "UpdateRobot"},
	{http.MethodPatch, "/robots/*", "RefreshRobotSecret"},
	{http.MethodDelete, "/robots/*", "DeleteRobot"},
	{http.MethodGet, "/systeminfo", "GetSystemInfo"},
}

// Operations lists every value of the operation label.
func Operations() []string {
	seen := map[string]bool{OperationOther: true}
	names := []string{OperationOther}

	for _, route := range operationRoutes {
		if !seen[route.operation] {
			seen[route.operation] = true
			names = append(names, route.operation)
		}
	}

	return names
}

// OperationOther is the operation label for a request that carries no known operation.
const OperationOther = "other"

// RequestBuckets are the histogram buckets, in seconds.
var RequestBuckets = []float64{.05, .1, .25, .5, 1, 2.5, 5, 10}

var requestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "harbor_operator_harbor_requests_seconds",
	Help:    "Latency of HTTP requests to Harbor by operation and result.",
	Buckets: RequestBuckets,
}, []string{"operation", "result"})

func init() {
	metrics.Registry.MustRegister(requestSeconds)
}

// meteredTransport records one latency observation per HTTP request.
type meteredTransport struct {
	next http.RoundTripper
}

func (t meteredTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	start := time.Now()
	response, err := t.next.RoundTrip(request)

	requestSeconds.WithLabelValues(operationLabel(request), resultLabel(response, err)).Observe(time.Since(start).Seconds())

	return response, err
}

func operationLabel(request *http.Request) string {
	segments := strings.Split(strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v2.0"), "/"), "/")

	for _, route := range operationRoutes {
		if route.method != request.Method {
			continue
		}

		want := strings.Split(strings.Trim(route.pattern, "/"), "/")
		if len(want) != len(segments) {
			continue
		}

		matches := true

		for index := range want {
			if want[index] != "*" && want[index] != segments[index] {
				matches = false

				break
			}
		}

		if matches {
			return route.operation
		}
	}

	return OperationOther
}

func resultLabel(response *http.Response, err error) string {
	switch {
	case err != nil:
		return ResultUnavailable
	case response.StatusCode >= http.StatusInternalServerError:
		return ResultUnavailable
	case response.StatusCode >= http.StatusBadRequest:
		return ResultClientError
	default:
		return ResultOK
	}
}
