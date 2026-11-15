# Phase 2 acceptance checklist: policies, proxy caches and system robots

Same rules as phase 1. Approved for the owner by the operator on 2026-11-15.

## Registries and proxy caches

- **AC-2-01 Registry.** A `HarborRegistry` of type `github-ghcr` (and one of type `docker-registry` pointing at a registry inside the test cluster) creates a Harbor registry endpoint, reports Harbor's health check result, and becomes `Ready`.
  Tests: `internal/controller: TestRegistryIsCreatedReportsItsHealthAndBecomesReady`
- **AC-2-02 Registry credentials.** A registry with a credentials Secret stores them in Harbor; changing the Secret updates Harbor. The credential never appears in status, events or logs.
  Tests: `internal/controller: TestRegistryCredentialsAreStoredAndFollowTheSecret`, `internal/controller: TestRegistryWithMissingOrIncompleteCredentialsWritesNothing`, `internal/harbor: TestRegistryErrorsNeverContainTheAccessSecret`
- **AC-2-03 Registry drift and delete.** URL, type-specific settings and `insecure` changed in Harbor are put back. Deleting the resource deletes the endpoint (or keeps it with `Orphan`), and waits with `DependentsExist` while a proxy-cache project uses it.
  Tests: `internal/controller: TestRegistryDriftInHarborIsPutBack`, `internal/controller: TestRegistryDeletion`
- **AC-2-04 Proxy-cache project.** A `HarborProject` with `proxyCache.registryRef` creates a proxy-cache project. Pulling `<harbor>/<project>/<path>:<tag>` through Harbor returns the upstream image (from the in-cluster upstream registry in tests).
  Tests: `internal/controller: TestProxyCacheProjectPullsTheUpstreamImage`, `internal/controller: TestProxyCacheProjectWaitsForItsRegistry`
- **AC-2-05 Registry access.** A project in a namespace the registry's `allowedNamespaces` doesn't admit reports `NotAllowed` and nothing is created.
  Tests: `internal/controller: TestProxyCacheProjectInANamespaceTheRegistryDoesNotAdmit`
- **AC-2-06 Proxy settings.** `speedLimitKbps` is applied and drift-corrected. Changing a project between proxy-cache and normal is refused by CRD validation (Harbor can't change it).
  Tests: `internal/controller: TestProxyCacheProjectDriftInHarborIsPutBack`, `api/v1alpha1: TestPhase2CRDImmutability/proxy cache cannot be added`, `api/v1alpha1: TestPhase2CRDImmutability/proxy cache cannot be removed`

## Tag retention

- **AC-2-07 Retention create.** A `HarborRetentionPolicy` with two rules (keep the 5 most recently pushed `v*` tags; keep anything pulled in the last 30 days) and a daily schedule creates exactly that policy on the project.
  Tests: `internal/controller: TestRetentionPolicyCreatesExactlyTheSpecsPolicy`
- **AC-2-08 Retention effect.** Running the policy (the test triggers a run through Harbor's API) on a project with 8 pushed `v*` tags leaves the 5 newest.
  Tests: `internal/controller: TestRetentionPolicyRunKeepsTheFiveNewestVersionTags`
- **AC-2-09 Retention drift and ownership.** Rules added or changed in Harbor are reverted. A second retention resource for the same project reports `Conflict`. Deleting the resource removes the rules (or keeps them with `Orphan`).
  Tests: `internal/controller: TestImmutabilityRulesChangedInHarborArePutBack`, `internal/controller: TestImmutabilityPolicyDeletion/Delete removes the project's rules`, `internal/controller: TestImmutabilityPolicyDeletion/Orphan leaves the project's rules`, `internal/controller: TestRetentionPolicyRulesChangedInHarborArePutBack`, `internal/controller: TestSecondRetentionPolicyForTheSameProjectReportsConflict`, `internal/controller: TestDeletingRetentionPolicyFollowsItsDeletionPolicy/Delete removes the rules`, `internal/controller: TestDeletingRetentionPolicyFollowsItsDeletionPolicy/Orphan keeps the rules`

## Tag immutability

- **AC-2-10 Immutability.** A `HarborImmutabilityPolicy` with a rule for `v*` tags makes pushing over an existing `v1.0.0` fail, while `latest` can still be overwritten.
  Tests: `internal/controller: TestImmutabilityRuleStopsOverwritingMatchingTags`
- **AC-2-11 Immutability drift.** Rules added, changed or removed in Harbor are put back to exactly the resource's list. Deleting the resource removes them (or keeps them with `Orphan`).
  Tests: `internal/controller: TestImmutabilityRulesChangedInHarborArePutBack`, `internal/controller: TestImmutabilityPolicyDeletion/Delete removes the project's rules`, `internal/controller: TestImmutabilityPolicyDeletion/Orphan leaves the project's rules`

## System robots

- **AC-2-12 System robot.** A `HarborSystemRobot` with pull on all projects and push on two named projects creates a system robot and its Secret in the resource's namespace. Its credentials can pull from any project and push only to the two.
  Tests: `internal/controller: TestSystemRobotPullsFromEveryProjectAndPushesOnlyToNamedOnes`
- **AC-2-13 System robot lifecycle.** Lost-secret refresh, `rotate`, drift, adoption and deletion behave as for project robots (AC-1-16 to AC-1-21).
  Tests: `internal/controller: TestSystemRobotLifecycle/drift in Harbor is put back`, `internal/controller: TestSystemRobotLifecycle/a lost Secret gets a new secret`, `internal/controller: TestSystemRobotLifecycle/changing rotate refreshes the secret`, `internal/controller: TestSystemRobotLifecycle/an existing Harbor robot is adopted only when asked`, `internal/controller: TestSystemRobotDeletion/Delete removes the Harbor robot`, `internal/controller: TestSystemRobotDeletion/Orphan leaves the Harbor robot`
- **AC-2-14 System access.** A registry or system robot in a namespace that the connection's `allowedNamespaces` admits but its `systemAllowedNamespaces` doesn't reports `NotAllowed`.
  Tests: `internal/controller: TestSystemRobotInANamespaceOnlyAllowedNamespacesAdmits`

## Shipping

- **AC-2-15 Docs.** `docs/user/` has worked examples of a pull-through cache for ghcr.io and of retention and immutability policies; reference docs cover every new field.
  Tests:
