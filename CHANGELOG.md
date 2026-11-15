# Changelog

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
