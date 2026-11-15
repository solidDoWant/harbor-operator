# harbor-operator

harbor-operator is a Kubernetes operator that sets up an existing Harbor registry from custom resources. It does not install Harbor.

You write a few custom resources, for example in the Helm chart that sets up CI for a repository. The operator then makes the Harbor project with its settings and storage quota, and a robot account for CI. It puts the robot's password in a Kubernetes Secret that a pipeline can use to push images. Later phases add tag retention and tag immutability policies, proxy caches in front of other registries, and system robot accounts.

The custom resources are the source of truth. When someone changes a setting in Harbor's web interface, the operator changes it back.

## Status

harbor-operator is in early development. The first release is not published yet. Autonomous agents build it, one phase at a time. The owner's checklist for each phase is in [`docs/acceptance/`](docs/acceptance/phase-1.md).

## More information

- [`SPEC.md`](SPEC.md) describes the full system.
- [`docs/README.md`](docs/README.md) lists the documentation for users, admins and developers.

## License

harbor-operator is licensed under the GNU Affero General Public License v3.0. See [`LICENSE`](LICENSE).
