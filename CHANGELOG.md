# Changelog

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
