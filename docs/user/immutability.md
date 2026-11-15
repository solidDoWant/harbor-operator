# Protect tags from being overwritten

For: chart authors who write harbor-operator resources. You know Kubernetes YAML and Helm.

An immutable tag is a tag that nobody can push over or delete. A `HarborImmutabilityPolicy` sets the tag immutability rules of one Harbor project. Every field is in the [field reference](crd-reference.md).

## The resource

```yaml
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborImmutabilityPolicy
metadata:
  name: my-app
spec:
  projectRef:
    name: my-app
  rules:
    - repositories:
        decoration: repoMatches
        pattern: "**"
      tags:
        decoration: matches
        pattern: "v*"
```

With this rule, a push over an existing `v1.0.0` tag fails. A push over `latest` still works, because `latest` does not match `v*`.

Each rule has a repository selector and a tag selector. A selector has a pattern, for example `**` for all names. Its `decoration` says if the rule applies to the names that match (`repoMatches`, `matches`) or to the names that do not match (`repoExcludes`, `excludes`). Set `disabled: true` to keep a rule but stop it. A policy has 15 rules at most, and two rules cannot have the same pair of selectors.

## Who owns the rules

The policy owns all the immutability rules of the project. If someone adds, changes or removes a rule in the Harbor web interface, the operator puts back the list from your spec. The operator also removes rules that existed in Harbor before you created the policy.

A project has one immutability policy. If you create a second one for the same project, it shows `Conflict` and changes nothing. The project must admit the namespace of the policy. If it does not, the policy shows `NotAllowed`. The other status reasons are in [Read the status](example.md#read-the-status).

## Delete the policy

If you delete the policy, the operator deletes all the immutability rules of the project. To keep the rules in Harbor, set `deletionPolicy: Orphan` before you delete the policy. A project that still has an immutability policy waits for the policy to go before the operator deletes it.
