# Phase 2 design: registries, proxy caches, policies and system robots

Builds on `docs/design/phase-1.md`: the same reconcile preamble (finalizer, references and admission, client), ownership rules, adoption, deletion policies and error reasons apply to every kind here. Only differences are listed.

## 1. Types

```go
// HarborRegistry (namespaced; admitted by the connection's systemAllowedNamespaces)
type HarborRegistrySpec struct {
    ConnectionRef        ObjectReference       `json:"connectionRef"`
    Name                 string                `json:"name,omitempty"` // default metadata.name; immutable
    Type                 string                `json:"type"`           // Harbor adapter type; immutable; Harbor validates it
    URL                  string                `json:"url"`
    CredentialsSecretRef *LocalSecretReference `json:"credentialsSecretRef,omitempty"` // keys accessKey, accessSecret
    Insecure             bool                  `json:"insecure,omitempty"`
    Description          string                `json:"description,omitempty"`
    AllowedNamespaces    AllowedNamespaces     `json:"allowedNamespaces,omitempty"` // whose HarborProjects may use it
    DeletionPolicy, Adopt, ResyncInterval      // as phase 1
}
// status: see section 2.1

// HarborProject additions
type ProxyCacheSpec struct {
    RegistryRef    ObjectReference `json:"registryRef"`
    SpeedLimitKbps *int32          `json:"speedLimitKbps,omitempty"` // -1 = unlimited
}
// CEL: proxyCache presence is immutable (set at creation or never).

// HarborRetentionPolicy (namespaced)
type HarborRetentionPolicySpec struct {
    ProjectRef ObjectReference `json:"projectRef"`
    Rules      []RetentionRule `json:"rules"`    // 1..15
    Schedule   string          `json:"schedule,omitempty"` // Harbor cron (6 fields); empty = manual only
    DeletionPolicy, ResyncInterval
}
type RetentionRule struct {
    Disabled     bool              `json:"disabled,omitempty"`
    Template     string            `json:"template"` // latestPushedK | latestPulledN | nDaysSinceLastPush | nDaysSinceLastPull | always
    Count        *int32            `json:"count,omitempty"` // K, N or days; CEL: required unless always
    Repositories Selector          `json:"repositories"`    // {decoration: repoMatches|repoExcludes, pattern}
    Tags         Selector          `json:"tags"`            // {decoration: matches|excludes, pattern, untagged bool}
}

// HarborImmutabilityPolicy (namespaced)
type HarborImmutabilityPolicySpec struct {
    ProjectRef ObjectReference      `json:"projectRef"`
    Rules      []ImmutabilityRule   `json:"rules"` // {disabled, repositories Selector, tags Selector}
    DeletionPolicy, ResyncInterval
}

// HarborSystemRobot (namespaced; admitted by systemAllowedNamespaces)
type HarborSystemRobotSpec struct {
    ConnectionRef  ObjectReference         `json:"connectionRef"`
    Name, Description, Duration, Disabled, Secret, DeletionPolicy, Adopt, ResyncInterval // as HarborProjectRobot
    SystemPermissions  []RobotAccess        `json:"systemPermissions,omitempty"`  // kind=system
    ProjectPermissions []ProjectPermission  `json:"projectPermissions,omitempty"` // {projects []string ("*" = all), access []RobotAccess}
}
```

Policies have no `adopt`: a project has exactly one retention policy and one immutability rule set in Harbor, and the policy resource always takes over whatever is there (the project resource already proved ownership). Their project must be admitted as for robots.

## 2. Reconcile tables (differences from phase 1)

Amended after the design review on issue #75. Everything in `docs/design/phase-1.md` section 3 (preambles, ownership, pending markers, safety rules) applies to the new kinds unless a row below says otherwise. Where this section and section 1 disagree, this section wins.

### 2.1 Status fields, claim keys and reasons

| Kind | Status fields | `status.harborKey` | Claimed ID (the ownership record of phase 1 section 3.5) |
|---|---|---|---|
| `HarborRegistry` | `registryID`, `harborKey`, `createPending`, `health` (`healthy`, `unhealthy`, `unknown`), conditions | identity + `registry` + name | `registryID` |
| `HarborSystemRobot` | as `HarborProjectRobot` | identity + `system-robot` + name | `robotID` |
| `HarborRetentionPolicy` | `projectID`, `retentionID`, `harborKey`, conditions | identity + `retention` + project name | `retentionID` |
| `HarborImmutabilityPolicy` | `projectID`, `harborKey`, conditions | identity + `immutability` + project name | `projectID` (set only while this resource is the owner) |

The phase 1 ownership rule applies unchanged to every kind: the claimant that records the claimed ID owns the object, otherwise the oldest admitted claimant. A non-owner gets `Conflict` on every reconcile and never writes or deletes. The deletion preamble's "Harbor ID in status" means the claimed ID above.

New condition reasons: `RegistryNotReady`, `RegistryOnOtherHarbor`, `ProjectNotFound`, `OperatorCredential`. New event reason: `HealthChanged`.

### 2.2 Dependency graph (who waits for whom on deletion)

- `HarborConnection` waits for the `HarborProject`s its `allowedNamespaces` admits, and for the `HarborRegistry`s and `HarborSystemRobot`s its `systemAllowedNamespaces` admits (new `spec.connectionRef` field indexes on both kinds).
- `HarborRegistry` waits for the proxy-cache `HarborProject`s its `allowedNamespaces` admits (index on `spec.proxyCache.registryRef`, namespace defaulted), under either policy.
- `HarborProject` waits for the robots and policies it admits, as in phase 1.
- Only admitted dependents count. A proxy-cache project that is deleted doesn't need its registry: project deletion ignores `registryRef`.

### 2.3 HarborRegistry

| Situation | Response | Test |
|---|---|---|
| Namespace not admitted by the connection's `systemAllowedNamespaces` (own namespace always admitted) | `NotAllowed` | integration |
| Credentials Secret named but missing, or lacks `accessKey`/`accessSecret` | `Ready=False, CredentialsInvalid`; nothing written | integration |
| Create (no ID, owner) | `status.createPending`, `POST /registries` with `credential.type: basic` when credentials are set, record ID, clear the marker | e2e |
| ID set, `GET /registries/{id}` returns 404, or `harborKey` names another Harbor identity | stale ID: clear it and create (`Recreated` on 404). A different **name** is not staleness: Harbor can rename registries, so a name change is drift. | e2e |
| `name`, `url`, `description` or `insecure` differ, or a resync is due | `PUT /registries/{id}` with all spec fields, **and the credentials from the Secret on every PUT** (Harbor never returns the secret, so this is the only way to correct UI changes; it also covers Secret changes). No hash of the credential is stored anywhere. | e2e |
| `credentialsSecretRef` removed from the spec | leave Harbor's credential as it is (unset fields are not touched) | unit |
| After each create or update | `POST /registries/ping` with the ID: 200 → `health: healthy`; an error about the upstream → `unhealthy`; not tried → `unknown`. A 401/403 for the operator's own call maps to the usual reasons. `health` never changes `Ready`. Emit `HealthChanged` (Warning when `unhealthy`) only when the value changes. The ping message is not copied into events. | e2e |
| Deleted, Delete, Harbor answers 412 (in use by a project or replication rule) | `DeleteBlocked` with Harbor's message, backoff max 10 min | integration |
| Deleted, admitted proxy-cache projects still reference it | `DependentsExist` (either policy) | integration |

Look up by name with `GET /registries?q=name=<name>` (exact match). CEL: `type` is immutable; `url` has no userinfo and no trailing slash; `name` matches `^[a-z0-9]+(?:[._-][a-z0-9]+)*$`. `type` is a string with that pattern; Harbor's 400 for an unknown type is `InvalidSpec`. "Type-specific settings" in the checklist means `url`, `insecure`, `description` and the credential.

### 2.4 HarborProject with proxyCache

