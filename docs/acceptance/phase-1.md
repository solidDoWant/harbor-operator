# Phase 1 acceptance checklist: projects and robots

Approved for the owner by the operator on 2026-11-15. Only the owner adds scenarios. Each scenario lists its tests on a `Tests:` line (`` `package/dir: TestName/subtest` ``); a scenario passes when every listed test passes on `master`. Workers add their tests to the line; that is not a new scenario. `make acceptance` prints the scoreboard.

## Connection

- **AC-1-01 Connect.** A `HarborConnection` with valid system-robot credentials becomes `Ready` and shows Harbor's version. With a wrong password it shows `Unauthorized`.
  Tests: `internal/controller: TestConnectionBecomesReadyWithValidCredentials`, `internal/controller: TestConnectionReportsBadCredentials/wrong password`
- **AC-1-02 Credential change.** After the credential Secret is updated to new valid credentials, dependent resources keep reconciling without restarting the operator.
  Tests: `internal/controller: TestConnectionRevalidatesWhenCredentialsSecretChanges`

## Projects

- **AC-1-03 Create project.** A `HarborProject` with `public`, `storageQuota: 5Gi`, prevent-vulnerable-images (severity high), scan-on-push and a CVE allowlist creates a Harbor project with exactly those settings, and the resource becomes `Ready` with the project ID.
  Tests: `internal/controller: TestProjectIsCreatedWithTheSpecSettings`, `test/e2e: TestProjectIsCreatedByTheOperatorInstalledFromTheChart`
- **AC-1-04 Update project.** Changing each of those fields in the resource changes it in Harbor.
  Tests: `internal/controller: TestProjectFollowsSpecChanges`
- **AC-1-05 Drift.** Changing those settings in Harbor's API (as the UI would) is reverted within one resync interval. Settings the resource leaves unset keep their Harbor value.
  Tests: `internal/controller: TestProjectDriftInHarborIsReverted`, `internal/controller: TestProjectSettingsDiffWritesOnlySetFieldsThatDiffer`
- **AC-1-06 Recreate.** A project deleted directly in Harbor is created again.
  Tests: `internal/controller: TestProjectDeletedInHarborIsRecreated`
- **AC-1-07 Delete.** Deleting the resource deletes the empty project. With `deletionPolicy: Orphan` the project stays.
  Tests: `internal/controller: TestProjectDeletion/policy Delete deletes the empty Harbor project`, `internal/controller: TestProjectDeletion/policy Orphan leaves the Harbor project`
- **AC-1-08 Delete blocked.** Deleting the resource of a project that holds an image leaves the project in place, and the resource reports `DeleteBlocked` until the image is removed, then finishes.
  Tests: `internal/controller: TestProjectDeletion/a project holding an image reports DeleteBlocked until the image is removed`, `internal/controller: TestProjectRetryBackoffIsCappedAtTenMinutes`
- **AC-1-09 Already exists.** A resource naming an existing, unowned Harbor project reports `AlreadyExists` and changes nothing. With `adopt: true` (and the connection allowing adoption) it takes the project over and applies its settings.
  Tests: `internal/controller: TestProjectNamingAnExistingHarborProject/without adopt reports AlreadyExists and changes nothing`, `internal/controller: TestProjectNamingAnExistingHarborProject/adopt with allowAdoption takes the project over`
- **AC-1-10 Adoption needs the connection's permission.** With `allowAdoption: false` on the connection, `adopt: true` reports `AdoptionNotAllowed`.
  Tests: `internal/controller: TestProjectNamingAnExistingHarborProject/adopt without allowAdoption reports AdoptionNotAllowed`
- **AC-1-11 Conflict.** A second resource (any namespace, even through a second connection to the same Harbor) naming the same Harbor project reports `Conflict`, and the first keeps working. Deleting the second never deletes the project.
  Tests: `internal/controller: TestSecondResourceForTheSameHarborProjectConflicts`, `internal/controller: TestProjectDeletion/a resource in Conflict never touches the project, even with an ID`

## Namespace access

- **AC-1-12 Connection access.** A project in another namespace than the connection, which the connection doesn't admit, reports `NotAllowed` and nothing is created in Harbor. Adding the namespace's label to the connection's `allowedNamespaces` selector makes it proceed.
  Tests: `internal/controller: TestProjectInANamespaceTheConnectionDoesNotAdmit`
