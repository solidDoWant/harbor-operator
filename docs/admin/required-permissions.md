# Permissions each resource kind needs

This page is generated from the Harbor client code by `make generate`. Do not edit it by hand.

The connection's Harbor credential must hold every permission listed for the kinds you use. A system scope permission applies to all of Harbor. A project scope permission applies in the projects the operator manages. A permission marked "user credentials only" is not needed when the credential is a robot, because the operator replaces a robot instead of updating it.

| Kind | Scope | Permission | Needed when |
| --- | --- | --- | --- |
| `HarborProject` | system | `project:create` | always |
| `HarborProject` | system | `quota:read` | always |
| `HarborProject` | system | `quota:update` | always |
| `HarborProject` | project | `project:delete` | always |
| `HarborProject` | project | `project:read` | always |
| `HarborProject` | project | `project:update` | always |
| `HarborProjectRobot` | project | `robot:create` | always |
| `HarborProjectRobot` | project | `robot:delete` | always |
| `HarborProjectRobot` | project | `robot:list` | always |
| `HarborProjectRobot` | project | `robot:read` | always |
| `HarborProjectRobot` | project | `robot:update` | user credentials only |
| `HarborRetentionPolicy` | project | `project:read` | always |
| `HarborRetentionPolicy` | project | `tag-retention:create` | always |
| `HarborRetentionPolicy` | project | `tag-retention:read` | always |
| `HarborRetentionPolicy` | project | `tag-retention:update` | always |
| `HarborImmutabilityPolicy` | project | `immutable-tag:create` | always |
| `HarborImmutabilityPolicy` | project | `immutable-tag:delete` | always |
| `HarborImmutabilityPolicy` | project | `immutable-tag:list` | always |
| `HarborImmutabilityPolicy` | project | `immutable-tag:update` | always |
| `HarborRegistry` | system | `registry:create` | always |
| `HarborRegistry` | system | `registry:delete` | always |
| `HarborRegistry` | system | `registry:list` | always |
| `HarborRegistry` | system | `registry:read` | always |
| `HarborRegistry` | system | `registry:update` | always |
| `HarborSystemRobot` | system | `robot:create` | always |
| `HarborSystemRobot` | system | `robot:delete` | always |
| `HarborSystemRobot` | system | `robot:list` | always |
| `HarborSystemRobot` | system | `robot:read` | always |
| `HarborSystemRobot` | system | `robot:update` | user credentials only |
| `HarborSystemRobot` | project | `project:read` | always |
