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

## Adoption

By default, a resource whose Harbor object already exists shows `AlreadyExists`. To take over an existing object:

1. Set `allowAdoption: true` on the connection.
2. Set `adopt: true` on the resource.

After adoption the operator makes the object match the spec, including values that were set in Harbor. Adoption gives a namespace control over an existing object. Leave `allowAdoption` off unless you need it. When the operator adopts a robot, it refreshes the robot secret at once, because Harbor never shows the old one.

Policies are different. A `HarborRetentionPolicy` has no `adopt` field. It takes control of the retention policy that the project already has in Harbor and replaces its rules. A project has only one retention policy, and the namespace already controls the project. If you admit a namespace on a project, that namespace can replace the project's retention rules.

## Deletion

The operator deletes a Harbor object only when the resource that owns it is deleted and its `deletionPolicy` is `Delete`. The default is `Delete`. You can change the default for a whole connection in `spec.defaults.deletionPolicy`. A resource can set its own value.

- `Orphan` leaves the Harbor object in place and removes the resource.
- When a `HarborRetentionPolicy` is deleted with `Delete`, the operator removes the rules and the schedule from the project's retention policy.
- A project that has repositories cannot be deleted. The resource shows `DeleteBlocked` until the repositories are gone.
- A project waits for robots and retention policies that it admits. A connection waits for projects that it admits. Both show `DependentsExist`.
- If the namespace of the resource is no longer admitted when it is deleted, the operator removes the resource and does not touch Harbor.

The operator never deletes an object that it did not create or adopt.

## Security model

- The operator signs in to Harbor with one credential per connection. Anyone who can create a `HarborProject` through a connection can create projects with that credential. Admit only namespaces that you trust.
- Credential Secrets are read only from the namespace of the connection.
- Robot Secrets are written only to the namespace of the robot resource. The operator never overwrites a Secret that the robot does not own.
- The operator never logs robot secrets or credentials, and never puts them in status, events or conditions.
- A robot can only have permissions that the system robot of the connection holds. See [credentials](credentials.md).
- Do not give namespace users write access to the `status` subresource of the operator's resources (for example `harborprojects/status`). The operator uses status as its own record. It is the proof that the operator created an object in Harbor. A user who can write status can take over Harbor projects and robots that the operator did not create. The chart gives this access only to the operator.
