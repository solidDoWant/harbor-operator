# harbor-operator specification

harbor-operator is a Kubernetes operator that configures an existing Harbor registry through custom resources. It does not install Harbor. It creates and keeps in shape Harbor projects (with their settings and storage quota), robot accounts, tag retention and tag immutability policies, and the upstream registry endpoints that proxy-cache projects pull through.

This file is the authoritative reference. Each phase is done when its checklist in `docs/acceptance/` passes (see "Phases").

## 1. Purpose and reference behaviour

**The owner's goal:** a Helm chart that sets up CI for a repository (namespace, Tekton objects) can also set up that repository's Harbor project and a robot account that CI pushes with, by including a few custom resources. The robot's credentials land in a Kubernetes Secret that a Tekton pipeline mounts directly. The owner can also declare pull-through caches (a Harbor proxy-cache project in front of ghcr.io, quay.io and so on) the same way.

**Reference behaviour.** Where this spec doesn't decide a behaviour, decide it in this order:
1. What Harbor's own UI and REST API (v2.0 API, Harbor 2.13 or later) do for the same action. The operator does what an admin clicking through the UI would do, nothing more.
2. What well-behaved, widely used operators do (crossplane providers, cert-manager, external-secrets): status conditions, finalizers, `observedGeneration`, events.
3. The simplest behaviour that keeps the principles.

Record the choice on the issue.

