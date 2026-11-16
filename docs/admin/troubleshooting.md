# Troubleshoot condition reasons

For: admins and chart authors who see a resource that is not `Ready`.

Every resource has a `Ready` condition. When it is not `True`, its `reason` says why. Run `kubectl get <kind>` to see the reason, and `kubectl describe <kind> <name>` to see the full message and the events. The operator never puts a secret in a message.

Each entry below has the cause, what to check, and the fix. Resource field names are in the [field reference](../user/crd-reference.md).

## Sign-in and connection

### `CredentialsInvalid`

Cause: the credential Secret of the `HarborConnection` is missing, is in another namespace, or has no `username` or `password` key.

Check: `kubectl get secret` in the namespace of the connection.

Fix: create the Secret there with both keys. The operator checks again at once. See [credentials](credentials.md).

### `Unauthorized`

Cause: Harbor does not accept the username and password.

Check: the Secret against the account in Harbor. Harbor locks an account after several failed sign-ins, so the operator tries again only once per resync interval.

Fix: correct the Secret. The operator checks again at once.

### `Forbidden`

Cause: Harbor accepted the sign-in but refuses the action. For a robot, the connection lacks a permission the spec asks for, and the only way to apply the change is to delete the robot and create it again. The operator does not do that.

Check: the message lists the missing permissions.

Fix: remove those permissions from the spec, or give them to the Harbor account of the connection.

### `HarborUnavailable`

Cause: the operator cannot reach Harbor, or Harbor answered with a server error.

Check: the URL in `spec.url`, DNS, the network policy, the certificate (`caBundle`) and Harbor's own health.

Fix: repair the network or Harbor. The operator tries again with a growing delay.

### `OperatorCredential`

Cause: a `HarborSystemRobot` names a Harbor robot that a connection signs in as. That connection is the robot's own `connectionRef`, or it is in another namespace. The operator never manages that robot.

Fix: choose another name for the system robot. To manage the operator's own robot, use an admin connection in the robot's namespace as its `connectionRef`. See [system robots](system-robots.md).

## Access and ownership

### `NotAllowed`

Cause: the connection, project or registry you refer to does not admit your namespace in its `allowedNamespaces` (or `systemAllowedNamespaces` for Harbor-wide kinds). The operator changes nothing in Harbor.

Check: the `allowedNamespaces` of the object you refer to, and the labels of your namespace.

Fix: an admin adds the namespace or its label. See [namespace access](namespace-access.md).

### `Conflict`

Cause: another, older resource claims the same Harbor object. Or Harbor reports a conflict, for example a name that another project in Harbor already uses.

Check: the message names the other resource.

Fix: delete one of the two resources, or change the name.

### `AlreadyExists`

Cause: a Harbor object with that name exists, and the resource did not set `adopt: true`. The operator never takes over an object it did not create.

Fix: choose another name, or set `adopt: true` if the object is yours.

### `AdoptionNotAllowed`

Cause: the resource has `adopt: true`, but its connection has `allowAdoption: false`.

Fix: an admin sets `allowAdoption: true` on the connection, or you remove `adopt`.

### `SecretConflict`

Cause: a Kubernetes Secret with the robot's Secret name exists and the robot does not own it. The operator never overwrites it.

Fix: rename the Secret in the robot's `spec.secret`, or delete the old Secret.

## Spec and references

### `InvalidSpec`

Cause: Harbor rejected a value in the spec. The message has Harbor's reason.

Fix: correct the value. The operator tries again when you change the resource.

### `NotFound`

Cause: a resource you refer to (the connection, project or registry) does not exist. This also appears with the `ReferenceResolved` condition set to `False`.

Check: the name and namespace in the reference.

Fix: create the missing resource or correct the reference.

### `ReferenceResolved`

Cause: this is the reason of the `ReferenceResolved` condition when the reference is found and allowed. It means the reference is fine.

Fix: nothing. If `Ready` is not `True`, look at its reason instead.

### `ProjectNotReady`

Cause: a robot or policy waits for its project to exist in Harbor, or the project is itself in error.

Check: `kubectl get harborproject` and the reason of the project.

Fix: fix the project. The robot or policy continues by itself.

### `ProjectNotFound`

Cause: a `HarborSystemRobot` lists Harbor projects that do not exist in Harbor.

Check: the message lists the names.

Fix: create the projects, or remove them from the robot. The operator tries again when the projects appear.

### `RegistryNotReady`

Cause: a proxy-cache `HarborProject` refers to a `HarborRegistry` that does not exist, has no Harbor registry yet, or is in `Conflict` or `NotAllowed`.

Check: `kubectl get harborregistry`.

Fix: fix the registry. See [proxy cache](../user/proxy-cache.md).

### `RegistryOnOtherHarbor`

Cause: the `HarborRegistry` lives in another Harbor than the connection of the project.

Fix: use a registry on the same Harbor, or change the connection of the project.

## Deletion and lifetime

### `DependentsExist`

Cause: you deleted a connection, project or registry that other resources still use. Or you deleted a robot, or disabled or renamed one of its Secrets, and a connection in the same namespace signs in with that Secret.

Check: the message lists up to five of them. For a robot, the message names the connections.

Fix: delete those resources. For a robot, delete the connections or point their `credentialsSecretRef` at another Secret. The deletion then finishes. The operator keeps the Secret until then.

### `DeleteBlocked`

Cause: Harbor refuses to delete the object, for example a project that still has repositories.

Fix: remove the repositories (or other content) in Harbor. The operator tries again.

With `deletionPolicy: DeleteWithContents`, the operator deletes the repositories itself. The message then shows how many repositories are left. A tag that an immutability rule protects also causes `DeleteBlocked`. Remove that rule in Harbor by hand.

### `ContentDeletionNotAllowed`

Cause: a project with `deletionPolicy: DeleteWithContents` was deleted, but its connection does not set `allowContentDeletion: true`. This is an event reason. The `Ready` condition shows `DeleteBlocked`, and its message names the connection. The operator deletes no image.

Fix: an admin sets `allowContentDeletion: true` on the connection, or you delete the images in Harbor. See [namespace access](namespace-access.md#deleting-the-images-of-a-project).

### `Expired`

Cause: the robot passed its end date.

Fix: create the robot again with a new name, or change `duration`.

## Healthy

### `Ready`

Cause: Harbor matches the spec.

Fix: nothing.
