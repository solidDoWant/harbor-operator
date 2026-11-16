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

## System robot permissions

A `HarborSystemRobot` lists system-level access in `spec.systemPermissions`. Each entry has a `resource` and an `action`. The API server rejects an entry that is not in this table. Project access in `spec.projectPermissions` uses the project table above, plus the extra entry in the next section.

The table is Harbor 2.15's documented list for system robots. It was not read from a running Harbor's `GET /api/v2.0/permissions`, because no Harbor was available when it was written.

| Resource | Actions |
|---|---|
| `audit-log` | `list` |
| `catalog` | `read` |
| `configuration` | `read`, `update` |
| `garbage-collection` | `create`, `list`, `read`, `stop`, `update` |
| `jobservice-monitor` | `list`, `stop` |
| `label` | `create`, `delete`, `read`, `update` |
| `ldap-user` | `create`, `list` |
| `log-rotation` | `create`, `list`, `read`, `stop`, `update` |
| `preheat-instance` | `create`, `delete`, `list`, `read`, `update` |
| `project` | `create`, `list` |
| `purge-audit` | `create`, `list`, `read`, `stop`, `update` |
| `quota` | `list`, `read`, `update` |
| `registry` | `create`, `delete`, `list`, `read`, `update` |
| `replication` | `create`, `list`, `read` |
| `replication-adapter` | `list` |
| `replication-policy` | `create`, `delete`, `list`, `read`, `update` |
| `robot` | `create`, `delete`, `list`, `read`, `update` |
| `scan-all` | `create`, `read`, `stop`, `update` |
| `scanner` | `create`, `delete`, `list`, `read`, `update` |
| `security-hub` | `create`, `list` |
| `system-volumes` | `read` |
| `user` | `create`, `delete`, `list`, `read`, `update` |
| `user-group` | `create`, `delete`, `list`, `read`, `update` |

### Extra project access for system robots

A system robot may also hold these permissions in `spec.projectPermissions`. A project robot cannot. A tenant must not be able to create a robot that edits or deletes the project its `HarborProject` owns.

| Resource | Actions |
| --- | --- |
| `project` | `delete`, `read`, `update` |
