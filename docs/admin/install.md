# Install and connect

For: admins who install harbor-operator. You know Kubernetes, Helm and Harbor's web interface.

This page installs the operator and connects it to one Harbor. The operator does not install Harbor.

## Install the operator

1. Install the chart: `helm install harbor-operator oci://ghcr.io/soliddowant/charts/harbor-operator --namespace harbor-operator --create-namespace`.
2. Make sure that the Deployment runs: `kubectl -n harbor-operator get pods`.

[Chart values](chart-values.md) lists every value you can set.

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
