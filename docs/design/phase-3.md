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

## 12. Amendments after the design review

The design review on issue #108 found gaps. This section answers them and overrides sections 2–11 where they disagree.

### 12.1 watchNamespaces (C1, H1, M5)

- A reference (`connectionRef`, `projectRef`, `registryRef`) whose target namespace isn't in `watchNamespaces` counts as **not admitted**: `Ready=False, NotAllowed`, message "namespace X is not watched by the operator", no Harbor call. Check membership before the `Get`; never rely on the multi-namespace cache's error. On deletion it is orphaned, like any reference that isn't admitted (SPEC section 7). The connection's own namespace must be in the list; the admin docs say so.
- The operator assumes it is the **only install per Harbor**, and that every resource that can claim objects in that Harbor is in a watched namespace. Resources in a namespace must be deleted before the namespace is removed from `watchNamespaces`. The admin docs state both. Not enforced in code.
- Cluster-wide grants that stay with `watchNamespaces` set: `namespaces` get, list and watch; `tokenreviews` and `subjectaccessreviews` create. Leases and events always get a Role in the release namespace. The per-namespace Roles are generated from `config/rbac/role.yaml` by `make generate`, the same way `clusterrole.yaml` is.
- Test for AC-3-05: an e2e install with `watchNamespaces: [a]`. `SubjectAccessReview`s show the operator's service account can't get or list `secrets` or `harborprojects` in namespace `b`. A resource in `b` gets no status and no Harbor object, checked after a resource in `a` that was created later is `Ready`.

### 12.2 Harbor unreachable (H2, M1, M2)

- The gate is `connectionNotReady` on the connection's `Ready` condition, for **non-terminating connections only**. Deletion paths keep `connectionNotReadyForDeletion` unchanged: through a terminating connection, each dependent calls Harbor at most once per step of its own backoff, and a terminating connection doesn't probe. No gate or circuit breaker lives in `internal/harbor` or the client cache.
- All seven controllers use one rate limiter: per item, exponential from 5 s to 5 min, plus the existing bucket. `MaxBackoff` fields stay only as test hooks.
- The bound AC-3-04 tests: at most one Harbor call per resource per backoff step, and, once the connection is `HarborUnavailable`, no calls from its non-deleting dependents. A dependent that maps an error to `HarborUnavailable` enqueues its connection (a `source.Channel` GenericEvent), so the connection flips promptly.
- Extra reconcile row: connection terminating, Harbor down, dependent deleted. The dependent shows `HarborUnavailable` and retries on backoff. When Harbor returns, the dependent finishes within one maximum backoff step, then the connection's finalizer goes.
- Test: the test's own `HarborConnection` points at a `net/http/httputil.ReverseProxy` (standard library) in front of the shared Harbor. The test switches the proxy to answer 503 or drop connections, and counts requests there. Never scale the shared Harbor down.

### 12.3 Metrics (H3, M3, M6)

- `harbor_operator_harbor_requests_seconds`: one observation per **HTTP request**, recorded by a `RoundTripper` that takes the operation name from the request context. `result` is `ok`, `client_error` (4xx) or `unavailable` (network or 5xx). Buckets: `.05 .1 .25 .5 1 2.5 5 10`.
- The budget test computes label sets per metric from the enums (kinds × reasons, operations × results, health values) and asserts the total bucket series stay at or under 1,500. It doesn't count series seen in one run.
- `harbor_operator_resources` and `harbor_operator_registry_health` are a `prometheus.Collector` that lists from the cache on each scrape, and exports only after `mgr.Elected()` is closed, so a standby replica exports nothing. A resource with no `Ready` condition has `reason="Unknown"`.
- A drift correction is an update to a Harbor object while `generation == status.observedGeneration`. Spec changes don't count.

### 12.4 Events (M4)

- Warning events only when the `Ready` condition's `(status, reason)` changes, with the condition message verbatim (already redacted by `wrapError`). Normal events for actions: `Created`, `Updated`, `Recreated`, `Replaced`, `Adopted`, `HealthChanged`, `Deleted`, `Orphaned`.
- `HarborConnectionReconciler` gets an event recorder too.
- The secret scan in `TestNoSecretAppearsInStatusEventsOrLogs…` extends to the connection and registry credential paths.

### 12.5 Upgrade test (M6)

After the rollout, wait until `controller_runtime_reconcile_total` for each controller is at least its number of resources, then assert `harbor_operator_drift_corrections_total` is 0. Compare robot IDs and the robot Secrets' data, and the full Harbor objects for policies, not only update times.

### 12.6 Dashboard and scraping (H4, L1, L2)

- Memory and restart panels use the operator's own process metrics: `process_resident_memory_bytes`, and `changes(process_start_time_seconds[$__range])`. No kubelet or kube-state-metrics metrics; the test allows none.
- The test parses queries with `github.com/VictoriaMetrics/metricsql` and strips `_bucket`, `_sum` and `_count` to get base names.
- The chart includes the JSON with `.Files.Get`, never `tpl`, because Grafana's `{{label}}` legends would break Helm.
- The chart fails rendering when `metrics.reader`, `vmServiceScrape` or `serviceMonitor` is enabled with `metrics.secure: false`. Both scrape objects use `bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token`. The docs say the scraper's service account must be listed in `metrics.reader.subjects`.

### 12.7 Smaller rules (L3–L6)

- NetworkPolicy: the chart fails when `networkPolicy.enabled` is true and no API-server CIDRs are given. DNS egress to kube-dns is part of the policy (a hostname URL needs it). AC-3-06 is tested with `helm template` assertions on the pod security context, resources, NetworkPolicy and PDB.
- Troubleshooting test: covers the condition `Reason*` constants in `api/v1alpha1/common_types.go` (read with `go/parser`), not the `EventReason*` ones.
- CI chart example: the e2e test admits the example's namespace through a label that its connection's `allowedNamespaces.selector` matches. The project name is a template value, so runs on the shared Harbor don't collide.
- Version matrix: one entry per minor in `test/harbor/versions.yaml`, with a Renovate `allowedVersions` per entry. When a new minor ships, a Renovate major-style PR moves the pair. Whoever merges it checks both entries still differ.
