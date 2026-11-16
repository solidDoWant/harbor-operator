# Which Harbor credential to use

The operator signs in to Harbor with a username and password (basic auth), stored in a Secret next to the `HarborConnection`. Two kinds of credential work.

## Use a system robot account (default)

Create a Harbor **system robot account** and put its name and secret in the connection's Secret. This is the recommended credential. It also works when Harbor signs people in through OIDC, where local users other than `admin` don't exist.

The robot needs permissions at two scopes:

| Scope | Resource | Actions |
|---|---|---|
| System | `project` | create, list |
| System | `quota` | read, update |
| System | `robot` | create, read, list, delete |
| All projects | `project` | read, update, delete |
| All projects | `robot` | create, read, list, delete |
| All projects | `tag-retention` | create, read, update, delete (for `HarborRetentionPolicy`) |
| All projects | `immutable-tag` | create, list, update, delete (for `HarborImmutabilityPolicy`) |
| All projects | every permission any robot resource in your cluster asks for | for CI robots usually `repository` push and pull. Harbor only lets a robot create robots with permissions it holds itself, and answers `403` otherwise. |

**Harbor's web interface can't create this robot.** It offers no `project` resource at the project scope. Create the robot through Harbor's API instead, signed in as a system administrator (the local `admin` user works, also in OIDC mode):

```sh
curl -sS -u 'admin:<password>' -H 'Content-Type: application/json' \
  https://<harbor>/api/v2.0/robots -d '{
  "name": "harbor-operator", "duration": -1, "level": "system", "disable": false,
  "permissions": [
    {"kind": "system", "namespace": "/", "access": [
      {"resource": "project", "action": "create"}, {"resource": "project", "action": "list"},
      {"resource": "quota", "action": "read"}, {"resource": "quota", "action": "update"},
      {"resource": "robot", "action": "create"}, {"resource": "robot", "action": "read"},
      {"resource": "robot", "action": "list"}, {"resource": "robot", "action": "delete"}]},
    {"kind": "project", "namespace": "*", "access": [
      {"resource": "project", "action": "read"}, {"resource": "project", "action": "update"},
      {"resource": "project", "action": "delete"},
      {"resource": "robot", "action": "create"}, {"resource": "robot", "action": "read"},
      {"resource": "robot", "action": "list"}, {"resource": "robot", "action": "delete"},
      {"resource": "repository", "action": "push"}, {"resource": "repository", "action": "pull"},
      {"resource": "tag-retention", "action": "create"}, {"resource": "tag-retention", "action": "read"},
      {"resource": "tag-retention", "action": "update"}, {"resource": "tag-retention", "action": "delete"},
      {"resource": "immutable-tag", "action": "create"}, {"resource": "immutable-tag", "action": "list"},
      {"resource": "immutable-tag", "action": "update"}, {"resource": "immutable-tag", "action": "delete"}]}
  ]}'
```

The answer holds the robot's full `name` (for example `robot$harbor-operator`) and its `secret`. Harbor shows the secret only once. Put them in the connection's Secret as `username` and `password`.

Harbor doesn't let a robot change another robot or refresh its secret. When a robot resource changes, or its secret must be renewed, the operator deletes the Harbor robot and creates it again, then rewrites the robot's Secret. Pods that read the Secret get the new password; anything that copied the old one needs the new one.

Before it deletes a Harbor robot, the operator asks Harbor which permissions the connection has in the project. If the robot resource asks for a permission that the connection does not have, the operator does not delete the robot. The robot resource shows `Forbidden` and lists the missing permissions.

## Or use a local admin user

A Harbor local user with the system administrator role can do everything, and the operator changes robots in place instead of replacing them. Use it if replacing robots is a problem for you.

The integration tests in `test/harborfixture` check the robot limits on Harbor 2.15.

## Check that the connection works

Run `kubectl get harborconnection`. The `Ready` column shows `True` when Harbor accepts the credentials. The `Reason` column tells you what is wrong when it does not:

| Reason | Meaning | Fix |
|---|---|---|
| `CredentialsInvalid` | The Secret is missing, is in another namespace, or has no `username` or `password` key. | Create the Secret in the connection's namespace with both keys. |
| `Unauthorized` or `Forbidden` | Harbor does not accept the username and password. | Correct the Secret. The operator does not retry at once. See below. |
| `HarborUnavailable` | The operator cannot reach Harbor, or Harbor answered with a server error. | Check the URL, the network and Harbor itself. The operator tries again after 5 seconds, then waits twice as long each time, up to 5 minutes. While the connection has this reason, the resources that use it also show `HarborUnavailable` and do not call Harbor. They recover by themselves when Harbor is back. |
| `DependentsExist` | You deleted the connection while a project, registry or system robot still uses it. | Delete those resources. The operator still uses the connection to apply their `deletionPolicy` in Harbor, and then the connection goes. The message lists up to five. Projects in namespaces the connection does not admit do not count. |

When you change the Secret, the operator checks the credentials again at once. You do not need to restart it.

When Harbor rejects the credentials (`Unauthorized` or `Forbidden`), the operator tries again only once per resync interval (10 minutes by default). Harbor locks a user account after several failed sign-ins. Fast retries with a wrong password would lock the account, and the people who use it. Other errors, such as an unreachable Harbor, are retried with a growing delay.
