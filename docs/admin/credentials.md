# Which Harbor credential to use

The operator signs in to Harbor with a username and password (basic auth), stored in a Secret next to the `HarborConnection`. The best way to get that credential is to let the operator make its own robot account. You can also use a robot account or a local user that you made by hand.

## Let the operator manage its own robot (root-robot pattern)

Harbor's web interface can't make a robot with the permissions the operator needs. So the operator makes its own robot, with the same `HarborSystemRobot` kind that you use for any other system robot. Two connections take part:

1. An **admin connection** signs in as Harbor's `admin` user, with the password from Harbor's own Secret. Only the `HarborSystemRobot` below uses it.
2. The **main connection** signs in as the robot. Every other resource uses it.

```yaml
# The admin connection. It uses the Secret that Harbor's chart already made.
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborConnection
metadata: {name: harbor-admin, namespace: harbor}
spec:
  url: http://harbor-core.harbor.svc.cluster.local
  credentialsSecretRef:
    name: harbor-admin-credentials   # see "Where Harbor keeps the admin password"
    username: admin                  # a fixed username, not read from the Secret
    passwordKey: HARBOR_ADMIN_PASSWORD
---
# The operator's own robot, managed through the admin connection.
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborSystemRobot
metadata: {name: harbor-operator, namespace: harbor}
spec:
  connectionRef: {name: harbor-admin}
  systemPermissions: [...]    # the list in "Permissions the robot needs"
  projectPermissions: [...]
  secret:
    dockerConfigJSON: {enabled: false}
    opaque: {enabled: true, name: harbor-operator-credentials}
---
# The main connection. Every other resource uses it.
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborConnection
metadata: {name: harbor, namespace: harbor}
spec:
  url: http://harbor-core.harbor.svc.cluster.local
  credentialsSecretRef: {name: harbor-operator-credentials}
```

You can apply all three objects together on an empty cluster. The main connection shows `CredentialsInvalid` until the robot's Secret exists. Then it turns `Ready` by itself. You do not restart the operator, and you do not need to order the objects in Flux.

The admin connection can update robots in place, so the operator can change its own robot and renew its secret without a replacement. Keep the admin connection in the namespace of the robot, and leave its `allowedNamespaces` and `systemAllowedNamespaces` at their default. The default admits only that namespace. That limit is what contains the power of the admin password.

### Where Harbor keeps the admin password

Harbor's chart stores the admin password in a Secret, under the key `HARBOR_ADMIN_PASSWORD`:

- When you set `existingSecretAdminPassword` in Harbor's chart values, it is the Secret with that name.
- Otherwise it is the Secret `<release>-core`, where `<release>` is the Helm release name of Harbor.

The Secret holds only the **initial** admin password. If someone changed the password in Harbor's web interface, the Secret is out of date. The admin connection then turns `Unauthorized`. The operator keeps working with its current robot. It needs the admin connection again only when the robot must be replaced or its secret renewed.

Harbor's chart renders `<release>-core` again on every upgrade. If the password was changed in the web interface, the upgrade writes the old password back. To avoid this, make your own Secret with the current password, and point the admin connection at it.

### Permissions the robot needs

The robot needs every permission for the kinds you use. The list is made from the operator's code, so it stays correct. See [Permissions each resource kind needs](required-permissions.md). Add every permission that any robot resource in your cluster asks for, for CI robots usually `repository` push and pull. Harbor lets a robot create robots only with permissions it holds itself, and answers `403` otherwise.

The operator checks the permissions of the connection against the kinds that use it. The `PermissionsSufficient` condition lists what is missing. The check covers only projects that exist.

### Move from a robot you made by hand

1. Set `allowAdoption: true` on the admin connection.
2. In one change, create the `HarborSystemRobot` with `adopt: true` and the name of your existing robot. Let it write a **new** Secret name. In the same change, point the main connection's `credentialsSecretRef` at that new Secret.
3. Adoption refreshes the robot secret at once, because Harbor never shows the old one. The main connection shows `Unauthorized` until the operator writes the new Secret and the connection checks it again. This takes a few seconds.
4. Check that `kubectl get harborconnection` shows the main connection `Ready`.
5. Set `allowAdoption` back to off on the admin connection.

### Security notes

- Permission to create a `HarborConnection` in a namespace is the same as permission to read every Secret in it. A connection can send any key of any Secret there to any URL. Grant it only where you already grant Secret read.
- A connection's credential must not come from a robot that the same connection manages, directly or through other resources. Deleting everything in bulk would deadlock. The operator also refuses to manage a robot that its own managing connection signs in as.
- A robot whose Secret a connection uses waits on deletion with `DependentsExist` until you delete or change that connection.
- Turn `allowAdoption` off on the admin connection after you migrate.
- Leave the admission lists of the admin connection at their default.

## Or use a robot you made by hand

You can still make the robot yourself. It needs the permissions in [Permissions each resource kind needs](required-permissions.md), at two scopes. Create it through Harbor's API as a system administrator (the local `admin` user works, also in OIDC mode), because the web interface offers no `project` resource at the project scope.

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

Example call:

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
