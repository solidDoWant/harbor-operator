# Phase 3 acceptance checklist: operations and documentation

Same rules as phase 1. Approved for the owner by the operator on 2026-11-15.

- **AC-3-01 Harbor versions.** The e2e suite passes against the two most recent Harbor minor releases (CI matrix).
  Tests:
- **AC-3-02 Metrics.** The metrics endpoint exposes, per kind, the count of resources by `Ready` reason and a histogram of Harbor API call latency by operation and result. Labels are fixed enums or kind names only (no object names, no namespaces).
  Tests:
- **AC-3-03 Events.** Create, update (drift corrected), recreate, adopt, delete and every error reason emit a Kubernetes event on the resource.
  Tests:
- **AC-3-04 Harbor down.** With Harbor unreachable, resources report `HarborUnavailable`, the operator doesn't crash or spin (bounded backoff, no more than one Harbor call per resource per backoff step), and everything recovers by itself within one resync after Harbor returns.
  Tests:
- **AC-3-05 Watch namespaces.** With the chart's `watchNamespaces` set, the operator ignores resources elsewhere and runs with namespaced RBAC only.
  Tests:
- **AC-3-06 Chart hardening.** The chart supports a NetworkPolicy (egress to the Harbor URL and the API server only), a PodDisruptionBudget, resource values with memory request equal to limit and no CPU limit by default, and a non-root read-only-root-filesystem pod. `make chart-lint` passes.
  Tests:
- **AC-3-07 Upgrade.** Upgrading the chart from the phase 2 release to the current build with existing resources changes nothing in Harbor and keeps every resource `Ready`.
  Tests:
- **AC-3-08 CI chart example.** `docs/user/` contains a Helm template snippet for a CI chart (namespace, project, push robot, retention policy) that the e2e suite renders and applies successfully.
  Tests:
- **AC-3-09 Troubleshooting docs.** `docs/admin/troubleshooting.md` covers every condition reason with its cause and fix; a generated CRD reference covers every field.
  Tests:
