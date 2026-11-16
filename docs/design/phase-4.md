# Phase 4 design: operator-managed credentials

Owner request (2026-11-16). Today the admin creates the operator's system robot by hand through Harbor's API: the web interface can't grant the permissions it needs, and the hand-kept permission list in the admin docs went wrong once. Phase 4 lets the operator manage its own robot declaratively with the kinds it already has. There is no special "bootstrap" mode. Builds on phases 1–3; their rules apply unless this page says otherwise.

## 1. The pattern

```yaml
# An admin connection: Harbor's own admin Secret, admitted only in its own namespace
kind: HarborConnection
metadata: {name: harbor-admin, namespace: harbor}
spec:
  url: http://harbor-core.harbor.svc.cluster.local
  credentialsSecretRef:
    name: harbor-admin-credentials     # the Secret Harbor's chart uses
    username: admin                    # literal username
    passwordKey: HARBOR_ADMIN_PASSWORD
---
# The operator's own robot, managed through the admin connection
kind: HarborSystemRobot
metadata: {name: harbor-operator, namespace: harbor}
spec:
  connectionRef: {name: harbor-admin}
  systemPermissions: [...]
  projectPermissions: [...]
  secret:
    dockerConfigJSON: {enabled: false}
    opaque: {enabled: true, name: harbor-operator-credentials}   # keys username, password, registry
---
# The connection every other resource uses
kind: HarborConnection
metadata: {name: harbor, namespace: harbor}
spec:
  url: http://harbor-core.harbor.svc.cluster.local
  credentialsSecretRef: {name: harbor-operator-credentials}
```

Applied together from empty, `harbor` is `CredentialsInvalid` until the robot's Secret exists, then turns `Ready` through the existing Secret watch. No operator restart and no ordering between Flux objects are needed.

## 2. Changes

### 2.1 Credential key mapping

```go
type CredentialsSecretReference struct {
    Name        string `json:"name"`                  // same namespace as the connection
    Username    string `json:"username,omitempty"`    // literal username; CEL: not with usernameKey
    UsernameKey string `json:"usernameKey,omitempty"` // default "username"
    PasswordKey string `json:"passwordKey,omitempty"` // default "password"
}
```

`HarborConnection.spec.credentialsSecretRef` changes from `LocalSecretReference` to this type. Existing objects (only `name`) keep working with the defaults. `HarborRegistry.spec.credentialsSecretRef` is unchanged. The Secret watch and the client cache key are unchanged; the cache key also includes the three new fields through `metadata.generation`.

### 2.2 The OperatorCredential guard, narrowed

The phase 2 rule refused to manage any system robot whose Harbor name equals the username of *any* connection to the same Harbor. New rule: refuse only when the **managing** connection (the robot's own `connectionRef`) signs in as that robot. That is the real hazard: a credential replacing or rotating itself. A robot managed through another connection (the admin connection) is reconciled normally, so its updates and secret refreshes happen in place (admin can update robots; no replacement).

### 2.3 Deletion waits for connections that use the robot's Secret

A `HarborSystemRobot` (or `HarborProjectRobot`) whose enabled output Secret is named in the `credentialsSecretRef` of a `HarborConnection` in the same namespace waits on deletion with `DependentsExist`, naming the connection. Field index on `spec.credentialsSecretRef.name`. Either deletion policy. Deleting the connection first lets the robot go. Only an output Secret whose controller owner reference is the robot counts: a Secret the spec names but the robot does not own (a `SecretConflict`, or a Secret refused with `OperatorCredential`) does not go with the robot, so the robot does not wait for its users.

### 2.4 Permission check on the connection

The connection gets a second condition, `PermissionsSufficient` (not part of `Ready`):

- **Required set:** computed in code from the kinds that have at least one resource using this connection, from a table in `internal/harbor` (for example `HarborProject` → system `project:create,list`, `quota:list,update`, project `project:read,update,delete`; `HarborProjectRobot` → `robot:*` plus every permission those robots request; `HarborRetentionPolicy` → `tag-retention:*`; `HarborImmutabilityPolicy` → `immutable-tag:*`; `HarborRegistry` → `registry:*`; `HarborSystemRobot` → `robot:*` plus what those robots request).
- **Held set:** `GET /api/v2.0/users/current/permissions` with `scope=/system` and `scope=/project/*` (relative). An admin user holds everything.
- **Missing:** `PermissionsSufficient=False, reason=InsufficientPermissions`, message listing up to 10 missing `scope resource:action` entries, a Warning event when the set changes. Re-checked on every connection reconcile and when a dependent kind's resource is created.
- The same table generates the admin docs' permission list (`make generate`), so docs can't drift from code.

The admin connection needs no special flag. Its power is contained by its admission lists, which default to its own namespace only. The admin docs say to leave them that way.

## 3. Reconcile rows (additions)

| Situation | Response | Test |
|---|---|---|
| Connection Secret lacks the mapped key | `CredentialsInvalid`, naming the key | integration |
| `username` and `usernameKey` both set | rejected by CEL | integration |
| System robot whose managing connection signs in as it | `Conflict`, `OperatorCredential` | integration |
| System robot managed through another connection, whose output Secret a connection uses | normal reconcile; rotate, drift and refresh happen in place (same robot ID) | integration |
| Robot deleted while a connection uses its output Secret | `DependentsExist` | integration |
| Connection credential lacks a required permission | `PermissionsSufficient=False, InsufficientPermissions` with the list | integration |
| The root chain applied from empty | all three `Ready` without restart | e2e |
| A hand-made system robot adopted (`adopt: true`, admin connection `allowAdoption`) | its secret is refreshed and written; the operator's connection switches to it | integration |

## 4. Docs

`docs/admin/credentials.md` leads with the root-robot pattern above (with Harbor's chart Secret names: `existingSecretAdminPassword`'s Secret or `<release>-core`, key `HARBOR_ADMIN_PASSWORD`). The admin can still use a hand-made robot or user. A migration section covers adopting an existing hand-made robot. The caveat that Harbor's admin Secret holds only the *initial* admin password is stated: if it was changed in the UI, the admin connection turns `Unauthorized` and the operator keeps working on its current robot until the next rotation needs admin.

## 5. Deleting a project's images (owner, 2026-11-16, issue #142)

Harbor refuses to delete a project that still holds repositories. A CI chart that makes one project per repository then can't offboard a repository with `Delete`: the `HarborProject` stays `DeleteBlocked`, and so does its namespace. This section adds an explicit opt-in that empties the project first. It changes SPEC principle 4 for that opt-in only.

### 5.1 API

- New `DeletionPolicy` value **`DeleteWithContents`**. It is accepted wherever `deletionPolicy` is (resources and `HarborConnection.spec.defaults`). On `HarborProject` it means "delete every repository, then the project". On every other kind it means the same as `Delete`.
- New `HarborConnection.spec.allowContentDeletion` (bool, default `false`). The opt-in on a resource takes effect only when its connection allows it, so an admitted namespace can't wipe images on a connection whose admin didn't agree. This matters most for adopted projects.

### 5.2 Reconcile rows (project deletion, after the phase 1 deletion preamble)

| Situation | Response | Test |
|---|---|---|
| Effective policy `DeleteWithContents`, connection `allowContentDeletion: false` | `Ready=False, ContentDeletionNotAllowed`. Nothing is deleted; the resource behaves like `Delete` (`DeleteBlocked` while images remain). Warning event once. | integration |
| `DeleteWithContents`, allowed, project has repositories | List repositories (`GET /projects/{name}/repositories`, paginated), delete each (`DELETE /projects/{name}/repositories/{repo}`; 404 counts as deleted), then delete the project and remove the finalizer. Status message during the run: `deleting contents: N repositories left`. | integration |
| A repository delete fails (5xx, network) partway | Keep the finalizer, `HarborUnavailable` with the count left, backoff. The next reconcile lists again and continues. | integration |
| Repository delete answers 403 | `Ready=False, Forbidden` naming the missing permission (`repository:delete` at project scope); nothing else deleted. | integration |
| Proxy-cache project | Same rows (cached repositories are deleted the same way). | integration |
| Effective policy `DeleteWithContents` while the project is not owned (`Conflict`, `AlreadyExists`, no ID) | The phase 1 preamble applies first: nothing is deleted. | unit |

Repository names in the path are URL-encoded twice, as Harbor requires for names containing `/` (`library%252Fapp`), through go-client's parameters. Never build the path by hand.

### 5.3 Permissions

`PermissionsSufficient` (section 2.4) adds project-scope `repository:delete` to the required set when any project using the connection has effective policy `DeleteWithContents` and the connection allows it.

### 5.4 Docs

The user docs say plainly that `DeleteWithContents` deletes every image in the project permanently and can't be undone. The admin docs explain `allowContentDeletion`.

## 6. Amendments after the design review

The design review on issue #149 found gaps. This section answers them and overrides sections 1–4 where they disagree.

### 6.1 Reading the held permissions (C1)

`GET /users/current/permissions` can't serve as written: `scope=/project/*` returns nothing, `scope=/system` doesn't report `robot` or `quota`, and project-scope `project` comes back as resource `"."`. The held set is read like this instead:

1. `GET /users/current` (go-client `User.GetCurrentUserInfo`). A 200 with `sysadmin_flag: true` means everything is held, so `PermissionsSufficient=True` and no further calls. Robots get 412 here and go on to step 2.
2. **System scope:** read the credential's own robot record (`GET /robots?q=Level=system`, matching the full name) and take its system-scope access list. Harbor 2.15 answers `scope=/system` with an empty list, even for an administrator, so the permission list can't serve here. Harbor itself checks a robot's new robot against the creating robot's record. Reading the record needs `robot:list`, which the table requires anyway whenever robots are managed; without it nothing is held. A user that isn't a system administrator holds nothing at system scope.
3. **Project scope:** one `scope=/project/<id>&relative=true` query per distinct project ID among admitted dependents (the `status.projectID` of admitted `HarborProject`s, and the IDs of projects named in admitted system robots' `projectPermissions`; `*` checks one existing project). Map resource `"."` to `project`. Requirements of projects that don't exist yet aren't checked; the docs say so.

The same reading fixes the shipped replace-safety check (`systemRobotTarget.missingPermissions`), which queries scope `"/"` and gets nothing back. That fix is its own issue (filed with this change).

### 6.2 The guard (H1)

Refuse (`Conflict`, `OperatorCredential`) when **either** the managing connection signs in as the robot, **or** any connection to the same Harbor identity that signs in as the robot is in a namespace other than the robot's. "Signs in as" uses the mapped username: the literal `username`, or the value at `usernameKey`, defaulting to `username`. A connection other than the managing one signs in as the robot only while Harbor accepts its current credentials: its `Ready` condition is `True` for its current generation. A connection that Harbor rejects keeps its `status.harborIdentity`, but it signs in as nothing, so a namespace cannot freeze another namespace's robot by naming its username without its secret. The managing connection counts in any state, because the operator's own Harbor calls go through it. Extra row: system robot whose Harbor name is the credential of a connection in another namespace → `OperatorCredential`. `docs/design/phase-2.md` section 2.8's guard row is replaced by this rule.

### 6.3 CRD defaults (H2)

No OpenAPI defaults on `usernameKey` or `passwordKey`. The defaults (`username`, `password`) are applied in code (`clientFor` and `internal/harbor`). CEL: `!(has(self.username) && has(self.usernameKey))`; `MinLength=1` on all three fields. The AC-4-01 integration test applies the `username: admin` object.

### 6.4 Project permissions for system robots (H3)

`HarborSystemRobot.spec.projectPermissions` allows `project: read|update|delete` (Harbor 2.15 offers them to robots, `PoliciesMap[ScopeProject]`). `HarborProjectRobot.spec.permissions` does **not**, so a tenant can't mint a robot that deletes or edits the project its `HarborProject` owns. The list generator and `docs/user/robot-permissions.md` gain these entries for system robots, and an integration test shows Harbor accepts them and that a robot holding them can read and update a project.

### 6.5 The required-set table (H4)

- The table is `(kind, scope, resource, action)`, with one row per Harbor call that kind's reconciler makes through `internal/harbor`. The first implementation issue derives it mechanically: each `internal/harbor` client method is annotated with the permissions it needs, and the table is the union over the methods each reconciler calls. A unit test checks every method used by a reconciler has an annotation.
- Scopes: project robots, tag retention and immutable tags are **project** scope. Creating projects and updating quotas are system scope.
- `robot:update` is required only for user credentials. Robot credentials replace robots instead (phase 1 section 3.4), so it's never required of them.
- A unit test checks every entry is in the CEL allow-lists, so each can be granted to a robot. AC-4-03's root robot, built from the generated list, proves the list is enough.

### 6.6 Deleting or removing a Secret a connection uses (M1)

- The `DependentsExist` wait runs **before** deletion-preamble steps 2–5 (phase 1 section 3.1), because the robot's Secrets go by owner reference under every policy.
- New watch: a `HarborConnection` event maps to the robots (system and project) in its namespace whose enabled output Secret name equals the connection's `spec.credentialsSecretRef.name`.
- Disabling or renaming an output Secret that a connection in the same namespace names: the old Secret isn't deleted, and the robot reports `DependentsExist` naming the connection, until the connection stops using it.

### 6.7 Migration procedure (M2)

Adopting a hand-made robot: create the `HarborSystemRobot` with `adopt: true` writing a **new** Secret name, and in the same change point the main connection's `credentialsSecretRef` at it. Adoption refreshes the secret at once, so the main connection is `Unauthorized` until the operator writes the new Secret and the connection re-validates (seconds, through the Secret watch). The AC-4-09 test follows this procedure and checks the connection ends up `Ready`. Afterwards the admin turns `allowAdoption` off on the admin connection.

### 6.8 Security notes in the docs (M3, L1, L3)

The admin security docs state:
- Permission to create a `HarborConnection` in a namespace equals reading every Secret there (any key can be sent to any URL). Grant it only where Secret read is already granted.
- A connection's credential must not come from a robot managed, directly or through dependents, by that same connection; bulk deletion would deadlock.
- Harbor's chart re-renders `<release>-core` on upgrade. If the admin password was changed in the UI, point the admin connection at your own Secret.
- Turn `allowAdoption` off on the admin connection after migrating.

### 6.9 Permission-check inputs and timing (M4)

- Only **admitted** dependents count: `allowedNamespaces` for projects, and through projects their robots and policies; `systemAllowedNamespaces` for registries and system robots. Project robots and policies are found through the admitted `HarborProject`s' indexes (`spec.projectRef`).
- Triggers: the connection reconcile itself, plus a Create-only watch predicate on each dependent kind that re-queues its connection (directly or through the project). No check while `Ready` is not `True`; the condition is then `Unknown` with the same reason as `Ready`.
- Events: Warning `InsufficientPermissions` when the missing set changes; Normal `PermissionsSufficient` on recovery.
- Entry format: `system robot:create`, `project <name> repository:push`.

## 7. Amendments after the content-deletion review

The second review on issue #149 (section 5) found gaps. This section answers them and overrides section 5 where they disagree.

### 7.1 Repository names in the delete path (H1)

`GET /projects/{name}/repositories` returns names with the project prefix (`proj/team/app`). The delete call takes the name **without** `proj/`, encoded once by the caller: pass `url.PathEscape("team/app")` (`team%2Fapp`) as go-client's `RepositoryName`, and the runtime adds the second encoding (`team%252Fapp`). A unit test on the `internal/harbor` wrapper checks `a/b` → request path `…/repositories/a%252Fb`. A 404 on a repository delete counts as deleted only if the next listing no longer shows that repository. Otherwise it is an error (`InvalidSpec`, "repository delete answered 404 but the repository still exists"), so a wrong name can't loop forever.

### 7.2 412 answers (H2)

| Situation | Response | Test |
|---|---|---|
| Repository delete answers 412 (for example an immutable tag) | `Ready=False, DeleteBlocked` with Harbor's message, keep the finalizer, backoff (max 10 min). The operator never changes immutability rules here. | integration (immutable rule made directly in Harbor) |
| Project delete answers 412 after the listed repositories were deleted (a push during the run, a proxy cache filling again) | Requeue and start again from listing, with the same message `deleting contents: N repositories left` | integration |

The user docs (section 5.4) say that immutable tags block `DeleteWithContents` and must be removed by hand.

### 7.3 Permissions (M1)

`PermissionsSufficient` requires project-scope `repository:list` **and** `repository:delete` for `DeleteWithContents`. The 403 row covers both calls and names the permission of the call that failed.

### 7.4 Ownership during a multi-reconcile run (M2)

The phase 1 step-5 ID check (`GET /projects/{id}` plus a name match) runs at the start of **every** reconcile that deletes content. Only listed repositories whose `project_id` equals `status.projectID` are deleted. If any listed repository has another `project_id`, stop and release as step 5 does for a changed ID.

### 7.5 Adoption plus content deletion (M3, decided by the operator with the owner present)

No code rule. The admin docs say plainly that `allowAdoption` and `allowContentDeletion` together let every admitted namespace permanently delete the images of any project it can adopt, and that `defaults.deletionPolicy: DeleteWithContents` applies to adopted projects too. The owner can tighten this later.

### 7.6 What is reported when the connection doesn't allow it (M4)

The `Ready` reason is `DeleteBlocked`, with Harbor's message plus "content deletion is not allowed by HarborConnection `ns/name`". A Warning event with reason `ContentDeletionNotAllowed` is emitted when the condition first turns to this state. The operator still tries the project delete first, so an empty project is deleted normally. `ReasonContentDeletionNotAllowed` is added to the event reason constants and documented. AC-4-12 is read this way.

### 7.7 Bounded work per reconcile (M5, L1)

Each reconcile takes page 1 with `page_size=100`, deletes those repositories, updates the message with what is left (`N` from the `X-Total-Count` header), and requeues at once until the list is empty. Deleting while paging forward would skip items, so the operator always takes page 1 again. When the list itself fails, the message keeps the last count.
