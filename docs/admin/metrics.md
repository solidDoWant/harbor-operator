# Metrics

The operator serves Prometheus metrics on the metrics port (see [chart values](chart-values.md)). Only the leader replica exports the resource metrics. A standby replica exports none of them.

No label holds an object name, a namespace, a Harbor URL or a project name. Labels are kind names or fixed values.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `harbor_operator_resources` | gauge | `kind`, `reason` | Number of resources of the kind whose `Ready` condition has the reason. A resource with no `Ready` condition has the reason `Unknown`. |
| `harbor_operator_harbor_requests_seconds` | histogram | `operation`, `result` | Latency of each HTTP request to Harbor. `result` is `ok`, `client_error` (HTTP 4xx) or `unavailable` (network failure or HTTP 5xx). Requests that match no known operation use the operation `other`. |
| `harbor_operator_registry_health` | gauge | `health` | Number of `HarborRegistry` resources with that `status.health`: `healthy`, `unhealthy` or `unknown`. |

The operator also exports the standard controller-runtime metrics, such as `controller_runtime_reconcile_total`.

## Dashboard

The chart ships a Grafana dashboard. It is off by default. Turn on one of two options:

- `grafanaDashboard.configMap.enabled` renders a ConfigMap for Grafana's dashboard sidecar. The labels come from `grafanaDashboard.configMap.labels`. The default is `grafana_dashboard: "1"`.
- `grafanaDashboard.grafanaOperator.enabled` renders a grafana-operator `GrafanaDashboard`. Set `grafanaDashboard.grafanaOperator.instanceSelector` to select your Grafana instances.

The dashboard has a `datasource` variable, so it works with Prometheus or VictoriaMetrics. Its `namespace` variable is the namespace where the operator runs. Your scraper must collect the operator's metrics with a `namespace` label.

The panels show resources that are not `Ready` by kind and reason, Harbor API latency and errors by operation, drift corrections, registries by health, reconcile errors and duration, work queue depth, and the operator's memory and restarts.