- **AC-1-13 Project access.** A robot in another namespace referencing a project reports `NotAllowed` until the project's `allowedNamespaces` admits that namespace (by name or by label selector). A robot in the project's own namespace works with no `allowedNamespaces` set.
  Tests: `internal/controller: TestRobotInANamespaceTheProjectDoesNotAdmit`, `internal/controller: TestRobotWithPushAndPullCanPushOnlyToItsProject`

## Robots

- **AC-1-14 Create robot.** A `HarborProjectRobot` with push and pull on the project creates a project robot and a `kubernetes.io/dockerconfigjson` Secret in its namespace. `docker login`-equivalent credentials from that Secret can push an image to the project and can't push to another project.
  Tests: `internal/controller: TestRobotWithPushAndPullCanPushOnlyToItsProject`
- **AC-1-15 Secret options.** Disabling the dockerconfigjson Secret, enabling the opaque Secret and setting custom names produce exactly the requested Secrets with the documented keys; switching back removes the disabled ones.
  Tests: `internal/controller: TestRobotSecretOptionsWriteExactlyTheRequestedSecrets`
- **AC-1-16 Lost secret.** Deleting the robot's Secret produces a new working secret; the old one no longer authenticates.
  Tests: `internal/controller: TestRobotSecretDeletedGetsANewRobotSecret`
- **AC-1-17 Rotate.** Changing `secret.rotate` gives the robot a new secret and rewrites the Secret.
  Tests: `internal/controller: TestRobotRotateGivesTheRobotANewSecret`
- **AC-1-18 Robot drift.** Permissions, description, expiry and disabled state changed in Harbor are put back, both with a system-robot connection (by replacing the robot and rewriting its Secret) and with an admin-user connection (in place).
  Tests:
- **AC-1-19 Robot delete.** Deleting the resource deletes the Harbor robot and its Secrets; with `Orphan` the Harbor robot stays.
  Tests: `internal/controller: TestRobotDeletion/Delete removes the Harbor robot`, `internal/controller: TestRobotDeletion/Orphan leaves the Harbor robot`
- **AC-1-20 Secret conflict.** A robot whose target Secret name is taken by a Secret it doesn't own reports `SecretConflict` and leaves that Secret untouched.
  Tests: `internal/controller: TestRobotSecretNameTakenByAForeignSecret`
- **AC-1-21 Robot adoption.** An existing robot is adopted only with `adopt: true` and gets a fresh secret written to its Secret.
  Tests: `internal/controller: TestRobotAdoptsAnExistingHarborRobot`, `internal/controller: TestRobotAdoptionNeedsTheConnectionToAllowIt`
- **AC-1-22 Project deletion waits for robots.** Deleting a project while a robot resource references it waits (`DependentsExist`) until the robot is deleted, then completes.
  Tests: `internal/controller: TestProjectDeletion/deletion waits for robots in namespaces the project admits`

## Secrets and errors

- **AC-1-23 No secret leaks.** Robot secrets and connection passwords appear in no status, condition, event or operator log line during the robot scenarios above.
  Tests:
- **AC-1-24 Invalid spec.** A spec Harbor rejects (for example a CVE allowlist with an invalid ID) reports `InvalidSpec` with Harbor's message; fixing the spec clears it. Specs CRD validation can reject (both quota fields set, unknown permission) are refused by the API server.
  Tests: `internal/controller: TestProjectSpecHarborRejectsReportsInvalidSpec`, `api/v1alpha1: TestCRDValidation/quota with unlimited`, `api/v1alpha1: TestCRDValidation/unlisted action`, `api/v1alpha1: TestCRDValidation/unlisted resource`

## Shipping

- **AC-1-25 Chart install.** `helm install` of the chart into a kind cluster with the released-image tag runs the operator, and AC-1-03 passes against it (the e2e suite runs the operator from the chart).
  Tests: `test/e2e: TestProjectIsCreatedByTheOperatorInstalledFromTheChart`
- **AC-1-26 Docs.** `docs/user/` shows a complete example of a project plus a CI push robot; `docs/admin/` explains creating the system robot, the connection and allowing namespaces. `make docs-check` passes.
  Tests:
