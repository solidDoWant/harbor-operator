# Phase 1 design: connections, projects and project robots

Decides the behaviour of phase 1. `SPEC.md` gives the model; this page fixes the details workers need. A case this page and `SPEC.md` don't decide is a design question: hand it back.

## 1. Packages

| Package | Job |
|---|---|
| `api/v1alpha1` | `HarborConnection`, `HarborProject`, `HarborProjectRobot`, shared types (`AllowedNamespaces`, `ObjectReference` (`namespace` defaults to the referrer's own, `name`), `RobotSecretSpec`, `DeletionPolicy`). CEL validation on the types. |
| `internal/harbor` | `ClientCache` keyed by connection UID + credential Secret resourceVersion; typed methods the controllers need (`GetProjectByName`, `CreateProject`, `UpdateProjectMetadata`, `SetProjectQuota`, `GetRobot`, `CreateRobot`, `UpdateRobot`, `RefreshRobotSecret`, `DeleteRobot`, ...) over `github.com/goharbor/go-client/pkg/sdk/v2.0`; `MapError(err) (reason string, retry bool)`. |
| `internal/access` | `Admits(ctx, client, allowed AllowedNamespaces, ownerNamespace, candidate string) (bool, error)`. The owner's own namespace is always admitted. |
| `internal/ownership` | Decides the winner among custom resources claiming the same Harbor object (oldest creationTimestamp, then UID). Keyed by the connection's normalized URL (scheme, lower-cased host, port, no trailing slash) plus the Harbor object name, so two connections to the same Harbor share one ownership space. Uses field indexes. |
| `internal/secrets` | Renders the dockerconfigjson and opaque Secrets; hashes the robot secret (SHA-256, standard library) into an annotation. |
| `internal/controller` | `HarborConnectionReconciler`, `HarborProjectReconciler`, `HarborProjectRobotReconciler`. |

## 2. Types (phase 1 fields)

```go
type AllowedNamespaces struct {
    Names    []string              `json:"names,omitempty"`
    Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// HarborConnection (namespaced)
type HarborConnectionSpec struct {
    URL                  string                 `json:"url"`          // https://harbor.example.com, no path
    CredentialsSecretRef LocalSecretReference   `json:"credentialsSecretRef"` // same namespace; keys username, password
    CABundle             string                 `json:"caBundle,omitempty"`   // PEM
    InsecureSkipVerify   bool                   `json:"insecureSkipVerify,omitempty"`
    AllowedNamespaces    AllowedNamespaces      `json:"allowedNamespaces,omitempty"`       // for HarborProject
    SystemAllowedNamespaces AllowedNamespaces   `json:"systemAllowedNamespaces,omitempty"` // for Harbor-wide kinds (phase 2)
    AllowAdoption        bool                   `json:"allowAdoption,omitempty"`
    Defaults             ConnectionDefaults     `json:"defaults,omitempty"` // resyncInterval (default 10m), deletionPolicy (default Delete)
}
// status: conditions Ready; harborVersion (from /systeminfo); observedGeneration

// HarborProject (namespaced)
type HarborProjectSpec struct {
    ConnectionRef     ObjectReference      `json:"connectionRef"`
    Name              string               `json:"name,omitempty"` // default metadata.name; immutable (CEL)
    Public            *bool                `json:"public,omitempty"`
    StorageQuota      *resource.Quantity   `json:"storageQuota,omitempty"` // unset = untouched; "-1" not allowed, use unlimited: true
    UnlimitedStorage  bool                 `json:"unlimitedStorage,omitempty"` // CEL: not both
    Vulnerability     *VulnerabilitySpec   `json:"vulnerability,omitempty"`
    AllowedNamespaces AllowedNamespaces    `json:"allowedNamespaces,omitempty"`
    DeletionPolicy    DeletionPolicy       `json:"deletionPolicy,omitempty"` // "" = connection default
    Adopt             bool                 `json:"adopt,omitempty"`
    ResyncInterval    *metav1.Duration     `json:"resyncInterval,omitempty"`
}
type VulnerabilitySpec struct {
    PreventVulnerableImages *bool    `json:"preventVulnerableImages,omitempty"`
    Severity                string   `json:"severity,omitempty"` // none|low|medium|high|critical (Harbor's values)
    ScanOnPush              *bool    `json:"scanOnPush,omitempty"`
    UseSystemAllowlist      *bool    `json:"useSystemAllowlist,omitempty"`
    CVEAllowlist            *CVEAllowlist `json:"cveAllowlist,omitempty"` // items []string (CVE IDs), expiresAt *metav1.Time; CEL: not with useSystemAllowlist=true
}
// status: conditions Ready, ReferenceResolved; projectID; observedGeneration

// HarborProjectRobot (namespaced)
type HarborProjectRobotSpec struct {
    ProjectRef     ObjectReference       `json:"projectRef"`
    Name           string                `json:"name,omitempty"` // default metadata.name; immutable
    Description    string                `json:"description,omitempty"`
    Duration       int64                 `json:"duration,omitempty"` // days; -1 never (default -1)
    Permissions    []RobotAccess         `json:"permissions"`        // {resource, action}; minItems 1
    Disabled       bool                  `json:"disabled,omitempty"`
    Secret         RobotSecretSpec       `json:"secret,omitempty"`
    DeletionPolicy DeletionPolicy        `json:"deletionPolicy,omitempty"`
    Adopt          bool                  `json:"adopt,omitempty"`
    ResyncInterval *metav1.Duration      `json:"resyncInterval,omitempty"`
}
// status: conditions Ready, ReferenceResolved; robotID; harborName (robot$proj+name); secretHash annotation lives on the Secret, not status; expiresAt
```

`resource`/`action` values are Harbor's (for example `repository`/`push`, `repository`/`pull`, `artifact`/`delete`, `tag`/`create`). CEL validates them against the list in `docs/user/robot-permissions.md`, which a worker generates from Harbor's `/permissions` endpoint once and commits.

## 3. Reconcile tables

Every reconcile starts by: (a) add finalizer if missing; (b) resolve references and check admission; (c) get a Harbor client. Any failure in (b) or (c) sets `Ready=False` with the reason and stops. Each row below is one observable situation and the operator's single response. The **Test** column names the covering test type; workers add the concrete test to the checklist `Tests:` line.

### HarborConnection

| Situation | Response | Test |
|---|---|---|
| Credential Secret missing or lacks keys | `Ready=False, CredentialsInvalid`; requeue on Secret change (watch) | integration |
| `/systeminfo` and an authenticated call (`/users/current` for users, `/projects?page_size=1` for robots) succeed | `Ready=True`, record `harborVersion` | integration |
| 401 | `Ready=False, Unauthorized` | integration |
| Network error / 5xx | `Ready=False, HarborUnavailable`, backoff | unit (error mapping) |
| Credential Secret changes | client cache entry replaced; dependents re-queued | integration |
| Deleted while resources reference it | keep finalizer, `Ready=False, DependentsExist`, list up to 5 dependents in message | integration |

### HarborProject

| Situation | Response | Test |
|---|---|---|
| Connection does not admit the project's namespace (`allowedNamespaces`; own namespace always admitted) | `Ready=False, NotAllowed`; nothing in Harbor | integration |
| status.projectID unset, no project of that name in Harbor | create with spec fields; record ID | e2e |
| status.projectID unset, project exists, another CR owns it | `Ready=False, Conflict` | integration |
| status.projectID unset, project exists, not owned, `adopt: false` | `Ready=False, AlreadyExists` | e2e |
| ... `adopt: true`, connection `allowAdoption: false` | `Ready=False, AdoptionNotAllowed` | integration |
| ... `adopt: true`, allowed | record ID, then normal update path | e2e |
| status.projectID set, project gone from Harbor | clear ID, recreate (same as first row) and emit a `Recreated` event | e2e |
| A set field differs in Harbor (UI change) | update that field only | e2e |
| A spec field is unset | never written; UI value left | unit |
| `storageQuota` set | set the project's quota `hard.storage` in bytes via `/quotas`; `unlimitedStorage` writes -1 | e2e |
| Harbor 400 on update | `Ready=False, InvalidSpec` with Harbor's message; no retry until generation changes | integration |
| Deleted, policy Orphan | remove finalizer only | e2e |
| Deleted, policy Delete, robot CRs still reference it | wait, `DependentsExist` | integration |
| Deleted, policy Delete, has repositories | wait, `DeleteBlocked`, backoff (max 10 min) | e2e |
| Deleted, policy Delete, empty | delete in Harbor, remove finalizer | e2e |
| Deleted, adoption never succeeded / ID unset | remove finalizer, touch nothing | unit |

### HarborProjectRobot

| Situation | Response | Test |
|---|---|---|
| Project CR missing, not Ready, or doesn't admit robot's namespace | `Ready=False, ProjectNotReady` or `NotAllowed` | integration |
| No robot of that name in the project | create (level `project`, permissions on that project only); write Secrets from the returned secret | e2e |
| Robot exists, not owned, `adopt` rules | as for projects; on adoption the secret is unknown, so refresh it immediately | e2e |
| Permissions, description, duration or disabled differ | `PUT /robots/{id}` with the full desired object | e2e |
| A Secret is missing, or its hash annotation doesn't match the secret the operator last wrote | refresh the robot secret in Harbor (`PATCH /robots/{id}` with an empty secret → Harbor generates one), rewrite every enabled Secret | e2e |
| `secret.rotate` value changed | refresh, rewrite, record `status.lastRotated` and the rotate value | e2e |
| A secret type is disabled | delete that Secret | integration |
| Secret name changed | delete old, write new | integration |
| A Secret of the target name exists but isn't owned by this robot | `Ready=False, SecretConflict`; never overwrite | integration |
| Robot gone from Harbor | recreate, rewrite Secrets, `Recreated` event | e2e |
| Deleted, policy Delete | delete robot in Harbor; Secrets go by owner reference | e2e |
| Deleted, policy Orphan | Secrets are still removed (they're Kubernetes objects owned by the CR); Harbor robot kept | integration |

**Docker config content.** `{"auths": {"<registry host>": {"username": "robot$proj+name", "password": "<secret>", "auth": base64(user:pass)}}}`, where the registry host is the connection URL's host (with port, if any). The opaque Secret has keys `username`, `password`, `registry`.

**Concurrency.** Each reconcile reads current Harbor state before writing; the only multi-writer case is two CRs claiming one Harbor object, which ownership resolves before any write. Controllers run with `MaxConcurrentReconciles: 4`; a single object never reconciles concurrently with itself (controller-runtime guarantees).

## 4. Chart and image

- Image built with `ko` or a distroless Dockerfile (`gcr.io/distroless/static` is on gcr.io, allowed) for amd64 and arm64, pushed to `ghcr.io/soliddowant/harbor-operator`.
- Chart `deploy/charts/harbor-operator`: CRDs (in `crds/` and a `templates/crds` toggle for upgrades), Deployment (1 replica, leader election), ServiceAccount, ClusterRole, metrics Service. Memory request equals limit; no CPU limit (owner's cluster rule). Values reference generated into `docs/admin/chart-values.md`.
- Release workflow on tag `v*`: image, chart (OCI to `ghcr.io/soliddowant/charts`), GitHub release with the changelog as notes.

## 5. Test harness

- `make test-integration`: envtest plus one Harbor started by `test/harbor/up.sh` in kind (shared by the run), operator in-process.
- `make test-e2e`: kind cluster, Harbor chart (images from `ghcr.io/goharbor`), operator chart with the local image loaded by `kind load`. Agents name clusters `harbor-op-<issue>`.
- One Harbor per run; tests use unique names (`t.Name()` hash) and clean up.
- CI runs integration and e2e as separate parallel jobs, each creating its own kind cluster.
