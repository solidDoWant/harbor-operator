# harbor-operator documentation

For: anyone who wants to find the right page. You need to know nothing yet.

harbor-operator has four kinds of readers. Each page is for one of them. This page says who the readers are, which pages are theirs, and what each page covers.

## Who reads these docs

| Reader | What they already know | What they need | Pages |
|---|---|---|---|
| Chart authors (users) | Kubernetes YAML and Helm. | Which resources to write for a project, a robot and a cache, and what each status means. | `docs/user/` |
| Admins | Kubernetes, Helm, Flux and Harbor's web interface. | How to install the operator, create the system robot and the connection, allow namespaces, the security limits, and how to fix problems. | `docs/admin/` (the chart value reference is generated) |
| Developers | Go and controller-runtime. | The architecture, the packages, the test setup and how to release. | `docs/dev/` |
| Agents | This repository. | Playbooks. Their pages do not follow the style rules for people. | `docs/agents/`, `.claude/`, `CLAUDE.md` |

The owner reads the acceptance checklists, the release notes and the status reports.

## Where each reader starts

- Chart authors: [`docs/user/`](user/README.md).
- Admins: [`docs/admin/`](admin/README.md).
- Developers: [`docs/dev/`](dev/README.md). [`SPEC.md`](../SPEC.md) has the full rules of the system.
- Agents: `CLAUDE.md`, then [`docs/agents/`](agents/planner.md).

## The tree

Every page under `docs/` is listed here. `make docs-check` fails when a page is missing from this list.

```
README.md                  The front door: what harbor-operator is and its status.
SPEC.md                    The rules of the system. For developers and agents.
CHANGELOG.md               The release notes.

docs/README.md             This page.

docs/user/
  README.md                The pages for chart authors.
  example.md               A project with a CI robot, the robot Secret and the status reasons.
  retention.md             A tag retention policy for a project and its status reasons.
  crd-reference.md         Every resource field (generated).
  robot-permissions.md     The resource and action pairs a project robot may be granted.
  immutability.md          Tag immutability rules for a project.

docs/admin/
  README.md                The pages for admins.
  install.md               Install the chart, create the system robot and the connection.
  namespace-access.md      Allowed namespaces, adoption, deletion policies and the security model.
  credentials.md           Which Harbor credential the operator signs in with.
  system-robots.md         System robots: access to many projects, and their status reasons.
  chart-values.md          Every Helm chart value (generated).

docs/dev/
  README.md                What the developer pages will cover, and the commands.

docs/acceptance/
  phase-1.md               The owner's checklist for phase 1: projects and robots.
  phase-2.md               The owner's checklist for phase 2: policies, proxy caches and system robots.
  phase-3.md               The owner's checklist for phase 3: operations and documentation.

docs/design/
  phase-1.md               The design for phase 1: types, packages and the reconcile tables.
  phase-2.md               The design for phase 2: types, packages and the reconcile tables.
  phase-3.md               The design for phase 3: metrics, events, Harbor outages, chart hardening and docs.

docs/agents/               Instructions for the agents (not for people).
  planner.md               The planner's playbook.
```

## Rules for writing the pages

- Write one page for one reader. Put each fact on the page for the reader who acts on it, and link to it from other pages.
- Write in plain English. Agents load the `simple-english` skill before they write a page for people.
- Generate reference tables (CRD fields, chart values, metrics) from the code when they exist, so that they stay correct.
- `make docs-check` checks the links, this list and the generated pages. It does not count words or score the style.
