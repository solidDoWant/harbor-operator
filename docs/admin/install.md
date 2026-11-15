# Install and connect

For: admins who install harbor-operator. You know Kubernetes, Helm and Harbor's web interface.

This page installs the operator and connects it to one Harbor. The operator does not install Harbor.

## Install the operator

1. Install the chart: `helm install harbor-operator oci://ghcr.io/soliddowant/charts/harbor-operator --namespace harbor-operator --create-namespace`.
2. Make sure that the Deployment runs: `kubectl -n harbor-operator get pods`.

[Chart values](chart-values.md) lists every value you can set.

## Limit the operator to some namespaces

By default, the operator watches all namespaces, and the chart gives it read and write access to Secrets and to its resources in all namespaces. To limit this access, set `watchNamespaces` to a list of namespaces:

```yaml
watchNamespaces:
  - team-a
  - team-b
```

With `watchNamespaces` set, the chart gives the operator a Role in each listed namespace and a Role in the release namespace for leader election. The operator keeps only these cluster-wide permissions:

- Read Namespaces. The operator reads the labels of namespaces for `allowedNamespaces` selectors.
- Create `tokenreviews` and `subjectaccessreviews`. The metrics endpoint uses them to check who reads the metrics.

Obey these rules when you use `watchNamespaces`:

- Create each listed namespace before you install or upgrade the chart.
- Put every `HarborConnection` in a listed namespace.
- Install the operator only once for each Harbor. Put all resources that use that Harbor in listed namespaces.
- Delete the resources in a namespace before you remove that namespace from the list.

The operator ignores resources in namespaces that are not listed. It does not show a status on them, and it does not change Harbor for them.

## Create the system robot in Harbor

1. In Harbor, open Administration, then Robot Accounts.
2. Create a system robot account with the permissions in [credentials](credentials.md).
3. Copy the robot name and secret. Harbor shows the secret once.

## Create the connection

1. Create a Secret with the robot name and secret in the namespace where you put the connection:

   ```
   kubectl -n harbor-system create secret generic harbor-creds --from-literal=username='robot$operator' --from-literal=password='<secret>'
   ```

2. Create the `HarborConnection` in the same namespace:

   ```yaml
   apiVersion: harbor.soliddowant.dev/v1alpha1
   kind: HarborConnection
   metadata:
     name: harbor
     namespace: harbor-system
   spec:
     url: https://harbor.example.com
     credentialsSecretRef:
       name: harbor-creds
     allowedNamespaces:
       selector:
         matchLabels:
           harbor.example.com/projects: "true"
   ```

3. Run `kubectl -n harbor-system get harborconnection`. The `Ready` column must be `True`. The Reason table in [credentials](credentials.md) explains the other values.

If Harbor uses a certificate from a private authority, put the PEM bundle in `spec.caBundle`. The field `spec.insecureSkipVerify` turns off the check. Use it only for tests.

The operator reads the credentials Secret only from the namespace of the connection. It writes the registry host into the robot Secrets. The host comes from Harbor's `external_url`. Set `spec.registryHost` to use another host.

The connection also sets defaults for the resources that use it: `defaults.resyncInterval` (how often the operator compares a resource with Harbor, default `10m`) and `defaults.deletionPolicy` (default `Delete`).

Next: [namespace access](namespace-access.md).

## Harden the install

The pod runs as non-root with a read-only root file system. Its memory request equals its limit, and it has no CPU limit.

Two options are off by default:

- `networkPolicy.enabled`: limits the pod to the API server, Harbor and cluster DNS. Set `networkPolicy.apiServer.cidrs` to the API server addresses, or the chart fails. Set `networkPolicy.harborEgress.peers` to where Harbor runs.
- `podDisruptionBudget.enabled`: allows one pod to be unavailable during a node drain.
