# harbor-operator for developers

For: people who change the harbor-operator code. You know Go and controller-runtime.

harbor-operator is in early development. The pages on the architecture are not written yet. They will describe:

- the architecture and the packages under `internal/`;
- the test tiers: unit tests, integration tests (envtest and a real Harbor) and e2e tests (kind, Harbor and the chart);
- how acceptance tests are tied to scenarios (the `Tests:` lines of the checklist) and counted by `make acceptance`;
- how to release.

## Integration tests

`make test-integration` starts Harbor on a kind cluster with `test/harbor/up.sh`, runs the `integration` tests with `HARBOR_URL`, `HARBOR_ADMIN_USER` and `HARBOR_ADMIN_PASSWORD` set, and deletes the cluster it created. Set `KEEP_CLUSTER=1` to keep it. Set `HARBOR_CLUSTER` to change the cluster name (default `harbor-op-int`). Without Docker the tests skip. The helper package `test/harborfixture` gives tests an admin client, unique names, projects, image pushes and cleanup.

## Getting started

The tools come from the Nix flake. Run `nix develop` (or use `direnv`) in the checkout. Then `make help` lists the commands. The most common ones are:

- `make build`: build `bin/harbor-operator`.
- `make fmt lint`: format the code and run every check.
- `make test`: run the unit tests.
- `make generate`: regenerate the CRDs, RBAC and deepcopy code after a change to `api/`.

Until the other pages exist, read [`SPEC.md`](../../SPEC.md) and the design for the current phase in [`docs/design/`](../design/phase-1.md).
