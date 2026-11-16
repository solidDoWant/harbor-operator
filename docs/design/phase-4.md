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

A `HarborSystemRobot` (or `HarborProjectRobot`) whose enabled output Secret is named in the `credentialsSecretRef` of a `HarborConnection` in the same namespace waits on deletion with `DependentsExist`, naming the connection. Field index on `spec.credentialsSecretRef.name`. Either deletion policy. Deleting the connection first lets the robot go.

### 2.4 Permission check on the connection

The connection gets a second condition, `PermissionsSufficient` (not part of `Ready`):

- **Required set:** computed in code from the kinds that have at least one resource using this connection, from a table in `internal/harbor` (for example `HarborProject` → system `project:create,list`, `quota:read,update`, project `project:read,update,delete`; `HarborProjectRobot` → `robot:*` plus every permission those robots request; `HarborRetentionPolicy` → `tag-retention:*`; `HarborImmutabilityPolicy` → `immutable-tag:*`; `HarborRegistry` → `registry:*`; `HarborSystemRobot` → `robot:*` plus what those robots request).
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
