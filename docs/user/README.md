# harbor-operator for chart authors

For: people who write the Kubernetes resources for a project, for example in a Helm chart that sets up CI. You know Kubernetes YAML and Helm.

- [Write a project and a CI robot](example.md): a complete example, the robot Secret and what each status reason means.
- [A Helm chart for one CI pipeline](ci-chart-example.md): a tested chart with a namespace, a project, a push robot and a retention policy.
- [Make a pull-through cache](proxy-cache.md): a registry and a proxy-cache project, and their status reasons.
- [Keep only the tags you need](retention.md): a tag retention policy for a project.
- [Field reference](crd-reference.md): every field of every resource (generated).
- [Protect tags from being overwritten](immutability.md): the immutability rules of a project.
- [Robot permissions](robot-permissions.md): the resource and action pairs a robot can have.
- [Kubernetes events](events.md): which events the operator writes and when.

List everything the operator manages with `kubectl get harbor -A`. Each kind has a short name: `hproj` (HarborProject), `hprobot` (HarborProjectRobot), `hsrobot` (HarborSystemRobot), `hconn` (HarborConnection), `hreg` (HarborRegistry), `hret` (HarborRetentionPolicy) and `himm` (HarborImmutabilityPolicy). For example, `kubectl get hproj -n my-namespace`.

Your admin must give you a connection and allow your namespace. See [`docs/admin/`](../admin/README.md). [`SPEC.md`](../../SPEC.md) has the full rules.
