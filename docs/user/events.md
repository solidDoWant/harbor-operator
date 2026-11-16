# Kubernetes events

The operator writes a Kubernetes event on a resource each time something changes in Harbor. Run `kubectl describe` on the resource, or `kubectl events --for <kind>/<name>`, to read them.

| Event reason | Type | When |
|---|---|---|
| `Created` | Normal | The operator created the Harbor object. |
| `Updated` | Normal | The operator changed the Harbor object to match the spec. This includes putting back a change made in Harbor. The message names the fields, never their values. |
| `Recreated` | Normal | The Harbor object was deleted in Harbor, and the operator created it again. |
| `Replaced` | Normal | A robot was deleted and created again, because Harbor did not allow an update. |
| `Adopted` | Normal | The operator took over a Harbor object that already existed. |
| `Deleted` | Normal | The resource was deleted, and the operator deleted the Harbor object. |
| `Orphaned` | Normal | The resource was deleted, and the Harbor object stays because the deletion policy is `Orphan`. |
| `HealthChanged` | Normal or Warning | The upstream health of a registry changed. |
| `ContentDeletionNotAllowed` | Warning | A project with `deletionPolicy: DeleteWithContents` was deleted, but its connection does not allow content deletion. The images stay. |
| A condition reason, such as `NotAllowed` or `HarborUnavailable` | Warning | The `Ready` condition changed to a failure. The message is the condition message. |

Events never contain secrets. Kubernetes keeps events for a short time only, so read the `Ready` condition for the current state.