**Users of harbor-operator:**
- **Admins** install the operator with Helm, create the `HarborConnection` and the Harbor-wide resources, and decide which namespaces may use them.
- **Chart authors** (the owner's CI chart) create projects, robots and policies in an application's namespace.
- **The owner** is both.

## 2. Principles

Non-negotiable. Tests enforce them, and only the owner can change them.

1. **The custom resource is the source of truth.** For every field the spec sets, the operator makes Harbor match it, and puts back changes made in Harbor's UI or API (drift is overwritten). Fields the spec leaves unset are not touched.
2. **A namespace reaches only what it was allowed to.** A resource can use a connection, project or registry only if that object's `allowedNamespaces` admits the resource's namespace. No resource can act on a Harbor object owned by another custom resource.
3. **Secrets stay secret.** Robot secrets and connection credentials appear only in Kubernetes Secrets. They are never logged, never put in status, events or conditions, and never written to a namespace other than the one the spec names.
4. **Nothing is deleted by surprise.** The operator deletes a Harbor object only when the custom resource that owns it is deleted and its `deletionPolicy` is `Delete`. It never deletes an object it did not create or adopt.
5. **Done means the owner's checklist passes.** Nobody raises the bar beyond the checklist and principles 1–4.

## 3. Target environment

- Kubernetes 1.33 or later. No other operator is required.
- Harbor 2.13 or later, reached over HTTPS (or HTTP for tests), v2.0 REST API. The owner runs Harbor 2.15 with OIDC sign-in; the operator never uses OIDC.
- The operator authenticates to Harbor with HTTP basic auth: either a Harbor **system robot account** (the owner's choice, and the documented default) or a local admin user. The admin creates that first credential by hand. Harbor doesn't let a robot update robots or refresh their secrets, so with a robot credential those changes are made by deleting and re-creating the robot (`docs/design/phase-1.md`).

## 4. Architecture

One Go binary, `harbor-operator`, built with `controller-runtime` (kubebuilder layout). One controller per kind. Each reconcile:

1. Reads the custom resource and resolves its references (connection, project, registry), checking `allowedNamespaces`.
2. Builds a Harbor client for the connection (`github.com/goharbor/go-client`, the official generated client) with the connection's credentials. Clients are cached per connection and rebuilt when the credential Secret changes.
3. Finds the Harbor object: by the ID recorded in status, else by name. Creates it, adopts it, or reports a conflict (see "Ownership and adoption").
4. Compares the fields the spec sets with Harbor and updates what differs.
5. Writes status: `observedGeneration`, the Harbor ID, and conditions `Ready` and, when relevant, `Synced` / `ReferenceResolved`, with a reason and a short message.
6. Requeues after `spec.resyncInterval` or the connection's default (10 minutes) so UI drift is corrected without a watch on Harbor.

A finalizer on every kind handles deletion (see "Deletion").

Leader election is on; one active replica. Metrics through controller-runtime's Prometheus endpoint.

## 5. Custom resources

API group `harbor.soliddowant.dev`, version `v1alpha1`. **Every kind is namespaced.** Harbor is installed into a namespace and there may be more than one Harbor, so the Harbor-wide kinds normally live in the same namespace as the Harbor they configure, next to the credentials they use. Field names below are indicative; the phase design fixes them.

### Harbor-wide kinds

These normally live in the connection's namespace (see "Selecting across namespaces").

| Kind | Harbor object | Key spec fields |
|---|---|---|
| `HarborConnection` | none (a Harbor instance) | `url`, `credentialsSecretRef` (a Secret in the same namespace; keys `username`, `password`), `caBundle` / `insecureSkipVerify`, `allowedNamespaces` (who may create project-level resources with it), `systemAllowedNamespaces` (who may create Harbor-wide resources with it), `allowAdoption`, `defaults` (resync interval, deletion policy) |
| `HarborRegistry` | registry endpoint (`/registries`) | `connectionRef`, `name`, `type` (`github-ghcr`, `quay`, `docker-hub`, `docker-registry`, `harbor` and the other types Harbor lists), `url`, optional `credentialsSecretRef` (same namespace), `insecure`, `allowedNamespaces` (whose projects may use it as a proxy-cache upstream), `deletionPolicy`, `adopt` |
| `HarborSystemRobot` | system-level robot account | `connectionRef`, `name`, `description`, `duration` (days, `-1` = never expires), `permissions` (system resources, plus per-project access to listed projects or `*`), `secret` (see "Robot secrets"; written to the robot's own namespace), `deletionPolicy`, `adopt` |

### Project-level kinds

| Kind | Harbor object | Key spec fields |
|---|---|---|
| `HarborProject` | project | `connectionRef`, `name` (defaults to `metadata.name`), `public`, `storageQuota` (resource quantity) or `unlimitedStorage`, `vulnerability` (`preventVulnerableImages`, `severity`, `scanOnPush`, `cveAllowlist` with `items` and `expiresAt`, or `useSystemAllowlist`), `proxyCache` (`registryRef`, optional `speedLimitKbps`), `allowedNamespaces`, `deletionPolicy`, `adopt` |
| `HarborProjectRobot` | project-level robot account | `projectRef`, `name`, `description`, `duration`, `permissions` (resource and action pairs, for example `repository: push, pull`), `disabled`, `secret`, `deletionPolicy`, `adopt` |
| `HarborRetentionPolicy` | the project's tag retention policy | `projectRef`, `rules` (up to 15, Harbor's templates: `latestPushedK`, `latestPulledN`, `nDaysSinceLastPush`, `nDaysSinceLastPull`, `always`; with repository and tag selectors), `schedule` (cron, or none) |
| `HarborImmutabilityPolicy` | the project's tag immutability rules | `projectRef`, `rules` (repository and tag selectors, `disabled`) |

Every reference (`connectionRef`, `projectRef`, `registryRef`) is `{namespace, name}`, with `namespace` defaulting to the referring resource's own namespace.

A project has at most one retention policy and one immutability policy custom resource; a second one gets `Ready=False, reason=Conflict` and changes nothing. Each policy resource owns **all** of that project's retention or immutability rules: rules made in the UI are removed.

### Selecting across namespaces

Every object other resources point at (`HarborConnection`, `HarborRegistry`, `HarborProject`) has `allowedNamespaces`:

```yaml
allowedNamespaces:
  names:            # explicit list
    - ci-foo
  selector:         # metav1.LabelSelector on Namespace labels
    matchLabels:
      harbor.soliddowant.dev/ci: "true"
```

A namespace is admitted when it is the object's own namespace, is in `names`, or matches `selector`. The object's own namespace is always admitted. The referring side always names the target explicitly (`projectRef: {namespace, name}`), so both sides must agree.

`HarborConnection` has two lists, because Harbor-wide objects are more powerful than project ones:
- `allowedNamespaces` admits `HarborProject` (and, through projects, robots and policies).
- `systemAllowedNamespaces` admits `HarborRegistry` and `HarborSystemRobot`. Usually left empty, so only the connection's own namespace (where Harbor and its admin live) can create them.

A reference that isn't admitted gets `Ready=False, reason=NotAllowed`, and the operator does nothing in Harbor for it. When admission is withdrawn later, the referring resource turns `NotAllowed` and stops being reconciled; existing Harbor objects are left as they are until it is deleted.

### Robot secrets

```yaml
secret:
  dockerConfigJSON:
    enabled: true        # default true
    name: ""             # default <cr name>-dockerconfig
  opaque:
    enabled: false       # default false
    name: ""             # default <cr name>-credentials; keys: username, password, registry
```

The dockerconfigjson Secret holds an auth entry for the connection's registry host. Secrets are always written to the robot resources own namespace, carry an owner reference to it, and are removed when it is deleted or when a type is disabled. Harbor returns a robot's secret only when it is created or refreshed: if the Kubernetes Secret is missing or doesn't match a stored hash, the operator refreshes the robot's secret in Harbor and rewrites the Secret. `spec.secret.rotate` (a string; changing it forces a refresh) supports manual rotation.

## 6. Ownership and adoption

- **Harbor identity.** Each connection records its Harbor's `external_url` from `GET /systeminfo` in status (normalized: scheme, lower-cased host, default port removed, no trailing slash). Two connections to the same Harbor share it, whatever URL each uses to reach it. Each resource records `status.harborKey` (Harbor identity + object type + Harbor name) when it starts claiming an object.
- **One owner per Harbor object.** The owner is the claimant whose status records the object's ID for this Harbor identity. If no claimant records an ID, the owner is the **oldest admitted** claimant (creation time, then UID). A claimant that isn't admitted, or is being deleted without an ID, doesn't count. Only the owner creates, updates or deletes the object. Every other claimant gets `Ready=False, reason=Conflict` on every reconcile, even if its own status has an ID.
- **Adoption.** On create, if an object with that name already exists in Harbor and this resource doesn't own it by the rule above: with `adopt: true` (and the connection's `allowAdoption`) the operator takes it over and from then on overwrites drift; otherwise `Ready=False, reason=AlreadyExists`. An object the operator created itself just before a crash is not "existing". The proof is a marker the operator writes to the resource's status, which namespace users can't write, checked against the object's creation time in Harbor (`docs/design/phase-1.md`).
- **Names.** Robot names come from Harbor's responses, never from text: Harbor adds its configured prefix (`robot$` by default) to `<project>+<name>` for project robots and `<name>` for system robots.

## 7. Deletion

- `deletionPolicy: Delete` (default, overridable by the connection's `defaults`) deletes the Harbor object when the custom resource is deleted; `Orphan` leaves it. Either way the operator only ever deletes by the ID in status, after checking that ID still names this resource's own object; with no ID it deletes nothing.
- A resource whose reference is no longer admitted is orphaned when deleted: the finalizer goes and Harbor is untouched.
- Harbor refuses to delete a project that still has repositories (HTTP 412). The operator reports `reason=DeleteBlocked` with Harbor's message and retries with backoff; it never deletes repositories itself. A 404 on delete counts as deleted.
- Deleting a `HarborProject` (either policy) waits while robot or policy resources that the project admits still point at it (`reason=DependentsExist`). Resources it doesn't admit never hold it up. The same goes for a `HarborConnection` and the resources it admits.
- If Harbor is unreachable or the connection's credentials are gone, deletion waits (finalizer kept) and retries. Admins delete the resources before deleting their namespace; removing a finalizer by hand gives up, and is documented, not automated.

## 8. Errors and status

- Harbor errors map to condition reasons: `Unauthorized` (401), `Forbidden` (403), `NotFound`, `Conflict`, `InvalidSpec` (Harbor 400 with its message), `DeleteBlocked` (412 on project or registry delete), `HarborUnavailable` (network, 5xx), and the reference reasons `NotAllowed`, `ProjectNotReady`, `RegistryNotReady`, `RegistryOnOtherHarbor`, `ProjectNotFound`, `OperatorCredential` (phase designs). Invalid specs that can be caught statically are rejected by CRD validation (CEL), not at reconcile.
- Transient errors retry with exponential backoff per resource, from 5 seconds up to 5 minutes; `InvalidSpec` waits for a spec change.
- While Harbor is unreachable, the connection probes it on that backoff, and the resources that use the connection report `HarborUnavailable` without calling Harbor. A resource that finds Harbor unavailable first has its connection checked at once. When the connection is ready again, its resources reconcile at once.
- Every status change also emits a Kubernetes event on the resource. Messages never include secrets.

## 9. Security model

- The operator's Kubernetes RBAC: read and watch Secrets (credentials), write Secrets (robot output), read Namespaces (for label selectors). The chart grants this cluster-wide by default; an admin can narrow it with `watchNamespaces`. The operator only ever reads a credential Secret from the namespace of the resource that names it, and only writes robot Secrets into the robot resource's own namespace.
- A namespace user who can create `HarborProject` or `HarborProjectRobot` objects can do in Harbor only what an admitting connection and project allow. A project robot only ever gets permissions on its own project.
- Harbor-wide kinds (`HarborRegistry`, `HarborSystemRobot`) are admitted only through `systemAllowedNamespaces`, by default only the connection's own namespace.
- Namespace users never get write access to the custom resources' `status` subresource. The operator trusts status as its own record, for example as proof that it created an object.
- Adoption gives a namespace control of an existing Harbor object. The `HarborConnection` field `allowAdoption` (default `false`) must be true before any resource using that connection may adopt.
- Two connections can point at the same Harbor (for example in different namespaces). Ownership and conflicts are decided per Harbor URL, so they can't be used to take over each other's objects.

## 10. Won't do

- Installing, upgrading or backing up Harbor itself.
- Replication policies and replication rules (registries exist only for proxy-cache projects).
- Project members, user groups, LDAP/OIDC configuration, users.
- Webhooks, P2P preheat, scanners and scanner configuration, tag/artifact management, labels, garbage-collection schedules, system settings.
- Watching Harbor for changes (drift is corrected on the periodic resync).
- Harbor versions before 2.13, or the v1 API.
- Hand-written HTTP clients for Harbor: use `goharbor/go-client`; if it lacks an endpoint, raise a question.
- Hand-written cryptography (the only crypto here is TLS and hashing the robot secret, both from the standard library).
- Formal verification, or fuzz or simulation runs that define done.

## 11. Phases

Each phase ends with a release an admin can deploy: image `ghcr.io/soliddowant/harbor-operator:<version>`, chart `oci://ghcr.io/soliddowant/charts/harbor-operator`. The owner deploys it in their own time; deploying never blocks a phase.

1. **Projects and robots** (`docs/acceptance/phase-1.md`). `HarborConnection`, `HarborProject` (settings, quota, vulnerability settings), `HarborProjectRobot` with Secrets, cross-namespace selection, ownership, adoption, deletion, drift correction, image and chart. This is everything the CI chart needs.
2. **Policies, proxy caches and system robots** (`docs/acceptance/phase-2.md`). `HarborRegistry` and proxy-cache projects, `HarborRetentionPolicy`, `HarborImmutabilityPolicy`, `HarborSystemRobot`.
3. **Operations and documentation** (`docs/acceptance/phase-3.md`). Metrics, events, a Harbor-version test matrix, chart polish (`watchNamespaces`, network policy, PodDisruptionBudget), full admin docs with worked examples for a CI chart.

Each phase's design (`docs/design/phase-N.md`, with its reconcile table) is reviewed before its issues are scheduled.

## 12. Testing

- **Unit:** Go `testing` + `testify`, table-driven. The comparison and mapping code (spec to Harbor model and back) is unit-tested.
- **Integration** (`//go:build integration`): envtest (a local API server) and a real Harbor. Controllers run in-process against both.
- **e2e** (`//go:build e2e`): kind, Harbor installed from its Helm chart, the operator installed from this repo's chart with a locally built image. Tests apply custom resources and check Harbor through its API and Kubernetes through the client.
- **Harbor for tests** runs from Harbor's images on `ghcr.io/goharbor` (never Docker Hub), chart from the `oci://ghcr.io/home-operations/charts-mirror/harbor` mirror or the upstream chart repository, with internal database and Redis. One Harbor per test run; tests isolate by unique project names.
- **Acceptance:** each checklist scenario lists its tests on a `Tests:` line. `make acceptance` prints passing scenarios out of the total.
- Tests never depend on wall-clock speed. Wait on conditions with deadlines. Timing-sensitive tests pass `-count=50 -race`.

## 13. Repository layout

```
cmd/harbor-operator/          main
api/v1alpha1/                 CRD Go types (controller-gen)
internal/controller/          one reconciler per kind
internal/harbor/              thin wrapper over goharbor/go-client: client cache, error mapping, model mapping
internal/access/              allowedNamespaces evaluation
internal/secrets/             robot Secret rendering
config/crd/bases/             generated CRDs (the chart includes them)
deploy/charts/harbor-operator Helm chart
test/                         e2e harness, Harbor install manifests, fixtures
docs/                         user/ admin/ dev/ agents/ design/ acceptance/
scripts/                      dev and agent scripts
```

## 14. Documentation plan

| Audience | Knows | Needs | Pages |
|---|---|---|---|
| Chart authors (users) | Kubernetes YAML, Helm | Which resources to write for a project, a robot, a cache; what each status means | `docs/user/` |
| Admins | Kubernetes, Helm, Flux, Harbor's UI | Install, create the system robot and connection, allow namespaces, security limits, troubleshooting | `docs/admin/` (CRD and chart value references generated) |
| Developers | Go, controller-runtime | Architecture, packages, test setup, releasing | `docs/dev/` |
| Agents | This repo | Playbooks | `docs/agents/`, `.claude/`, `CLAUDE.md` |

## 15. Open questions

1. ~~Can a Harbor system robot manage robot accounts?~~ Answered on Harbor 2.15.4: it can create project and system robots that hold only permissions it holds itself (`403` otherwise), and delete them, but it can't update robots or refresh their secrets. The system robot stays the default credential: the operator replaces a robot (delete and create) when Harbor refuses an update or refresh (`docs/design/phase-1.md`), but only when the connection holds every permission the robot asks for; otherwise the robot shows `Forbidden` and nothing is deleted (`docs/design/phase-2.md`). A local admin user also works and updates robots in place ([`docs/admin/credentials.md`](docs/admin/credentials.md)).
2. Does `goharbor/go-client` cover every endpoint the phases need (quotas, retentions, immutable rules, robot secret refresh, registries)? Missing endpoints are raised as questions, not hand-written.
