# Project robot permissions

For: chart authors who write a `HarborProjectRobot`.

A project robot lists the access it needs in `spec.permissions`. Each entry has a `resource` and an `action`. The API server rejects an entry that is not in this table.

The table is Harbor 2.15's documented list for project robots. It was not read from a running Harbor's `GET /api/v2.0/permissions`, because no Harbor was available when it was written.

| Resource | Actions |
|---|---|
| `accessory` | `list` |
| `artifact` | `delete`, `list`, `read` |
| `artifact-label` | `create`, `delete` |
| `configuration` | `read`, `update` |
| `export-cve` | `create`, `read` |
| `immutable-tag` | `create`, `delete`, `list`, `update` |
| `label` | `create`, `delete`, `list`, `read`, `update` |
| `log` | `list` |
| `member` | `create`, `delete`, `list`, `read`, `update` |
| `metadata` | `create`, `delete`, `list`, `read`, `update` |
| `notification-policy` | `create`, `delete`, `list`, `read`, `update` |
| `preheat-policy` | `create`, `delete`, `list`, `read`, `update` |
| `quota` | `read` |
| `repository` | `create`, `delete`, `list`, `pull`, `push`, `read`, `update` |
| `robot` | `create`, `delete`, `list`, `read`, `update` |
| `sbom` | `create`, `read`, `stop` |
| `scan` | `create`, `read`, `stop` |
| `scanner-pull` | `read` |
| `tag` | `create`, `delete`, `list` |
| `tag-retention` | `create`, `delete`, `list`, `operate`, `read`, `update` |

Example: a robot that can push and pull images.

```yaml
permissions:
  - resource: repository
    action: push
  - resource: repository
    action: pull
```
