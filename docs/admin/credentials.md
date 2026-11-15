# Which Harbor credential to use

The operator signs in to Harbor with a username and password (basic auth), stored in a Secret next to the `HarborConnection`. Two kinds of credential work.

## Use a system robot account (default)

Create a Harbor **system robot account** in Harbor's UI (Administration, Robot Accounts) and put its name and secret in the connection's Secret. This is the recommended credential. It also works when Harbor signs people in through OIDC, where local users other than `admin` don't exist.

Give the robot:

- `project`: create, read, update, delete, list
- `quota`: read, update
- `robot`: create, read, list, delete
- on all projects, every permission that any robot resource in your cluster may ask for (for CI robots usually `repository` push and pull). Harbor only lets a robot create robots with permissions it holds itself, and answers `403` otherwise.

Harbor doesn't let a robot change another robot or refresh its secret. When a robot resource changes, or its secret must be renewed, the operator deletes the Harbor robot and creates it again, then rewrites the robot's Secret. Pods that read the Secret get the new password; anything that copied the old one needs the new one.

## Or use a local admin user

A Harbor local user with the system administrator role can do everything, and the operator changes robots in place instead of replacing them. Use it if replacing robots is a problem for you.

The integration tests in `test/harborfixture` check the robot limits on Harbor 2.15.
