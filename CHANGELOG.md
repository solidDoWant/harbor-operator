# Changelog

## 0.4.1-rc.1

harbor-operator is a Kubernetes operator that configures an existing Harbor registry through custom resources. It does not install Harbor. This release is a candidate (a test version before the final release) for 0.4.1. It fixes how the operator replaces robots and which permissions it asks for, adds a command that prints the robot permissions it needs, and changes how the chart selects Grafana instances. To install or upgrade, run `helm upgrade --install harbor-operator oci://ghcr.io/soliddowant/charts/harbor-operator --version 0.4.1-rc.1 --namespace harbor-operator --create-namespace`.

### Upgrade notes

- If you enabled `grafanaDashboard.grafanaOperator.enabled` and relied on the default instance selector, set `grafanaDashboard.grafanaOperator.instanceSelector` yourself, for example to `matchLabels: {dashboards: grafana}`. The chart no longer has a default and fails to render without one. Your selector is also no longer merged with a default. ([#185](https://github.com/solidDoWant/harbor-operator/pull/185))
- The `GrafanaDashboard` object's name now includes a hash of `instanceSelector`. On upgrade, Helm deletes the old dashboard object and creates the new one. ([#192](https://github.com/solidDoWant/harbor-operator/pull/192))

### Robots and permissions

- The new `harbor-operator permissions` command prints the robot permissions the operator needs, as a `HarborSystemRobot` spec. ([#194](https://github.com/solidDoWant/harbor-operator/pull/194))
- The operator now lists `quota:list` as a required system permission for projects, because it reads and sets storage quotas. Before, a connection's `PermissionsSufficient` condition was `True` for a robot without it. ([#188](https://github.com/solidDoWant/harbor-operator/pull/188))

### Using kubectl

- Every kind is in the `harbor` category, so `kubectl get harbor -A` lists all of them, and every kind has a short name. ([#187](https://github.com/solidDoWant/harbor-operator/pull/187))

### Fixes

- A robot replaced through a system-robot connection is no longer replaced a second time from a stale cached read. Before, that could leave the robot's Secret with a secret Harbor no longer accepted. ([#197](https://github.com/solidDoWant/harbor-operator/pull/197))

Internal work: CI and releases run on the shared Tekton Pipelines instead of GitHub Actions, the test harness retries kind cluster creation on host port conflicts and a test retries finalizer removal, and the project's agents act as the GitHub App through githubgate interception ([#193](https://github.com/solidDoWant/harbor-operator/pull/193), [#196](https://github.com/solidDoWant/harbor-operator/pull/196), [#203](https://github.com/solidDoWant/harbor-operator/pull/203), [#204](https://github.com/solidDoWant/harbor-operator/pull/204)).

### Known issues

None.

## 0.4.0

harbor-operator is a Kubernetes operator that configures an existing Harbor registry through custom resources. It does not install Harbor. With this release, the operator can manage its own Harbor robot account through its own custom resources, so you no longer make that robot by hand. The release also adds a check of the permissions of each connection, and an option that deletes the images of a project together with the project. To install or upgrade, run `helm upgrade --install harbor-operator oci://ghcr.io/soliddowant/charts/harbor-operator --version 0.4.0 --namespace harbor-operator --create-namespace`. The chart also updates the custom resource definitions. The [credentials guide](https://github.com/solidDoWant/harbor-operator/blob/v0.4.0/docs/admin/credentials.md) tells you how to set up the operator's robot.

### Upgrade notes

An upgrade from 0.3.0 needs no change. Existing connections keep working, and all new fields and chart values are off or empty by default.

### Connections and credentials

- A `HarborConnection` can now read its credentials from any Secret, for example the Secret that Harbor's chart makes for the admin password. Set `credentialsSecretRef.usernameKey` and `credentialsSecretRef.passwordKey` to name the keys in the Secret, or set `credentialsSecretRef.username` to give the username directly. The defaults are the keys `username` and `password`, as before. ([#166](https://github.com/solidDoWant/harbor-operator/pull/166))
- The operator can manage its own robot account (the root-robot pattern). An admin connection signs in with Harbor's admin password and manages a `HarborSystemRobot`. The main connection signs in with the Secret of that robot. The operator now refuses a system robot with `OperatorCredential` only when the robot's own connection signs in as that robot, or when a connection in another namespace that Harbor accepts signs in as it. The [credentials guide](https://github.com/solidDoWant/harbor-operator/blob/v0.4.0/docs/admin/credentials.md) shows the setup, and how to move from a robot that you made by hand. ([#168](https://github.com/solidDoWant/harbor-operator/pull/168), [#171](https://github.com/solidDoWant/harbor-operator/pull/171), [#178](https://github.com/solidDoWant/harbor-operator/pull/178))
- A `HarborConnection` has a new condition, `PermissionsSufficient`. If the Harbor credential does not have a permission that the resources on the connection need, the condition is `False` with the reason `InsufficientPermissions`, and the message lists the missing permissions. The operator also emits an event when the list changes. ([#172](https://github.com/solidDoWant/harbor-operator/pull/172))
- A new page lists the Harbor permissions that each kind needs. The operator's own code generates this list, so it stays correct. See [required permissions](https://github.com/solidDoWant/harbor-operator/blob/v0.4.0/docs/admin/required-permissions.md). ([#164](https://github.com/solidDoWant/harbor-operator/pull/164))

### Robots and Secrets

- A robot does not delete its Secret while a `HarborConnection` in the same namespace signs in with that Secret. When you delete the robot, or disable or rename its Secret, the robot status shows `DependentsExist` and names the connection. Delete the connection, or point it at another Secret, and the robot continues. Only Secrets that the robot owns count. ([#169](https://github.com/solidDoWant/harbor-operator/pull/169), [#179](https://github.com/solidDoWant/harbor-operator/pull/179))
- A `HarborSystemRobot` can now get the project permissions `read`, `update` and `delete` on the `project` resource in `projectPermissions`. A `HarborProjectRobot` cannot get them. ([#163](https://github.com/solidDoWant/harbor-operator/pull/163))

### Projects

- The new deletion policy `DeleteWithContents` deletes every image in a `HarborProject` and then the project. You cannot undo this. It works only when the connection sets `allowContentDeletion: true`. Otherwise, the operator deletes no image, and the project shows `DeleteBlocked` while it still holds images. On other kinds, `DeleteWithContents` is the same as `Delete`. ([#170](https://github.com/solidDoWant/harbor-operator/pull/170))

### Deployment

- The new chart value `podLabels` adds labels to the operator pod, for example to match a network policy. ([#175](https://github.com/solidDoWant/harbor-operator/pull/175))
- The new chart value `grafanaDashboard.grafanaOperator.allowCrossNamespaceImport` lets the grafana-operator import the dashboard into a Grafana instance in another namespace. ([#175](https://github.com/solidDoWant/harbor-operator/pull/175))

### Security

- The operator is built with Go 1.27.4 and updated libraries (compress, OpenTelemetry, gRPC). This removes the advisories that 0.3.0 listed as known issues. ([#141](https://github.com/solidDoWant/harbor-operator/pull/141), [#144](https://github.com/solidDoWant/harbor-operator/pull/144), [#181](https://github.com/solidDoWant/harbor-operator/pull/181))

### Fixes

- When a `HarborSystemRobot` uses a connection that signs in as a robot, and Harbor refuses to update the system robot, the operator now replaces it. Before, the system robot stayed `Forbidden` and named permissions that the connection had. ([#165](https://github.com/solidDoWant/harbor-operator/pull/165))

Internal work: the design for this release and the answers to its reviews, end-to-end tests of the root-robot pattern, and updates to test images and the dependency bot ([#136](https://github.com/solidDoWant/harbor-operator/pull/136), [#137](https://github.com/solidDoWant/harbor-operator/pull/137), [#138](https://github.com/solidDoWant/harbor-operator/pull/138), [#139](https://github.com/solidDoWant/harbor-operator/pull/139), [#140](https://github.com/solidDoWant/harbor-operator/pull/140), [#143](https://github.com/solidDoWant/harbor-operator/pull/143), [#145](https://github.com/solidDoWant/harbor-operator/pull/145), [#146](https://github.com/solidDoWant/harbor-operator/pull/146), [#150](https://github.com/solidDoWant/harbor-operator/pull/150), [#151](https://github.com/solidDoWant/harbor-operator/pull/151), [#153](https://github.com/solidDoWant/harbor-operator/pull/153), [#167](https://github.com/solidDoWant/harbor-operator/pull/167), [#173](https://github.com/solidDoWant/harbor-operator/pull/173)).

### Known issues

`govulncheck` reports two advisories in libraries that the operator includes. The operator does not call the affected code, for the reasons below.

- GO-2026-6443 (`google.golang.org/grpc`, server crash): the risk applies to a gRPC server. The operator runs no gRPC server. The package is only linked in through Kubernetes libraries.
- GO-2026-6094 (`github.com/google/cel-go`, private JSON fields): the risk applies to programs that pass their own Go types to CEL. The operator does not do this. The package is only linked in through Kubernetes libraries.

## 0.3.0

harbor-operator is a Kubernetes operator that configures an existing Harbor registry through custom resources. It does not install Harbor. This release makes the operator easier to run: it adds metrics, a Grafana dashboard, Kubernetes events for every change, a namespace limit, a network policy and new guides. It also changes how the operator behaves while Harbor is down. To install or upgrade, run `helm upgrade --install harbor-operator oci://ghcr.io/soliddowant/charts/harbor-operator --version 0.3.0 --namespace harbor-operator --create-namespace`. The [install guide](https://github.com/solidDoWant/harbor-operator/blob/v0.3.0/docs/admin/install.md) tells you how to connect the operator to Harbor.

### Upgrade notes

- If your nodes need the old CPU request, set `resources.requests.cpu: 100m`. The default CPU request is now `10m`, because the operator uses little CPU. ([#113](https://github.com/solidDoWant/harbor-operator/pull/113))

All new chart values are off or empty by default. An upgrade from 0.2.0 needs no other change.

### Monitoring

- The operator exports metrics: the number of resources for each kind and `Ready` reason (`harbor_operator_resources`), the time of each Harbor request (`harbor_operator_harbor_requests_seconds`), the number of registries for each health state (`harbor_operator_registry_health`), and the number of changes in Harbor that the operator put back (`harbor_operator_drift_corrections_total`). The [metrics guide](https://github.com/solidDoWant/harbor-operator/blob/v0.3.0/docs/admin/metrics.md) lists them. ([#110](https://github.com/solidDoWant/harbor-operator/pull/110), [#130](https://github.com/solidDoWant/harbor-operator/pull/130))
- The chart can create a `ServiceMonitor` for the Prometheus Operator or a `VMServiceScrape` for VictoriaMetrics. It can also create a ClusterRole that lets your scraper read the secure metrics endpoint. Set `metrics.serviceMonitor.enabled`, `metrics.vmServiceScrape.enabled` and `metrics.reader.enabled`. ([#122](https://github.com/solidDoWant/harbor-operator/pull/122))
- The chart can install a Grafana dashboard, as a ConfigMap for the Grafana sidecar or as a `GrafanaDashboard` for the grafana-operator. Set `grafanaDashboard.configMap.enabled` or `grafanaDashboard.grafanaOperator.enabled`. ([#119](https://github.com/solidDoWant/harbor-operator/pull/119))
- Every kind emits a Kubernetes event when the operator creates, changes, deletes or orphans a Harbor object. To orphan an object is to leave it in Harbor when you delete its resource. A `HarborConnection` also emits events now. The [events page](https://github.com/solidDoWant/harbor-operator/blob/v0.3.0/docs/user/events.md) lists them. ([#114](https://github.com/solidDoWant/harbor-operator/pull/114))

### Deployment

- The new `watchNamespaces` chart value limits the operator to the namespaces in the list. When you set it, the chart gives the operator a Role in each of those namespaces instead of one ClusterRole for the whole cluster. ([#117](https://github.com/solidDoWant/harbor-operator/pull/117))
- If a resource names a connection, project or registry in a namespace that the operator does not watch, the status shows `NotAllowed`. When you delete such a resource, the operator leaves its Harbor object in place. ([#124](https://github.com/solidDoWant/harbor-operator/pull/124))
- The chart can create a NetworkPolicy and a PodDisruptionBudget. The NetworkPolicy lets the operator connect only to the Kubernetes API server, Harbor and the cluster DNS. Set `networkPolicy.enabled` and `networkPolicy.apiServer.cidrs`, or `podDisruptionBudget.enabled`. ([#113](https://github.com/solidDoWant/harbor-operator/pull/113))

### Fixes

- While Harbor is down, resources show `HarborUnavailable` and the operator does not send requests to Harbor for them. The connection tries Harbor again after 5 seconds, and waits longer after each failure, up to 5 minutes. When Harbor is back, all resources become ready without help. ([#121](https://github.com/solidDoWant/harbor-operator/pull/121))
- If Harbor resets the network connection, the operator no longer tries again in a tight loop. It waits as described above. ([#129](https://github.com/solidDoWant/harbor-operator/pull/129))
- The `harbor_operator_drift_corrections_total` metric is now exported, so the drift panel of the dashboard shows data. ([#130](https://github.com/solidDoWant/harbor-operator/pull/130))

### Documentation

- A new troubleshooting guide tells admins what each condition reason means and what to do. ([#115](https://github.com/solidDoWant/harbor-operator/pull/115))
- A new example shows a Helm chart that gives a CI pipeline its own Harbor project and robot. Tests run this example. ([#118](https://github.com/solidDoWant/harbor-operator/pull/118))

Internal work: tests against the two latest Harbor versions (2.14 and 2.15), a test of the chart upgrade from 0.2.0, test fixes and the answers to the design review ([#109](https://github.com/solidDoWant/harbor-operator/pull/109), [#111](https://github.com/solidDoWant/harbor-operator/pull/111), [#120](https://github.com/solidDoWant/harbor-operator/pull/120), [#123](https://github.com/solidDoWant/harbor-operator/pull/123), [#126](https://github.com/solidDoWant/harbor-operator/pull/126)).

### Known issues

`govulncheck` reports the same nine advisories as in 0.2.0, in libraries that the operator includes. The operator cannot reach any of them, for the reasons below. Dependency updates will remove them.

- GO-2026-6865 (`github.com/klauspost/compress`): only a test package of the Prometheus library uses this module. The operator binary does not contain it.
- GO-2026-6859 (Go `net/http`, HTTP/1 server crash): the crash needs a handler that uses `MaxBytesReader`. The health and metrics endpoints of the operator do not use it.
- GO-2026-6603, GO-2026-6612 and GO-2026-6617 (`golang.org/x/net`, HTTP/2 server): the operator runs no HTTP/2 server. The metrics endpoint accepts only HTTP/1.1 unless you set `--enable-http2`, and the chart does not set it.
- GO-2026-6610 (`golang.org/x/net`, malformed HTTP/2 headers): the risk applies to a reverse proxy. The operator is not a proxy.
- GO-2026-6611 (`golang.org/x/net`, HTTP/2 CPU use): the operator uses HTTP/2 only as a client of the Kubernetes API server, which the operator trusts. It connects to Harbor over HTTP/1.1.
- GO-2026-6348 (`google.golang.org/grpc`) and GO-2026-6505 (`go.opentelemetry.io/otel` trace exporter): the operator makes no gRPC connections and starts no trace exporter. These packages are only linked in through Kubernetes libraries.

## 0.2.0

harbor-operator is a Kubernetes operator that configures an existing Harbor registry through custom resources. It does not install Harbor. This release adds pull-through caches, tag retention and tag immutability policies, and system robot accounts. It also fixes four problems in 0.1.0. To install or upgrade, run `helm upgrade --install harbor-operator oci://ghcr.io/soliddowant/charts/harbor-operator --version 0.2.0 --namespace harbor-operator --create-namespace`. The chart also installs the new custom resource definitions. The [install guide](https://github.com/solidDoWant/harbor-operator/blob/v0.2.0/docs/admin/install.md) tells you how to connect the operator to Harbor.

### Proxy caches

- A `HarborRegistry` tells Harbor where an upstream registry is, for example ghcr.io or quay.io. The upstream credentials come from a Secret in the namespace of the registry. If somebody changes the registry in Harbor, the operator changes it back. The `Health` column shows if Harbor can reach the upstream, and the operator emits a `HealthChanged` event when the health changes. ([#78](https://github.com/solidDoWant/harbor-operator/pull/78), [#87](https://github.com/solidDoWant/harbor-operator/pull/87))
- A `HarborProject` with `spec.proxyCache` makes a pull-through cache: a Harbor project that gets images from the upstream registry when somebody pulls them, and keeps a copy. The registry must admit the namespace of the project in its `allowedNamespaces`. ([#78](https://github.com/solidDoWant/harbor-operator/pull/78), [#87](https://github.com/solidDoWant/harbor-operator/pull/87))
- A registry is a Harbor-wide object. The `systemAllowedNamespaces` of the connection must admit the namespace of the registry. ([#87](https://github.com/solidDoWant/harbor-operator/pull/87))
- The operator does not delete a registry while cache projects still use it. The status shows `DependentsExist`. If Harbor refuses the delete, the status shows `DeleteBlocked`. ([#87](https://github.com/solidDoWant/harbor-operator/pull/87))

### Policies

- A `HarborRetentionPolicy` sets the tag retention rules and the schedule of one project. Harbor runs these rules to delete old images. The operator removes rules that somebody adds or changes in Harbor. When you delete the resource, the operator removes the rules from Harbor, or keeps them if `deletionPolicy` is `Orphan`. ([#78](https://github.com/solidDoWant/harbor-operator/pull/78), [#84](https://github.com/solidDoWant/harbor-operator/pull/84))
- A `HarborImmutabilityPolicy` sets the tag immutability rules of one project. Nobody can push over or delete a tag that these rules protect. The operator puts back the rules from the spec when somebody changes them in Harbor. A project waits for its immutability policy to go before the operator deletes it. ([#78](https://github.com/solidDoWant/harbor-operator/pull/78), [#85](https://github.com/solidDoWant/harbor-operator/pull/85))
- A project has one policy of each kind. A second policy for the same project shows `Conflict` and changes nothing. ([#84](https://github.com/solidDoWant/harbor-operator/pull/84), [#85](https://github.com/solidDoWant/harbor-operator/pull/85))

### Robots and Secrets

- A `HarborSystemRobot` creates a Harbor robot account that is not tied to one project. It can have system permissions and access to many projects, or to all projects with `*`. The operator writes its credentials into Secrets in the namespace of the robot, as for a project robot. The `systemAllowedNamespaces` of the connection must admit that namespace. ([#78](https://github.com/solidDoWant/harbor-operator/pull/78), [#86](https://github.com/solidDoWant/harbor-operator/pull/86), [#89](https://github.com/solidDoWant/harbor-operator/pull/89))
- The API rejects a `HarborSystemRobot` with a system permission that is not in the Harbor list of system permissions. ([#83](https://github.com/solidDoWant/harbor-operator/pull/83))
- The operator never changes or deletes the robot that a connection uses to sign in to Harbor. A `HarborSystemRobot` with the name of that robot shows `OperatorCredential`. ([#89](https://github.com/solidDoWant/harbor-operator/pull/89))

### Fixes

- A project robot that Harbor refuses to update is no longer deleted when the connection does not have a permission that the robot asks for. The status shows `Forbidden` and the missing permissions. ([#82](https://github.com/solidDoWant/harbor-operator/pull/82))
- If you delete a `HarborConnection` before the resources that use it, they no longer stay in `Terminating`. The resources apply their `deletionPolicy` in Harbor, and then the connection goes. ([#100](https://github.com/solidDoWant/harbor-operator/pull/100))
- A connection with a wrong password tries again once per resync interval. Before, it tried fast enough to lock the Harbor account. ([#103](https://github.com/solidDoWant/harbor-operator/pull/103))
- If somebody renames a registry in Harbor, another `HarborRegistry` cannot take over, change or delete it. It shows `Conflict`. ([#104](https://github.com/solidDoWant/harbor-operator/pull/104))

### Documentation

- New guides for pull-through caches, retention, immutability and system robots. Tests run the examples in these guides. ([#87](https://github.com/solidDoWant/harbor-operator/pull/87), [#84](https://github.com/solidDoWant/harbor-operator/pull/84), [#85](https://github.com/solidDoWant/harbor-operator/pull/85), [#89](https://github.com/solidDoWant/harbor-operator/pull/89), [#96](https://github.com/solidDoWant/harbor-operator/pull/96))

Internal work: parallel integration tests, CI time limits, test fixes and the design for the next phase ([#90](https://github.com/solidDoWant/harbor-operator/pull/90), [#93](https://github.com/solidDoWant/harbor-operator/pull/93), [#97](https://github.com/solidDoWant/harbor-operator/pull/97), [#101](https://github.com/solidDoWant/harbor-operator/pull/101)).

### Known issues

`govulncheck` reports the same nine advisories as in 0.1.0, in libraries that the operator includes. The operator cannot reach any of them, for the reasons below. Dependency updates will remove them.

- GO-2026-6865 (`github.com/klauspost/compress`): only a test package of the Prometheus library uses this module. The operator binary does not contain it.
- GO-2026-6859 (Go `net/http`, HTTP/1 server crash): the crash needs a handler that uses `MaxBytesReader`. The health and metrics endpoints of the operator do not use it.
- GO-2026-6603, GO-2026-6612 and GO-2026-6617 (`golang.org/x/net`, HTTP/2 server): the operator runs no HTTP/2 server. The metrics endpoint accepts only HTTP/1.1 unless you set `--enable-http2`, and the chart does not set it.
- GO-2026-6610 (`golang.org/x/net`, malformed HTTP/2 headers): the risk applies to a reverse proxy. The operator is not a proxy.
- GO-2026-6611 (`golang.org/x/net`, HTTP/2 CPU use): the operator uses HTTP/2 only as a client of the Kubernetes API server, which the operator trusts. It connects to Harbor over HTTP/1.1.
- GO-2026-6348 (`google.golang.org/grpc`) and GO-2026-6505 (`go.opentelemetry.io/otel` trace exporter): the operator makes no gRPC connections and starts no trace exporter. These packages are only linked in through Kubernetes libraries.

## 0.1.0

harbor-operator is a Kubernetes operator that configures an existing Harbor registry through custom resources. It does not install Harbor. This is the first release. It manages Harbor projects and project robot accounts, and it writes the robot credentials into Kubernetes Secrets. To install it, run `helm install harbor-operator oci://ghcr.io/soliddowant/charts/harbor-operator --version 0.1.0 --namespace harbor-operator --create-namespace`. Then follow the [install guide](https://github.com/solidDoWant/harbor-operator/blob/v0.1.0/docs/admin/install.md) to connect the operator to Harbor.

### Connections

- A `HarborConnection` holds the Harbor URL and the name of a Secret with a username and a password. Its status shows `Ready` and the Harbor version, or the reason the operator cannot sign in: `CredentialsInvalid`, `Unauthorized` or `HarborUnavailable`. ([#41](https://github.com/solidDoWant/harbor-operator/pull/41), [#45](https://github.com/solidDoWant/harbor-operator/pull/45), [#52](https://github.com/solidDoWant/harbor-operator/pull/52))
- A connection can use a Harbor system robot account instead of an admin user. The operator can create projects through such a robot. ([#43](https://github.com/solidDoWant/harbor-operator/pull/43), [#62](https://github.com/solidDoWant/harbor-operator/pull/62))
- The operator does not delete a connection while a project still uses it. The status shows `DependentsExist` and lists those projects. ([#52](https://github.com/solidDoWant/harbor-operator/pull/52))

### Projects

- A `HarborProject` creates a Harbor project and keeps these settings equal to the spec: public access, storage quota and the vulnerability settings (scan on push, the block on vulnerable images, the severity level and the CVE allowlist). If somebody changes one of these settings in Harbor, the operator changes it back. The operator does not touch the settings that the spec leaves empty. ([#41](https://github.com/solidDoWant/harbor-operator/pull/41), [#53](https://github.com/solidDoWant/harbor-operator/pull/53))
- To manage a project that already exists in Harbor, set `adopt: true`. The connection must have `allowAdoption: true`. Otherwise the status shows `AlreadyExists` or `AdoptionNotAllowed`. ([#53](https://github.com/solidDoWant/harbor-operator/pull/53))
- If two resources name the same Harbor project, the older one keeps it and the newer one shows `Conflict`. ([#50](https://github.com/solidDoWant/harbor-operator/pull/50), [#53](https://github.com/solidDoWant/harbor-operator/pull/53))
- When you delete a `HarborProject`, the operator deletes the Harbor project if `deletionPolicy` is `Delete`, and leaves it in Harbor if it is `Orphan`. The connection sets the default policy. The operator waits with `DependentsExist` while robots use the project, and shows `DeleteBlocked` while the project still holds images. ([#55](https://github.com/solidDoWant/harbor-operator/pull/55))
- When the connection of a project is gone, deleting the project or its robots finishes and leaves the Harbor objects in place. ([#72](https://github.com/solidDoWant/harbor-operator/pull/72))

### Robots and Secrets

- A `HarborProjectRobot` creates a Harbor robot account for one project, with the permissions and the duration in the spec. The operator puts back changes made in Harbor, and deletes the Harbor robot with the resource when the policy is `Delete`. ([#41](https://github.com/solidDoWant/harbor-operator/pull/41), [#56](https://github.com/solidDoWant/harbor-operator/pull/56), [#62](https://github.com/solidDoWant/harbor-operator/pull/62))
- The operator writes the robot credentials into a `kubernetes.io/dockerconfigjson` Secret in the namespace of the robot. You can use it as an image pull Secret. To also get a Secret with the keys `username`, `password` and `registry`, set `spec.secret.opaque.enabled: true`. ([#46](https://github.com/solidDoWant/harbor-operator/pull/46), [#56](https://github.com/solidDoWant/harbor-operator/pull/56))
- To get a new robot password, change `spec.secret.rotate` to any new value. If you delete a robot Secret, the operator makes a new password and writes the Secret again. ([#58](https://github.com/solidDoWant/harbor-operator/pull/58), [#63](https://github.com/solidDoWant/harbor-operator/pull/63))
- The operator never overwrites a Secret that it did not create. If a Secret with the target name exists, the status shows `SecretConflict`. A robot whose two Secrets have the same name is rejected. ([#58](https://github.com/solidDoWant/harbor-operator/pull/58), [#70](https://github.com/solidDoWant/harbor-operator/pull/70))

### Security

- A resource can use a connection or a project only if the `allowedNamespaces` of that object admits the namespace of the resource. If it does not, the status shows `NotAllowed` and the operator changes nothing in Harbor. ([#50](https://github.com/solidDoWant/harbor-operator/pull/50), [#53](https://github.com/solidDoWant/harbor-operator/pull/53), [#56](https://github.com/solidDoWant/harbor-operator/pull/56))
- Robot passwords and connection credentials appear only in Kubernetes Secrets. They are not in logs, status, conditions or events. ([#60](https://github.com/solidDoWant/harbor-operator/pull/60))
- A resource cannot take over or delete a Harbor project or robot that another resource owns, or one that somebody made in Harbor. The operator keeps its record of a create that is in progress in the status, where users cannot write it. ([#65](https://github.com/solidDoWant/harbor-operator/pull/65), [#71](https://github.com/solidDoWant/harbor-operator/pull/71), [#73](https://github.com/solidDoWant/harbor-operator/pull/73), [#74](https://github.com/solidDoWant/harbor-operator/pull/74))

### Deployment

- The operator image `ghcr.io/soliddowant/harbor-operator` is for linux/amd64 and linux/arm64. The Helm chart `oci://ghcr.io/soliddowant/charts/harbor-operator` installs the operator, its custom resource definitions and its permissions. ([#47](https://github.com/solidDoWant/harbor-operator/pull/47), [#54](https://github.com/solidDoWant/harbor-operator/pull/54))
- The documentation has guides for chart authors and admins, and a reference for each field of each resource. ([#59](https://github.com/solidDoWant/harbor-operator/pull/59))

Internal work: CI, the release workflow, the Harbor test environment on kind, end-to-end and leak tests, and agent instructions ([#37](https://github.com/solidDoWant/harbor-operator/pull/37), [#38](https://github.com/solidDoWant/harbor-operator/pull/38), [#39](https://github.com/solidDoWant/harbor-operator/pull/39), [#40](https://github.com/solidDoWant/harbor-operator/pull/40), [#44](https://github.com/solidDoWant/harbor-operator/pull/44), [#49](https://github.com/solidDoWant/harbor-operator/pull/49), [#51](https://github.com/solidDoWant/harbor-operator/pull/51), [#54](https://github.com/solidDoWant/harbor-operator/pull/54)).

### Known issues

`govulncheck` reports nine advisories in libraries that the operator includes. The operator cannot reach any of them, for the reasons below. Dependency updates will remove them.

- GO-2026-6865 (`github.com/klauspost/compress`): only a test package of the Prometheus library uses this module. The operator binary does not contain it.
- GO-2026-6859 (Go `net/http`, HTTP/1 server crash): the crash needs a handler that uses `MaxBytesReader`. The health and metrics endpoints of the operator do not use it.
- GO-2026-6603, GO-2026-6612 and GO-2026-6617 (`golang.org/x/net`, HTTP/2 server): the operator runs no HTTP/2 server. The metrics endpoint accepts only HTTP/1.1 unless you set `--enable-http2`, and the chart does not set it.
- GO-2026-6610 (`golang.org/x/net`, malformed HTTP/2 headers): the risk applies to a reverse proxy. The operator is not a proxy.
- GO-2026-6611 (`golang.org/x/net`, HTTP/2 CPU use): the operator uses HTTP/2 only as a client of the Kubernetes API server, which the operator trusts. It connects to Harbor over HTTP/1.1.
- GO-2026-6348 (`google.golang.org/grpc`) and GO-2026-6505 (`go.opentelemetry.io/otel` trace exporter): the operator makes no gRPC connections and starts no trace exporter. These packages are only linked in through Kubernetes libraries.
