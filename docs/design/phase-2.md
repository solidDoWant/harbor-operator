# Phase 2 design: registries, proxy caches, policies and system robots

Builds on `docs/design/phase-1.md`: the same reconcile preamble (finalizer, references and admission, client), ownership rules, adoption, deletion policies and error reasons apply to every kind here. Only differences are listed.

## 1. Types

```go
// HarborRegistry (namespaced; admitted by the connection's systemAllowedNamespaces)
type HarborRegistrySpec struct {
    ConnectionRef        ObjectReference       `json:"connectionRef"`
    Name                 string                `json:"name,omitempty"` // default metadata.name; immutable
    Type                 string                `json:"type"`           // Harbor adapter type; enum from Harbor's /replication/adapters at design time
    URL                  string                `json:"url"`
    CredentialsSecretRef *LocalSecretReference `json:"credentialsSecretRef,omitempty"` // keys accessKey, accessSecret
    Insecure             bool                  `json:"insecure,omitempty"`
    Description          string                `json:"description,omitempty"`
    AllowedNamespaces    AllowedNamespaces     `json:"allowedNamespaces,omitempty"` // whose HarborProjects may use it
    DeletionPolicy, Adopt, ResyncInterval      // as phase 1
}
// status: registryID, health (Harbor's status string), conditions

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

### HarborRegistry

| Situation | Response | Test |
|---|---|---|
| Namespace not in connection's `systemAllowedNamespaces` (own namespace always admitted) | `NotAllowed` | integration |
| Create / update | `POST`/`PUT /registries`; then `POST /registries/ping` with the ID and store the result in `status.health` (not a Ready gate: an unreachable upstream is `Ready=True` with `health=unhealthy` and a warning event) | e2e |
| Credentials Secret changes | watch triggers update; Harbor never returns the secret, so the operator stores a hash annotation on the resource's status (`status.credentialsHash`) and re-sends when it differs | integration |
| Deleted, a HarborProject proxy cache references it | `DependentsExist` | integration |

### HarborProject with proxyCache

| Situation | Response | Test |
|---|---|---|
| Registry missing, not Ready, or doesn't admit project's namespace | `ReferenceResolved=False`, `NotAllowed` or `RegistryNotReady`; nothing created | integration |
| Create | `POST /projects` with `registry_id` and metadata `proxy_speed_kb` | e2e |
| Adopting a project whose proxy-cache setting differs from the spec | `InvalidSpec` ("proxy-cache mode can't be changed"); nothing written | integration |

### HarborRetentionPolicy

| Situation | Response | Test |
|---|---|---|
| Project has no retention policy (`retention_id` metadata unset) | `POST /retentions` with scope project, algorithm `or` | e2e |
| Project has one | `PUT /retentions/{id}` with the full desired policy when it differs (compare normalized rules and trigger) | e2e |
| Another retention resource for the same Harbor project is older | `Conflict` | integration |
| Deleted, Delete | `PUT` the policy with zero rules and no schedule (Harbor keeps a policy object per project; empty is "none") | e2e |

### HarborImmutabilityPolicy

| Situation | Response | Test |
|---|---|---|
| Rule list differs | Compute a diff against `GET /projects/{p}/immutabletagrules` matched by (selectors) and create, update or delete individual rules so Harbor equals the spec | e2e |
| Deleted, Delete | delete all rules | e2e |

### HarborSystemRobot

Same table as `HarborProjectRobot`, with level `system`. Secrets go to the resource's own namespace. A project named in `projectPermissions` that doesn't exist in Harbor is `InvalidSpec` from Harbor; the operator reports it and retries on the resync (the project may be created later).

## 3. Test fixtures

- A plain `registry:2`-compatible registry in the kind cluster as the proxy-cache upstream: `ghcr.io/distribution/distribution` (pinned by digest). Tests push a small image to it with `go-containerregistry` (`crane`) and pull it through Harbor.
- Image pushes for retention and immutability use `go-containerregistry` against Harbor with a robot from phase 1. No Docker daemon needed in tests.
- `github-ghcr` registry type is created but not pulled through in CI (no outbound dependency); AC-2-01 checks only that Harbor accepts it.
