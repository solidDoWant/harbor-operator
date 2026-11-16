# Phase 4 acceptance checklist: operator-managed credentials

Same rules as phase 1. Approved by the owner on 2026-11-16 (design: `docs/design/phase-4.md`).

- **AC-4-01 Key mapping.** A `HarborConnection` whose Secret is Harbor's admin Secret (only key `HARBOR_ADMIN_PASSWORD`), with `username: admin` and `passwordKey: HARBOR_ADMIN_PASSWORD`, becomes `Ready`. A missing mapped key reports `CredentialsInvalid` naming the key. `username` together with `usernameKey` is refused by the API server.
  Tests: `internal/controller: TestConnectionMapsCredentialKeys`, `internal/harbor: TestMappedCredentials`
- **AC-4-02 Existing connections.** A connection with only `credentialsSecretRef.name` (keys `username` and `password`) keeps working unchanged after the upgrade.
  Tests: `internal/controller: TestConnectionBecomesReadyWithValidCredentials`, `internal/harbor: TestMappedCredentials/defaults`
- **AC-4-03 Root chain from empty.** An admin connection, a `HarborSystemRobot` managed through it writing an opaque Secret, and a second connection using that Secret, all applied at once to an empty namespace, become `Ready` without restarting the operator. A `HarborProject` through the second connection then works.
  Tests:
- **AC-4-04 Rotation in place.** Changing the root robot's `secret.rotate` gives it a new secret with the same Harbor robot ID. The second connection re-validates and stays usable; its dependents keep working.
  Tests:
- **AC-4-05 Drift in place.** Permissions or description of the root robot changed in Harbor are put back with the same robot ID.
  Tests:
- **AC-4-06 Narrow guard.** A system robot whose own managing connection signs in as it reports `Conflict` with reason `OperatorCredential` and changes nothing. The same robot managed through the admin connection reconciles normally.
  Tests:
- **AC-4-07 Deletion waits.** Deleting the root robot while a connection uses its Secret reports `DependentsExist`, and the Harbor robot stays. After that connection is deleted, the robot deletion finishes.
  Tests:
- **AC-4-08 Permission check.** A connection whose credential lacks a permission that an existing resource kind needs reports `PermissionsSufficient=False` with reason `InsufficientPermissions`, listing it. After the permission is granted, the condition turns `True` within one resync. An admin-user connection reports `True`.
  Tests:
- **AC-4-09 Adopting a hand-made robot.** An existing hand-made system robot adopted by a `HarborSystemRobot` through the admin connection gets a fresh secret in its Secret, and a connection using that Secret becomes `Ready`.
  Tests:
- **AC-4-10 Docs.** The admin docs lead with the root-robot pattern, generate the permission list from the operator's code, and cover migrating from a hand-made robot. `make docs-check` passes.
  Tests: `internal/harbor/permtable: TestCredentialsDocLeadsWithTheRootRobotPattern`
- **AC-4-11 Delete with contents.** A `HarborProject` with `deletionPolicy: DeleteWithContents`, on a connection with `allowContentDeletion: true`, that holds images in two repositories (one with a `/` in its name), is deleted together with its images, and the resource goes away.
  Tests:
- **AC-4-12 Not allowed by the connection.** The same project on a connection without `allowContentDeletion` reports `DeleteBlocked` saying the connection doesn't allow content deletion (with a `ContentDeletionNotAllowed` Warning event) when deleted, deletes no image, and finishes once the images are removed by hand.
  Tests:
- **AC-4-13 Resumes after a failure.** When Harbor fails partway through deleting the repositories, the resource reports what is left, and the deletion finishes by itself once Harbor answers again.
  Tests:
- **AC-4-14 Other kinds.** `DeleteWithContents` on a robot, policy or registry resource behaves exactly like `Delete`.
  Tests:
