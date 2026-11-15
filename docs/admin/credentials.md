# Which Harbor credential to use

The operator signs in to Harbor with a username and password (basic auth), stored in a Secret next to the `HarborConnection`. Two kinds of credential work.

## Use a system robot account (default)

Create a Harbor **system robot account** in Harbor's UI (Administration, Robot Accounts) and put its name and secret in the connection's Secret. This is the recommended credential. It also works when Harbor signs people in through OIDC, where local users other than `admin` don't exist.

Give the robot:

- `project`: create, read, update, delete, list
- `quota`: read, update
- `robot`: create, read, list, delete
- `tag-retention`: create, read, update, delete, on all projects. The operator needs them for `HarborRetentionPolicy` resources.
- on all projects, every permission that any robot resource in your cluster may ask for (for CI robots usually `repository` push and pull). Harbor only lets a robot create robots with permissions it holds itself, and answers `403` otherwise.

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
| `Unauthorized` | Harbor does not accept the username and password. | Correct the Secret. |
| `HarborUnavailable` | The operator cannot reach Harbor, or Harbor answered with a server error. | Check the URL, the network and Harbor itself. The operator tries again with a growing delay. |
| `DependentsExist` | You deleted the connection while a project still uses it. | Delete those projects first. The message lists up to five. Projects in namespaces the connection does not admit do not count. |

When you change the Secret, the operator checks the credentials again at once. You do not need to restart it.
