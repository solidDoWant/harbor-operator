# Make a pull-through cache

For: chart authors who write harbor-operator resources. You know Kubernetes YAML and Helm.

A pull-through cache is a Harbor project that gets images from another registry when someone pulls them, and keeps a copy. Harbor calls it a proxy-cache project. You need two resources:

- A `HarborRegistry` tells Harbor where the upstream registry is. The upstream registry is the registry that has the images.
- A `HarborProject` with `proxyCache` makes the cache project.

Every field is in the [field reference](crd-reference.md).

## Before you start

A `HarborRegistry` is a Harbor-wide object. The connection must admit your namespace in `systemAllowedNamespaces`, not only in `allowedNamespaces`. By default only the namespace of the connection can create registries. Ask your admin. The admin page [namespace access](../admin/namespace-access.md) explains it.

## A cache for ghcr.io

```yaml
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborRegistry
metadata:
  name: ghcr
spec:
  connectionRef:
    name: harbor
  type: github-ghcr
  url: https://ghcr.io
  credentialsSecretRef:
    name: ghcr-credentials
  allowedNamespaces:
    names: [team-a]
---
apiVersion: v1
kind: Secret
metadata:
  name: ghcr-credentials
stringData:
  accessKey: my-github-user
  accessSecret: my-github-token
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborProject
metadata:
  name: ghcr-cache
spec:
  connectionRef:
    name: harbor
  public: true
  proxyCache:
    registryRef:
      name: ghcr
    speedLimitKbps: -1
```

To pull `ghcr.io/org/app:v1` through the cache, pull `<harbor host>/ghcr-cache/org/app:v1`.

`type` is the Harbor adapter type, for example `github-ghcr`, `quay`, `docker-hub` or `docker-registry`. Harbor checks the value. A value that Harbor does not know gives `InvalidSpec`. You cannot change `type` or the Harbor name later.

`credentialsSecretRef` is optional. The Secret must be in the namespace of the registry and must have the keys `accessKey` and `accessSecret`. Harbor never shows the stored secret, so the operator sends the credentials from the Secret every time it updates the registry. If you change the Secret, Harbor gets the new values. If you remove `credentialsSecretRef`, the operator does not change the credential in Harbor.

`allowedNamespaces` on the registry lists the namespaces whose projects can use it as an upstream. The namespace of the registry is always admitted.

`speedLimitKbps` limits the download speed from the upstream, in kilobits per second. `-1` means no limit. If you leave it out, the operator does not change it.

You set `proxyCache` when you create the project. You cannot add it, remove it or point it at another registry later. Harbor cannot change a project between a normal project and a cache.

## Read the status

Run `kubectl get harborregistry`. The `Health` column shows the last result of Harbor's check of the upstream:

- `healthy`: the upstream answered.
- `unhealthy`: Harbor could not reach the upstream, or the upstream refused the credentials.
- `unknown`: the check did not run.

The health does not change `Ready`. When the health changes, the operator emits a `HealthChanged` event.

These reasons are added to the ones in [the main example](example.md#read-the-status):

| Reason | Meaning | What you do |
|---|---|---|
| `CredentialsInvalid` | The registry's credentials Secret is missing, or it has no `accessKey` or `accessSecret`. | Create the Secret with both keys. The operator writes nothing to Harbor until then. |
| `RegistryNotReady` | The project waits for its registry. The registry does not exist yet, has no Harbor registry yet, or shows `Conflict` or `NotAllowed`. | Check the registry status. |
| `RegistryOnOtherHarbor` | The registry is in another Harbor than the project's connection. | Use a registry in the same Harbor. |
| `NotAllowed` | The registry does not admit the namespace of the project, or the connection does not admit the namespace of the registry in `systemAllowedNamespaces`. | Ask the owner of the registry or the admin. |
| `InvalidSpec` with "proxy-cache mode can't be changed" | You adopt a Harbor project, and one of the two is a cache and the other is not. | Use another project name. |

## Delete the resources

A registry waits while cache projects that it admits still use it. It shows `DependentsExist`. Delete the projects first. This also applies with `deletionPolicy: Orphan`.

If Harbor still uses the registry in a project or a replication rule that no resource manages, Harbor refuses the delete. The registry shows `DeleteBlocked` with the message from Harbor, and the operator tries again.
