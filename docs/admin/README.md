# harbor-operator for admins

For: people who install harbor-operator and connect it to Harbor. You know Kubernetes, Helm, Flux and Harbor's web interface.

harbor-operator is in early development. These pages are not written yet. They will tell you how to:

- install the operator with its Helm chart;
- create the Harbor system robot account that the operator signs in with, and the `HarborConnection`;
- decide which namespaces may use a connection, a project or a registry;
- understand the security limits, and fix problems.

For now, two pages exist: [which Harbor credential to use](credentials.md) and the [chart values](chart-values.md).

For the rest, read [`SPEC.md`](../../SPEC.md).