| Situation | Response | Test |
|---|---|---|
| Create or adoption, registry resource missing, without `status.registryID`, or in `Conflict`/`NotAllowed` | `ReferenceResolved=False, RegistryNotReady`; nothing created. (Like robots, it does not wait for the registry's `Ready`.) | integration |
| Registry doesn't admit the project's namespace (any reconcile) | `NotAllowed` | integration |
| Registry's `harborKey` identity differs from the project connection's identity (any reconcile) | `ReferenceResolved=False, RegistryOnOtherHarbor`; nothing written | integration |
| Create | `POST /projects` with `registry_id` from the registry's status and metadata `proxy_speed_kb` | e2e |
| Existing project: Harbor's `registry_id` differs from the registry's `status.registryID` (UI change, registry re-created) | `PUT` it back. If Harbor refuses to change `registry_id`, `InvalidSpec` with Harbor's message, nothing else written. | e2e |
| Adoption: Harbor project is a proxy cache and the spec has no `proxyCache`, or the other way round | `InvalidSpec` ("proxy-cache mode can't be changed"); nothing written. A different `registry_id` alone is drift, handled by the row above. | integration |
| Registry later not `Ready` or terminating | no effect: the project keeps reconciling | integration |

CEL: `proxyCache` presence and `proxyCache.registryRef` (namespace defaulted) are immutable. `speedLimitKbps` is `-1` or greater than 0; it maps to metadata `proxy_speed_kb` (a string, phase 1 section 3.8); unset is not touched.

### 2.5 HarborRetentionPolicy and HarborImmutabilityPolicy (shared rules)

| Situation | Response | Test |
|---|---|---|
| Project resource missing, without `status.projectID`, or `Conflict`/`NotAllowed` | `ProjectNotReady` | integration |
| Project doesn't admit the policy's namespace | `NotAllowed` | integration |
| Not the owner (phase 1 section 3.5, claim keys above) | `Conflict`; never writes or deletes | integration |
| `status.projectID` differs from the project resource's (project re-created) | clear the claimed ID and start over | integration |
| Deleted, Harbor answers 404 for the project or policy | counts as deleted | unit |

**No adoption for policies, by design.** A project has exactly one retention policy and one immutability rule set, and the policy resource takes over whatever is there: SPEC section 5 says rules made in the UI are removed. The project resource already proved ownership of the project. The admin security docs state this exception.

Watches: `HarborProject` through a `spec.projectRef` index, `Namespace`, and policies with the same `harborKey`.

### 2.6 HarborRetentionPolicy

| Situation | Response | Test |
|---|---|---|
| Project metadata `retention_id` unset | `POST /retentions` (scope `project`, ref = project ID, algorithm `or`); record `retentionID` | e2e |
| `retention_id` set | `GET /retentions/{id}` first and require `scope.level == project` and `scope.ref == status.projectID`. On a mismatch: `Conflict` with a message, nothing written. Otherwise `PUT` the full desired policy when it differs. | e2e |
| `schedule` doesn't parse | `InvalidSpec` (parse with `github.com/robfig/cron/v3` using a 6-field parser with seconds; never a hand-written regex) | unit |
| Deleted, Delete | `PUT` the policy with zero rules and an empty schedule. Harbor accepts an empty rule list (checked on the test Harbor, chart 1.19.4), so the policy is never deleted with `DELETE /retentions/{id}`. | e2e |

Mapping: rules use `action: retain`; the template parameter key equals the template name; selectors use `kind: doublestar`; `untagged` goes in the tag selector's `extras` JSON (built with `encoding/json`); an empty `schedule` is trigger `kind: Schedule` with `settings.cron: ""`. Rules are compared as an ordered list (Harbor keeps order); the cron as a string. CEL: `count` is forbidden with `always` and required and greater than 0 otherwise.

### 2.7 HarborImmutabilityPolicy

| Situation | Response | Test |
|---|---|---|
| Rule list differs | Match spec rules to Harbor rules by `(repositories, tags)` selectors. **Create new rules first, then update in place** (only `disabled` can differ on a match), **then delete** rules not in the spec, so tags are never briefly unprotected. Rules are written with `action: immutable`, `template: immutable_template`. | e2e |
| Deleted, Delete | delete all of the project's immutability rules | e2e |

CEL: `maxItems: 15`, unique `(repositories, tags)` pairs. Immutability selectors use their own type without `untagged`.

### 2.8 HarborSystemRobot

Reuses the phase 1 robot rows for Secrets, rotation, expiry, `SecretConflict`, create-pending and replace. Differences:

| Situation | Response | Test |
|---|---|---|
| Namespace not admitted by the connection's `systemAllowedNamespaces` | `NotAllowed` | integration |
| The robot's Harbor name equals the `username` in the credentials of any connection with the same Harbor identity | `Conflict` reason `OperatorCredential`: never adopt, update, refresh, replace or delete it | integration |
| A project named in `projectPermissions` doesn't exist (`GET /projects/{name}` with `X-Is-Resource-Name`) | `Ready=False, ProjectNotFound` naming the missing projects; write nothing; requeue at the resync interval (the project may be created later). `InvalidSpec` stays for real 400s. | integration |
| Create | level `system`; one permission entry `{kind: system, namespace: /, access: systemPermissions}` plus one `{kind: project, namespace: <name or *>, access}` per `projectPermissions` entry | e2e |
| Permissions differ | compare as a set of `(kind, namespace, resource, action)`; update, or replace on 403 as in phase 1 | e2e |

Look up by name with `GET /robots?q=Level=system,name=<name>`. CEL: `systemPermissions` and `projectPermissions` together have at least one entry; system resources and actions come from a generated list (`docs/user/robot-permissions.md` gains a system section, generated from Harbor's `/permissions`); names match the phase 1 pattern.

**Replace safety (also for project robots).** Before the delete in a replace, check that the desired permissions are a subset of the connection's own (`GET /users/current/permissions`). If not, `Ready=False, Forbidden` naming the missing permissions, and delete nothing, so a robot is never deleted and then not re-created.

### 2.9 Watches (in addition to phase 1 section 3.6)

- Project reconciler: `HarborRegistry` through `spec.proxyCache.registryRef`.
- Registry reconciler: `HarborConnection`, its credentials `Secret` (index on `spec.credentialsSecretRef`), `Namespace`, `HarborProject` through `registryRef` (dependents), registries with the same `harborKey`.
- System robot reconciler: as project robots, plus `HarborConnection` and `HarborProject` creation (for `ProjectNotFound`).
- Connection reconciler: `HarborRegistry` and `HarborSystemRobot` through `spec.connectionRef` (dependents).
- Policy reconcilers: section 2.5.

### 2.10 RBAC note

The chart grants `*/status` on every kind only to the operator. The admin docs list all six kinds in the warning that granting status writes lets users take over Harbor objects.

## 3. Test fixtures

- A plain `registry:2`-compatible registry in the kind cluster as the proxy-cache upstream: `ghcr.io/distribution/distribution` (pinned by digest). Tests push a small image to it with `go-containerregistry` (`crane`) and pull it through Harbor.
- Image pushes for retention and immutability use `go-containerregistry` against Harbor with a robot from phase 1. No Docker daemon needed in tests.
- `github-ghcr` registry type is created but not pulled through in CI (no outbound dependency); AC-2-01 checks only that Harbor accepts it.
