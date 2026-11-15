# Write a project and a CI robot

For: chart authors who write harbor-operator resources. You know Kubernetes YAML and Helm.

This page shows one complete example. It makes a Harbor project with a storage quota and vulnerability settings. It also makes a robot account that CI uses to push images. Every field is in the [field reference](crd-reference.md).

## Before you start

Your admin must give you a `HarborConnection`. In this example it is named `harbor` and is in the same namespace as your resources. If it is in another namespace, set `connectionRef.namespace`. The connection must admit your namespace. The admin page [namespace access](../admin/namespace-access.md) explains how.

## The resources

<!-- example:project-and-robot -->
```yaml
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborProject
metadata:
  name: my-app
spec:
  connectionRef:
    name: harbor
  public: false
  storageQuota: 10Gi
  vulnerability:
    preventVulnerableImages: true
    severity: high
    scanOnPush: true
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborProjectRobot
metadata:
  name: ci
spec:
  projectRef:
    name: my-app
  description: Pushes images from CI
  permissions:
    - resource: repository
      action: push
    - resource: repository
      action: pull
```

The project is named `my-app` in Harbor because `spec.name` is empty and the operator uses `metadata.name`. The name cannot change later. The operator sets only the fields that you write. If you leave a field out, the operator does not change it in Harbor.

If someone changes a field that you set, in the Harbor web interface for example, the operator changes it back.

The robot never expires, because `duration` defaults to `-1`. Each entry in `permissions` is a pair of a resource and an action. [Robot permissions](robot-permissions.md) lists the pairs that Harbor allows.

## The robot Secret

The operator writes the robot credentials into a Secret in the same namespace as the robot. By default it is a `kubernetes.io/dockerconfigjson` Secret named `ci-dockerconfig`. Use it as an image pull Secret or as the Docker config of a CI job.

To get a Secret with the keys `username`, `password` and `registry`, set `spec.secret.opaque.enabled: true`. Its default name is `ci-credentials`.

To renew the password, change `spec.secret.rotate` to any new string. The operator makes a new password and rewrites the Secrets.

If a Secret with the same name already exists and the robot does not own it, the operator does not touch it. The robot shows `SecretConflict`.

## Read the status

Run `kubectl get harborproject,harborprojectrobot`. The `Ready` column is `True` when Harbor matches your spec. The `Reason` column says what is wrong when it is not. Run `kubectl describe` to see the full message.

| Reason | Meaning | What you do |
|---|---|---|
| `Ready` | Harbor matches the spec. | Nothing. |
| `NotAllowed` | The connection or project does not admit your namespace. | Ask the admin to allow it. The operator changes nothing in Harbor. |
| `Conflict` | Another resource claims the same Harbor object, and it is older. | Delete one of the two resources. |
| `AlreadyExists` | A Harbor object with that name exists and you did not set `adopt: true`. | Choose another name, or set `adopt: true`. |
| `AdoptionNotAllowed` | You set `adopt: true` but the connection has `allowAdoption: false`. | Ask the admin to allow adoption. |
| `InvalidSpec` | Harbor rejected a value. The message has Harbor's reason. | Correct the value. The operator tries again when you change the resource. |
| `CredentialsInvalid`, `Unauthorized`, `HarborUnavailable` | The connection cannot sign in to Harbor, or cannot reach it. | Tell the admin. See [credentials](../admin/credentials.md). |
| `DependentsExist` | You deleted a project that robots still use. | Delete the robots first. |
| `DeleteBlocked` | Harbor refuses to delete a project that still has repositories. | Delete the repositories in Harbor. The operator tries again. |
| `ProjectNotReady` | A robot waits for its project to exist in Harbor. | Check the project status. |
| `SecretConflict` | A Secret with the robot's Secret name exists and the robot does not own it. | Rename the Secret in `spec.secret`, or delete the old one. |
| `Expired` | The robot passed its end date. | Create the robot again with a new name, or change `duration`. |

## Delete the resources

By default, deleting a resource also deletes its Harbor object. To keep the Harbor object, set `deletionPolicy: Orphan` on the resource before you delete it. The robot Secrets are always deleted with the robot.
