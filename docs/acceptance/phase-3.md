# Phase 3 acceptance checklist: operations and documentation

Same rules as phase 1. Approved for the owner by the operator on 2026-11-15.

- **AC-3-01 Harbor versions.** The e2e suite passes against the two most recent Harbor minor releases (CI matrix).
  Tests: `test/e2e: TestHarborRunsTheMatrixMinorRelease`
- **AC-3-02 Metrics.** The metrics endpoint exposes, per kind, the count of resources by `Ready` reason, a histogram of Harbor API call latency by operation and result, and the count of registries by upstream health (`healthy`, `unhealthy`, `unknown`). Labels are fixed enums or kind names only (no object names, no namespaces).
  Tests: `internal/harbor: TestHarborRequestsAreObservedByOperationAndResult`, `internal/controller: TestResourceCollectorCountsByKindReasonAndHealth`
- **AC-3-03 Events.** Create, update (drift corrected), recreate, adopt, delete and every error reason emit a Kubernetes event on the resource.
  Tests: `internal/controller: TestProjectStateChangesEmitEvents`, `internal/controller: TestProjectLeftInHarborEmitsAnOrphanedEvent`, `internal/controller: TestProjectErrorReasonEmitsAWarningEvent`, `internal/controller: TestConnectionReadinessEmitsAnEvent`
- **AC-3-04 Harbor down.** With Harbor unreachable, resources report `HarborUnavailable`, the operator doesn't crash or spin (bounded backoff, no more than one Harbor call per resource per backoff step), and everything recovers by itself within one resync after Harbor returns.
  Tests: `internal/controller: TestHarborUnavailableBacksOffAndRecovers`
- **AC-3-05 Watch namespaces.** With the chart's `watchNamespaces` set, the operator ignores resources elsewhere and can't read Secrets or custom resources outside the listed namespaces. The only cluster-wide grants left are reading Namespaces and the metrics endpoint's token and access reviews.
  Tests:
- **AC-3-06 Chart hardening.** The chart supports a NetworkPolicy (egress to the Harbor URL and the API server only), a PodDisruptionBudget, resource values with memory request equal to limit and no CPU limit by default, and a non-root read-only-root-filesystem pod. `make chart-lint` passes.
  Tests: `test/chart: TestDefaultPodIsHardened`, `test/chart: TestNetworkPolicyLimitsEgress`, `test/chart: TestNetworkPolicyNeedsAPIServerCIDRs`, `test/chart: TestPodDisruptionBudget`
- **AC-3-07 Upgrade.** Upgrading the chart from the phase 2 release to the current build with existing resources changes nothing in Harbor and keeps every resource `Ready`.
  Tests:
- **AC-3-08 CI chart example.** `docs/user/` contains a Helm template snippet for a CI chart (namespace, project, push robot, retention policy) that the e2e suite renders and applies successfully.
  Tests:
- **AC-3-09 Troubleshooting docs.** `docs/admin/troubleshooting.md` covers every condition reason with its cause and fix; a generated CRD reference covers every field.
  Tests:
- **AC-3-10 Scraping.** With the chart's options on, it renders a `VMServiceScrape` or a `ServiceMonitor` for the metrics Service, and a ClusterRole allowing `get` on `/metrics` bound to the service accounts given in values. A scraper using one of those service accounts gets the metrics from the secure endpoint; one without gets 403.
  Tests:
- **AC-3-11 Dashboard.** The chart can ship a Grafana dashboard, either as a ConfigMap for Grafana's dashboard sidecar or as a grafana-operator `GrafanaDashboard`. It shows: resources not `Ready` by kind and reason, Harbor API latency and error rate by operation, drift corrections, registry upstream health, reconcile errors and duration, and work queue depth. Every query in it uses only metrics the operator exports.
  Tests:
