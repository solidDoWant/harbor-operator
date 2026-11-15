# Keep only the tags you need

A `HarborRetentionPolicy` sets the tag retention policy of one Harbor project. Tag retention is a set of rules that Harbor runs to delete old images. Harbor keeps the images that match at least one rule and deletes the others.

<!-- example:retention -->
```yaml
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborRetentionPolicy
metadata:
  name: app
spec:
  projectRef:
    name: app
  schedule: "0 0 0 * * *"
  rules:
    - template: latestPushedK
      count: 5
      repositories: {decoration: repoMatches, pattern: "**"}
      tags: {decoration: matches, pattern: "v*"}
    - template: nDaysSinceLastPull
      count: 30
      repositories: {decoration: repoMatches, pattern: "**"}
      tags: {decoration: matches, pattern: "**"}
```

This policy keeps the 5 most recently pushed `v*` tags and every image that was pulled in the last 30 days. Harbor runs it every day at midnight.

The fields:

- `schedule` is a Harbor cron expression with six fields. The first field is the seconds. If you leave it empty, the policy runs only when somebody starts it in Harbor.
- `template` is one of `latestPushedK`, `latestPulledN`, `nDaysSinceLastPush`, `nDaysSinceLastPull` or `always`. `count` is the number of images or days. The `always` template takes no `count`.
- `tags.untagged: true` also selects images that have no tag.
- You can have up to 15 rules. The order of the rules is kept.

A project has one retention policy. The resource controls all of it: the operator removes rules that somebody adds or changes in Harbor's UI. If a second `HarborRetentionPolicy` names the same project, it shows `Conflict` and changes nothing.

When you delete the resource, the operator removes the rules and the schedule from Harbor. If `deletionPolicy` is `Orphan`, the rules stay in Harbor.

## Status reasons

| Reason | Meaning |
|---|---|
| `Ready` | Harbor's policy matches the spec. |
| `ProjectNotReady` | The `HarborProject` does not exist yet, or it has no Harbor project yet. |
| `NotAllowed` | The project's `allowedNamespaces` does not include your namespace. |
| `Conflict` | Another `HarborRetentionPolicy` controls this project's policy. |
| `InvalidSpec` | The `schedule` is not a cron expression with six fields, or Harbor did not accept the rules. The message tells you why. |
