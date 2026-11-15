# Phase 3 design: operations and documentation

Builds on phases 1 and 2. No new kinds and no new Harbor behaviour. Each section maps to scenarios in `docs/acceptance/phase-3.md`.

## 1. Harbor version matrix

- The CI `e2e` job becomes a matrix over the two most recent Harbor minor releases (chart versions pinned in `test/harbor/versions.yaml`; Renovate updates them). The `checks` aggregator needs the whole matrix.
- A test that needs a feature only one version has skips with the version in the message. Nothing in the operator branches on the Harbor version unless a test proves it necessary; if one does, the branch lives in `internal/harbor` only.

## 2. Metrics

Registered on controller-runtime's metrics registry:

| Metric | Type | Labels |
|---|---|---|
| `harbor_operator_resources` | gauge | `kind`, `reason` (`Ready` or the condition reason) |
| `harbor_operator_harbor_requests_seconds` | histogram | `operation` (fixed enum: one value per `internal/harbor` method), `result` (`ok` or the mapped reason) |
| `harbor_operator_drift_corrections_total` | counter | `kind` |
| `harbor_operator_registry_health` | gauge | `health` (`healthy`, `unhealthy`, `unknown`): the number of `HarborRegistry` resources with that `status.health` |

No label carries an object name, namespace, Harbor URL or project name. A unit test lists every series the operator can export with two kinds' worth of resources and checks the count stays under 200.

## 3. Events

One helper, `internal/controller/events.go`, emits `Normal` events `Created`, `Updated` (drift corrected, with the field names, never values), `Recreated`, `Adopted`, `Deleted`, `Orphaned`, and `Warning` events with the condition reason for every error. It is called from the same place status is written, so the two never disagree. Rate: at most one identical event per resource per resync (client-go's event correlator already aggregates).

## 4. Harbor unreachable

- `HarborUnavailable` requeues with exponential backoff from 5 s to 5 min (controller-runtime rate limiter, per item), and the connection reconciler probes `/systeminfo` on the same backoff.
- While the connection is `HarborUnavailable`, project-level reconcilers return early with `HarborUnavailable` and **no Harbor call**; the connection becoming `Ready` re-queues all dependents (the existing index). This bounds calls to one probe per connection per backoff step.
- Test: integration test that stops Harbor's core (scale to 0), counts requests through a recording `http.RoundTripper`, then restores it and waits for `Ready`.

## 5. watchNamespaces

- Chart value `watchNamespaces: []`. Empty: cluster-wide cache, ClusterRole. Non-empty: the manager's cache is limited to those namespaces (`cache.Options.DefaultNamespaces`), and the chart renders a Role and RoleBinding in each instead of the ClusterRole, plus a small ClusterRole for `namespaces` get/list/watch only (label selectors need namespace labels).
- Resources outside the list are invisible to the operator (not reported); documented.

## 6. Chart hardening

- `networkPolicy.enabled` (default false): egress to the API server (`kubernetes.default` service CIDR or the endpoints, value-supplied CIDRs) and to `networkPolicy.harborEgress` (pod selector + namespace selector, or CIDRs); DNS egress to kube-dns. Ingress only to the metrics and health ports.
- `podDisruptionBudget.enabled` (default false, `maxUnavailable: 1`).
- Default resources: memory request 128Mi = limit, CPU request 10m, no CPU limit.
- Pod: `runAsNonRoot`, `readOnlyRootFilesystem`, drop all capabilities, seccomp `RuntimeDefault`.

## 7. Upgrade test

An e2e test installs the previous release's chart from `oci://ghcr.io/soliddowant/charts/harbor-operator` (version from `test/upgrade/previous-version`), creates one of every kind, upgrades to the local chart and image, and checks: every resource stays `Ready`, `harbor_operator_drift_corrections_total` stays 0, and Harbor objects' update times don't change.

## 8. CI chart example

`docs/user/ci-chart-example.md` embeds a Helm template from `docs/user/examples/ci-chart/` (namespace, `HarborProject`, push `HarborProjectRobot`, `HarborRetentionPolicy`). The e2e test runs `helm template` on that directory and applies the output. The doc includes the files with a generated block, so `make docs-check` fails if they drift.

## 9. Troubleshooting and reference

- `docs/admin/troubleshooting.md`: one entry per condition reason constant in `api/v1alpha1` (cause, what to check, fix). A unit test lists the constants and fails if one has no entry.
- `docs/admin/crd-reference.md` generated from the API types' doc comments (`crd-ref-docs` or equivalent maintained tool), checked by `make docs-check`.

## 10. Scraping

- `metrics.vmServiceScrape.enabled` renders an `operator.victoriametrics.com/v1beta1` `VMServiceScrape`; `metrics.serviceMonitor.enabled` renders a `monitoring.coreos.com/v1` `ServiceMonitor`. Both default to false, and both target the metrics Service over HTTPS with the scraper's service-account bearer token. The serving certificate is self-signed by controller-runtime, so both set `insecureSkipVerify: true` by default, with a value to pass a CA instead.
- `metrics.reader.enabled` renders a ClusterRole with `get` on the non-resource URL `/metrics`, and a ClusterRoleBinding to the service accounts listed in `metrics.reader.subjects` (namespace and name). This is what the secure endpoint's SubjectAccessReview checks.
- Test: an e2e test renders the chart with the reader on, then scrapes the endpoint with a token from a listed service account (200, includes `harbor_operator_resources`) and with one that isn't listed (403). Rendering the scrape objects is checked by `make chart-lint` with those values on; the CRDs for them are not installed in kind.

## 11. Dashboard

- The dashboard JSON lives at `deploy/charts/harbor-operator/dashboards/harbor-operator.json` and is written by hand once, then kept in git. It uses a datasource variable (`$datasource`, type prometheus), so it works with VictoriaMetrics or Prometheus, and a `$namespace` variable for the operator's namespace.
- `grafanaDashboard.configMap.enabled` renders a ConfigMap with the JSON and the label from `grafanaDashboard.configMap.labels` (default `grafana_dashboard: "1"`). `grafanaDashboard.grafanaOperator.enabled` renders a `grafana.integreatly.org/v1beta1` `GrafanaDashboard` with the JSON inline and `instanceSelector` from values. Both default to false.
- Panels: resources not `Ready` by kind and reason (`harbor_operator_resources`), Harbor API p50 and p95 latency and non-`ok` rate by operation (`harbor_operator_harbor_requests_seconds`), drift corrections per kind, registries by health, reconcile errors and p95 duration per controller (`controller_runtime_reconcile_*`), work queue depth (`workqueue_depth`), and the operator pod's memory and restarts from standard kubelet metrics.
- Test: a unit test parses the JSON, extracts the metric names from every query, and fails if one isn't exported by the operator (the series-budget test's list plus the controller-runtime and kubelet metrics it names) or if the JSON doesn't parse. `make chart-lint` renders both dashboard options.
