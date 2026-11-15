# A Helm chart for one CI pipeline

For: chart authors who write harbor-operator resources. You know Kubernetes YAML and Helm.

This page shows a small Helm chart that sets up everything one CI pipeline needs in Harbor. It makes a namespace, a Harbor project, a robot that can push and pull images, and a tag retention policy. The project's robot Secret appears in the new namespace. The end-to-end tests render this chart with `helm template` and apply it, so the example works.

## Before you start

Your admin must give you a `HarborConnection`. Its `allowedNamespaces.selector` must match the label that the chart puts on the new namespace. The admin page [namespace access](../admin/namespace-access.md) explains how. Set `connection.name`, `connection.namespace` and `namespaceLabels` in the values to match.

The project name is a value, because a project name is unique in Harbor.

## The chart

The chart has three files.

<!-- include:docs/user/examples/ci-chart/Chart.yaml yaml -->
```yaml
apiVersion: v2
name: ci
description: A namespace, a Harbor project, a push robot and a retention policy for one CI pipeline
type: application
version: 0.1.0
```
<!-- /include -->

<!-- include:docs/user/examples/ci-chart/values.yaml yaml -->
```yaml
# The namespace the pipeline's resources live in.
namespace: ci-my-app
# The name of the project in Harbor. It must be unique in the Harbor instance.
projectName: my-app
# The HarborConnection your admin gave you.
connection:
  name: harbor
  namespace: harbor-operator-system
# The label that the connection's allowedNamespaces.selector matches.
namespaceLabels:
  harbor.example.com/ci: "true"
```
<!-- /include -->

<!-- include:docs/user/examples/ci-chart/templates/ci.yaml yaml -->
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Values.namespace }}
  labels:
    {{- toYaml .Values.namespaceLabels | nindent 4 }}
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborProject
metadata:
  name: {{ .Values.projectName }}
  namespace: {{ .Values.namespace }}
spec:
  connectionRef:
    name: {{ .Values.connection.name }}
    namespace: {{ .Values.connection.namespace }}
  name: {{ .Values.projectName }}
  public: false
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborProjectRobot
metadata:
  name: push
  namespace: {{ .Values.namespace }}
spec:
  projectRef:
    name: {{ .Values.projectName }}
  description: Pushes images from CI
  permissions:
    - resource: repository
      action: push
    - resource: repository
      action: pull
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborRetentionPolicy
metadata:
  name: {{ .Values.projectName }}
  namespace: {{ .Values.namespace }}
spec:
  projectRef:
    name: {{ .Values.projectName }}
  schedule: "0 0 0 * * *"
  rules:
    - template: latestPushedK
      count: 10
      repositories: {decoration: repoMatches, pattern: "**"}
      tags: {decoration: matches, pattern: "**"}
```
<!-- /include -->

## What happens

The robot and the retention policy name the project with `projectRef`. They wait for the project to be `Ready`. The robot Secret is `push-dockerconfig` in the new namespace. Use it in a CI job as the Docker config. See [Write a project and a CI robot](example.md) for the Secret and the status reasons, and [Keep only the tags you need](retention.md) for the retention rules.
