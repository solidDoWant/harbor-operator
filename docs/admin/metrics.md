# Metrics

The operator serves Prometheus metrics on the metrics port (see [chart values](chart-values.md)). Only the leader replica exports the resource metrics. A standby replica exports none of them.

No label holds an object name, a namespace, a Harbor URL or a project name. Labels are kind names or fixed values.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `harbor_operator_resources` | gauge | `kind`, `reason` | Number of resources of the kind whose `Ready` condition has the reason. A resource with no `Ready` condition has the reason `Unknown`. |
| `harbor_operator_harbor_requests_seconds` | histogram | `operation`, `result` | Latency of each HTTP request to Harbor. `result` is `ok`, `client_error` (HTTP 4xx) or `unavailable` (network failure or HTTP 5xx). Requests that match no known operation use the operation `other`. |
| `harbor_operator_registry_health` | gauge | `health` | Number of `HarborRegistry` resources with that `status.health`: `healthy`, `unhealthy` or `unknown`. |

The operator also exports the standard controller-runtime metrics, such as `controller_runtime_reconcile_total`.

## Scraping

The metrics endpoint uses HTTPS and checks each request with the Kubernetes API. A scraper must send a service-account token, and its service account needs the right to `get` the `/metrics` path.

To give that right, turn on `metrics.reader.enabled` and list the scraper's service accounts in `metrics.reader.subjects`. Each item has a `namespace` and a `name`. The chart then makes a ClusterRole that allows `get` on `/metrics`, and binds it to those service accounts. A service account that is not listed gets HTTP 403.

The chart can also make the scrape object for you. Both objects scrape the metrics Service over HTTPS with the scraper's own service-account token:

- `metrics.vmServiceScrape.enabled` makes a VictoriaMetrics `VMServiceScrape`.
- `metrics.serviceMonitor.enabled` makes a Prometheus Operator `ServiceMonitor`.

The operator makes its own serving certificate, so the objects skip certificate checks by default. To check it, set `caFile` on the object to a CA bundle file inside the scraper pod.

All three options need `metrics.secure` to be `true`. The chart fails to render if it is not. The scrape objects need their CRDs in the cluster. The chart does not install them.
