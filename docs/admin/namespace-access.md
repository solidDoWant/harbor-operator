# Namespace access, adoption and deletion

For: admins who decide what each namespace may do with the operator.

A resource can use a connection or a project only when that object admits the namespace of the resource. A namespace that is not admitted gets the reason `NotAllowed`, and the operator changes nothing in Harbor.

## Allow namespaces

`allowedNamespaces` has two fields: `names` (a list of namespace names) and `selector` (a label selector). A namespace is admitted when it is in `names` or its labels match `selector`. The namespace of the object itself is always admitted.

| Object | `allowedNamespaces` admits |
|---|---|
| `HarborConnection` | `HarborProject` resources that use the connection. |
| `HarborProject` | `HarborProjectRobot` resources that point at the project. |

An empty `allowedNamespaces` admits only the own namespace. A project owner can therefore let one other team make robots for a project without access to the connection.

When you change a namespace label, the operator checks the resources again.

## Ownership

Two resources cannot control one Harbor object. If two resources name the same project, the oldest one owns it. The other shows `Conflict` and changes nothing.

A registry that somebody renames in Harbor stays with the `HarborRegistry` that recorded its ID. The owner puts the old name back at its next check. Until then, a `HarborRegistry` that uses the new name shows `Conflict`. It does not adopt, change or delete the registry.

## Adoption

By default, a resource whose Harbor object already exists shows `AlreadyExists`. To take over an existing object:

1. Set `allowAdoption: true` on the connection.
2. Set `adopt: true` on the resource.

After adoption the operator makes the object match the spec, including values that were set in Harbor. Adoption gives a namespace control over an existing object. Leave `allowAdoption` off unless you need it. When the operator adopts a robot, it refreshes the robot secret at once, because Harbor never shows the old one.

Policies are different. A `HarborRetentionPolicy` has no `adopt` field. It takes control of the retention policy that the project already has in Harbor and replaces its rules. A project has only one retention policy, and the namespace already controls the project. If you admit a namespace on a project, that namespace can replace the project's retention rules.

Immutability policies have no `adopt` field. A `HarborImmutabilityPolicy` always takes over all the immutability rules of its project, and it removes rules that were made in Harbor. The project resource already owns the project, so a namespace that the project admits can change the rules of that project.

## Deletion

The operator deletes a Harbor object only when the resource that owns it is deleted and its `deletionPolicy` is `Delete`. The default is `Delete`. You can change the default for a whole connection in `spec.defaults.deletionPolicy`. A resource can set its own value.

- `Orphan` leaves the Harbor object in place and removes the resource.
- When a `HarborRetentionPolicy` is deleted with `Delete`, the operator removes the rules and the schedule from the project's retention policy.
- A project that has repositories cannot be deleted. The resource shows `DeleteBlocked` until the repositories are gone. The exception is `DeleteWithContents`, described in the next section.
- A project waits for robots and retention policies that it admits. A registry waits for proxy-cache projects that it admits, also with `Orphan`. A connection waits for projects that its `allowedNamespaces` admits, and for registries that its `systemAllowedNamespaces` admits. All show `DependentsExist`.
- You can delete a connection before the resources that use it. The connection waits with `DependentsExist`. When you then delete those resources, the operator still uses the connection to apply their `deletionPolicy` in Harbor. After the last one goes, the connection goes. If Harbor cannot be reached or rejects the credentials, the resources wait.
- A registry that a project or replication rule in Harbor still uses cannot be deleted. The resource shows `DeleteBlocked` with the message from Harbor.
- If the namespace of the resource is no longer admitted when it is deleted, the operator removes the resource and does not touch Harbor.

The operator never deletes an object that it did not create or adopt.

## Deleting the images of a project

A project with `deletionPolicy: DeleteWithContents` asks the operator to delete every repository in the project, and then the project. The operator does this only if the connection sets `allowContentDeletion: true`. The default is `false`. With `false`, the project waits with `DeleteBlocked` like a project with `Delete`, and the operator writes a `ContentDeletionNotAllowed` event.

Set `allowContentDeletion: true` only if you agree that the admitted namespaces can delete their images. The images are deleted permanently. Nobody can get them back.

Do not combine `allowContentDeletion` and `allowAdoption` unless you trust every admitted namespace with every project that it can adopt. Together, they let an admitted namespace adopt an existing project and then delete all of its images. `spec.defaults.deletionPolicy: DeleteWithContents` on the connection also applies to adopted projects.

The credential of the connection needs project scope `repository:list` and `repository:delete` for this. See [required permissions](required-permissions.md).

## Security model

- The operator signs in to Harbor with one credential per connection. Anyone who can create a `HarborProject` through a connection can create projects with that credential. Admit only namespaces that you trust.
- Credential Secrets are read only from the namespace of the connection.
- Robot Secrets are written only to the namespace of the robot resource. The operator never overwrites a Secret that the robot does not own.
- The operator never logs robot secrets or credentials, and never puts them in status, events or conditions.
- A robot can only have permissions that the system robot of the connection holds. See [credentials](credentials.md).
- Do not give namespace users write access to the `status` subresource of the operator's resources (for example `harborprojects/status`). The operator uses status as its own record. It is the proof that the operator created an object in Harbor. A user who can write status can take over Harbor projects, robots and immutability rules that the operator did not create. The chart gives this access only to the operator.
