# Phase 1 design: connections, projects and project robots

Decides the behaviour of phase 1. `SPEC.md` gives the model; this page fixes the details workers need. A case this page and `SPEC.md` don't decide is a design question: hand it back.

## 1. Packages

| Package | Job |
|---|---|
| `api/v1alpha1` | `HarborConnection`, `HarborProject`, `HarborProjectRobot`, shared types (`AllowedNamespaces`, `ObjectReference` (`namespace` defaults to the referrer's own, `name`), `RobotSecretSpec`, `DeletionPolicy`). CEL validation on the types. |
| `internal/harbor` | `ClientCache` keyed by connection UID + `metadata.generation` + credential Secret resourceVersion; typed methods the controllers need (`GetProjectByName`, `CreateProject`, `UpdateProjectMetadata`, `SetProjectQuota`, `GetRobot`, `CreateRobot`, `UpdateRobot`, `RefreshRobotSecret`, `DeleteRobot`, ...) over `github.com/goharbor/go-client/pkg/sdk/v2.0`; `MapError(err) (reason string, retry bool)`. |
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
    RegistryHost         string                 `json:"registryHost,omitempty"` // host[:port] for docker configs; default from external_url
    AllowedNamespaces    AllowedNamespaces      `json:"allowedNamespaces,omitempty"`       // for HarborProject
    SystemAllowedNamespaces AllowedNamespaces   `json:"systemAllowedNamespaces,omitempty"` // for Harbor-wide kinds (phase 2)
    AllowAdoption        bool                   `json:"allowAdoption,omitempty"`
    Defaults             ConnectionDefaults     `json:"defaults,omitempty"` // resyncInterval (default 10m), deletionPolicy (default Delete)
}
// status: conditions Ready; harborVersion, harborIdentity (normalized external_url), registryHost; observedGeneration

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
// status: conditions Ready, ReferenceResolved; projectID; harborKey; observedGeneration

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
// status: conditions Ready, ReferenceResolved; robotID; harborKey; harborName (as Harbor returned it); observedRotate; lastRotated; expiresAt. The secret hash lives in an annotation on each Secret, never in status.
```

`resource`/`action` values are Harbor's (for example `repository`/`push`, `repository`/`pull`, `artifact`/`delete`, `tag`/`create`). CEL validates them against the list in `docs/user/robot-permissions.md`, which a worker generates from Harbor's `/permissions` endpoint once and commits.

## 3. Reconcile tables

Amended after the design review on issue #4. The **Test** column names the covering test type; workers add the concrete test to the checklist `Tests:` line. Each row below is one observable situation and the operator's single response.

### 3.1 Preambles

**Normal reconcile:** (a) add the finalizer if missing; (b) resolve references and check admission; (c) get a Harbor client; (d) check ownership (section 3.5). A failure in (b), (c) or (d) sets `Ready=False` with the reason, does nothing in Harbor, and requeues at the resync interval (watches, section 3.6, usually bring it back sooner).

**Deletion** (runs instead, before (b) and (c)):
1. No Harbor ID in status → remove the finalizer, touch nothing.
2. `deletionPolicy: Orphan` → (projects: wait for admitted dependents first, row below) remove the finalizer, touch nothing in Harbor.
3. The reference is not admitted any more → remove the finalizer, touch nothing in Harbor, emit a `NotAllowed` event.
4. The connection can't give a client (credentials missing or invalid, Harbor down) → keep the finalizer, `Ready=False` with the connection's reason, back off.
5. Otherwise: check the ID still names this resource's own object (section 3.4). If it doesn't, delete nothing and remove the finalizer. If it does, delete **by ID**. A 404 counts as deleted.

### 3.2 HarborConnection

| Situation | Response | Test |
|---|---|---|
| Credential Secret (own namespace) missing or lacks `username`/`password` | `Ready=False, CredentialsInvalid`; requeue on Secret change (watch) | integration |
| `GET /systeminfo` and `GET /users/current/permissions` succeed | `Ready=True`; record `status.harborVersion` and `status.harborIdentity` (normalized `external_url`, SPEC section 6) | integration |
| `GET /users/current/permissions` returns 401 (Harbor treats wrong basic-auth credentials as anonymous; this call refuses anonymous) | `Ready=False, Unauthorized` | integration |
| Network error / 5xx | `Ready=False, HarborUnavailable`, backoff | unit (error mapping) |
| Credential Secret or connection spec changes | client cache entry replaced (key: UID + `metadata.generation` + Secret resourceVersion); dependents re-queued | integration |
| Deleted while resources it admits reference it | keep finalizer, `Ready=False, DependentsExist`, list up to 5 dependents in the message. References from namespaces it doesn't admit are ignored. | integration |

`status.registryHost`: `spec.registryHost` if set, else the host (and non-default port) of `harborIdentity`. The robots' docker config uses it.

### 3.3 HarborProject

| Situation | Response | Test |
|---|---|---|
| Connection does not admit the project's namespace (own namespace always admitted) | `Ready=False, NotAllowed`; nothing in Harbor | integration |
| Not the owner (section 3.5) | `Ready=False, Conflict` on every reconcile, even with an ID in status | integration |
| Owner, no ID, no project of that name in Harbor | set `status.createPending` (section 3.7), create, record ID, clear `status.createPending` | e2e |
| Owner, no ID, project exists, `status.createPending` set and Harbor's `creation_time` is not earlier than it (section 3.7) | it is this resource's own: record the ID, clear `status.createPending` | integration |
| Owner, no ID, project exists, `adopt: false` | `Ready=False, AlreadyExists` | e2e |
| ... `adopt: true`, connection `allowAdoption: false` | `Ready=False, AdoptionNotAllowed` | integration |
| ... `adopt: true`, allowed | record the ID, `Adopted` event, then the update path | e2e |
| ID set, `GET /projects/{id}` returns 404, or returns a project with another name, or `status.harborKey` names another Harbor identity | the ID is stale: clear it and take the "no ID" rows (a 404 also emits `Recreated` once created) | e2e |
| A set field differs in Harbor (UI change) | update that field only (mapping in section 3.8) | e2e |
| A spec field is unset | never written; Harbor's value left | unit |
| `storageQuota` set | `GET /quotas?reference=project&reference_id=<id>`, then `PUT /quotas/{id}` with `hard.storage` in bytes; `unlimitedStorage` writes -1 | e2e |
| Harbor 400 on update | `Ready=False, InvalidSpec` with Harbor's message; no retry until the generation changes | integration |
| Deleted (either policy), robot or policy resources that this project admits still reference it | wait, `DependentsExist`. Referencing resources it doesn't admit are ignored. | integration |
| Deleted, Delete, Harbor answers 412 (has repositories) | wait, `DeleteBlocked` with Harbor's message, backoff (max 10 min) | e2e |
| Deleted, Delete, empty | delete by ID, remove finalizer | e2e |
| Deleted, Orphan | remove finalizer once dependents are gone | e2e |

Look up a project by name with `GET /projects/{name}` and header `X-Is-Resource-Name: true` (go-client `GetProjectParams.XIsResourceName`), never `?name=` (fuzzy match).

### 3.4 HarborProjectRobot

The robot waits for its project to have `status.projectID` and not be in `Conflict` or `NotAllowed`; it does **not** wait for the project's `Ready` (an unrelated `InvalidSpec` on the project mustn't stop robots).

| Situation | Response | Test |
|---|---|---|
| Project resource missing, has no ID, or is `Conflict`/`NotAllowed` | `Ready=False, ProjectNotReady` | integration |
| Project doesn't admit the robot's namespace | `Ready=False, NotAllowed` | integration |
| Not the owner (section 3.5) | `Ready=False, Conflict` | integration |
| Owner, no ID, no robot of that name (`GET /robots?q=Level=project,ProjectID=<id>,name=<project>+<name>`) | set `status.createPending`, create (level `project`, one permission entry `{kind: project, namespace: <project name>, access: [...]}`), record ID and `status.harborName` from Harbor's response, write Secrets, clear `status.createPending` | e2e |
| Owner, no ID, robot exists, `status.createPending` set and the robot's `creation_time` is not earlier than it | own robot: record the ID, then refresh its secret (the secret was lost with the crash) | integration |
| Owner, no ID, robot exists, not created by us | `AlreadyExists` / `AdoptionNotAllowed` / adopt as for projects; on adoption refresh the secret immediately | e2e |
| ID set, `GET /robots/{id}` returns 404, or a robot whose name isn't this one's or whose project isn't this project | stale ID: clear it and take the "no ID" rows (`Recreated` event on 404) | e2e |
| Description, duration, disabled or permissions differ (permissions compared as a set of `(resource, action)`) | `PUT /robots/{id}` with the GET result with those fields changed. If Harbor answers 403 (robot credentials can't update robots), **replace**: set `status.createPending`, delete by ID, create, record the new ID, write Secrets, `Replaced` event | e2e |
| An enabled Secret is missing or renamed, and another Secret this robot owns has data matching its hash annotation | copy the credentials from it; write the new Secret before deleting the old one. No refresh. | integration |
| No Secret this robot owns has matching data, or the secret-refresh-pending annotation is set | refresh-pending annotation, `PATCH /robots/{id}` with an empty secret (Harbor generates one), write every enabled Secret, clear the annotation. On 403, replace as above | e2e |
| `secret.rotate` differs from `status.observedRotate` | refresh as above, record `status.observedRotate` and `status.lastRotated`. On create, record the current value without refreshing | e2e |
| A secret type is disabled | delete that Secret | integration |
| The target Secret name exists without a controller owner reference to this robot's UID | `Ready=False, SecretConflict`; never overwrite or delete it | integration |
| `expires_at` is in the past (duration > 0) | `Ready=False, Expired`; do nothing else | unit |
| Deleted | deletion preamble; Secrets always go by owner reference, for either policy | e2e |

**Docker config content.** `{"auths": {"<status.registryHost of the connection>": {"username": "<status.harborName>", "password": "<secret>", "auth": base64(user:pass)}}}`. The opaque Secret has keys `username`, `password`, `registry`.

### 3.5 Ownership

Before any write (and before create), list the claimants of `status.harborKey` (field index on `status.harborKey`; a resource sets it as soon as it has resolved its connection and is admitted). The owner is the claimant whose status has the object's ID for this Harbor identity; otherwise the oldest admitted claimant (creation time, then UID). Claimants that aren't admitted, or are being deleted without an ID, don't count. The same rule covers projects and robots (robot key: Harbor identity + project name + robot name).

### 3.6 Watches

- Project reconciler: `HarborConnection` (mapped through a `spec.connectionRef` index), `Namespace` (label changes re-queue projects in that namespace and projects referring to connections whose selector may now match), other projects with the same `status.harborKey` (so a `Conflict` loser re-checks when the owner goes away).
- Robot reconciler: `HarborProject` (through a `spec.projectRef` index), `Namespace`, owned `Secret`s, and robots with the same `status.harborKey`.
- Connection reconciler: its credential `Secret`.

### 3.7 Pending markers

Proof that an existing Harbor object is this resource's own interrupted create must be something a namespace user can't forge. Users can write a resource's metadata and spec, but not its status subresource: the chart grants `*/status` only to the operator's own ClusterRole, and the admin docs say that granting `*/status` to anyone else lets them take over Harbor objects.

- **`status.createPending`** (RFC 3339 time, written through the `/status` subresource) is set before a create call and cleared once the ID is in status. An existing Harbor object found with no ID in status counts as this resource's own only if `status.createPending` is set **and** the object's Harbor `creation_time` is no earlier than `status.createPending` minus 2 minutes (clock skew between Harbor and the API server). Anything else is an existing object, and the adoption rules apply. When Harbor refuses the create call with a 4xx (for example 403 or 409), or refuses the delete that comes first in a robot replace, nothing was created, so `status.createPending` is cleared in the same status write that records the failure. After a network error, a timeout or a 5xx the result is unknown, and the marker stays.
- **`harbor.soliddowant.dev/secret-refresh-pending`** (annotation) is set before a robot secret refresh and cleared once every enabled Secret is written. If it is present at the start of a reconcile, the refresh is done again. It can stay an annotation: forging it only makes the operator renew the secret of a robot the resource already owns.
- The operator ignores a `harbor.soliddowant.dev/create-pending` annotation (an earlier design used one) and removes it when it sees it.
- Both interrupted steps are safe to repeat.

### 3.8 Field mapping

All project metadata values are strings and are written with `PUT /projects/{id}` (Harbor merges keys; it ignores the top-level `public` on update).

| Spec | Harbor metadata key | Value |
|---|---|---|
| `public` | `public` | `"true"` / `"false"` |
| `vulnerability.preventVulnerableImages` | `prevent_vul` | `"true"` / `"false"` |
| `vulnerability.severity` | `severity` | lower case |
| `vulnerability.scanOnPush` | `auto_scan` | `"true"` / `"false"` |
| `vulnerability.useSystemAllowlist` | `reuse_sys_cve_allowlist` | `"true"` / `"false"`; written as `"false"` whenever `cveAllowlist` is set |
| `vulnerability.cveAllowlist` | `cve_allowlist` (object) | `items[].cve_id` replaces Harbor's whole list; `expires_at` Unix seconds, omitted = never |

### 3.9 Safety rules for `internal/harbor`

- Never return go-client response values, and never format them into errors or logs (their `Error()` methods print the payload, including robot secrets). Return typed results and a typed error holding only the status code and Harbor's message.
- Set go-openapi's `runtime.Debug = false` explicitly, whatever `DEBUG`/`SWAGGER_DEBUG` say.

### 3.10 CEL rules

- `spec.name` (effective: `spec.name` or `metadata.name`), `connectionRef` and `projectRef` (namespace defaulted to `metadata.namespace`) are immutable, by a root-level rule comparing the effective values of `oldSelf` and `self`.
- Project and robot names match `^[a-z0-9]+(?:[._-][a-z0-9]+)*$`.
- `duration` is `-1` or 1..2147483646. `storageQuota` is greater than 0.

### 3.11 Concurrency

Each reconcile reads current Harbor state before writing. Two resources never write one Harbor object, because only the owner (section 3.5) writes. Controllers run with `MaxConcurrentReconciles: 4`; a single object never reconciles concurrently with itself.

## 4. Chart and image

- Image built with `ko` or a distroless Dockerfile (`gcr.io/distroless/static` is on gcr.io, allowed) for amd64 and arm64, pushed to `ghcr.io/soliddowant/harbor-operator`.
- Chart `deploy/charts/harbor-operator`: CRDs (in `crds/` and a `templates/crds` toggle for upgrades), Deployment (1 replica, leader election), ServiceAccount, ClusterRole, metrics Service. Memory request equals limit; no CPU limit (owner's cluster rule). Values reference generated into `docs/admin/chart-values.md`.
- Release workflow on tag `v*`: image, chart (OCI to `ghcr.io/soliddowant/charts`), GitHub release with the changelog as notes.

## 5. Test harness

- `make test-integration`: envtest plus one Harbor started by `test/harbor/up.sh` in kind (shared by the run), operator in-process.
- `make test-e2e`: kind cluster, Harbor chart (images from `ghcr.io/goharbor`), operator chart with the local image loaded by `kind load`. Agents name clusters `harbor-op-<issue>`.
- One Harbor per run; tests use unique names (`t.Name()` hash) and clean up.
- CI runs integration and e2e as separate parallel jobs, each creating its own kind cluster.
