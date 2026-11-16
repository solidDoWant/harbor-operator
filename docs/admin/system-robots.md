# System robots

For: admins who give tools access to many Harbor projects at once. You know Kubernetes, Helm, Flux and Harbor's web interface.

A system robot is a Harbor robot account that is not tied to one project. It can have system-level permissions and access to many projects. A `HarborSystemRobot` makes one in Harbor and writes its credentials into Secrets in its own namespace. Every field is in the [field reference](../user/crd-reference.md).

## The resource

```yaml
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborSystemRobot
metadata:
  name: deployer
  namespace: harbor-system
spec:
  connectionRef:
    name: harbor
  description: pulls from every project, pushes to two
  projectPermissions:
    - projects: ["*"]
      access:
        - resource: repository
          action: pull
    - projects: [team-a, team-b]
      access:
        - resource: repository
          action: push
```

The project `*` means every project. `spec.systemPermissions` gives system-level access. The allowed pairs are in [robot permissions](../user/robot-permissions.md#system-robot-permissions). The Secrets, `secret.rotate`, `duration`, `adopt` and `deletionPolicy` work as for a project robot. See [the CI robot example](../user/example.md).

## Who can create one

The connection's `systemAllowedNamespaces` must admit the namespace of the robot. The connection's own namespace is always admitted. If the namespace is not admitted, the robot shows `NotAllowed` and the operator creates nothing in Harbor. A namespace that only `allowedNamespaces` admits gets `NotAllowed` too.

A connection that is deleted waits until the system robots that it admits are gone.

## Status reasons for system robots

These reasons are in addition to the reasons in [Read the status](../user/example.md#read-the-status):

- `ProjectNotFound`: A project named in `projectPermissions` does not exist in Harbor. The message names the missing projects. The operator writes nothing and tries again when a `HarborProject` is created and at each resync.
- `OperatorCredential`: A connection to the same Harbor signs in as this robot, and that connection is the robot's own `connectionRef` or is in another namespace. The operator never updates, refreshes, replaces or deletes that robot. A connection that changes its own credential can lock the operator out of Harbor, and a namespace must not take over the credential of another namespace. A robot that you manage through an admin connection in its own namespace is not refused, even when another connection in that namespace signs in with the robot's Secret. Its updates and secret refreshes then change the same Harbor robot.
- `Forbidden`: Harbor refused an update, and the connection does not hold every permission that the robot asks for. The operator then does not replace the robot, so that the robot is not deleted without a new one. A robot with access to `*` is never replaced, because Harbor cannot report the permissions of the connection in every project at once.
