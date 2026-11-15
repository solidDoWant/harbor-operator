# Which Harbor credential to use

The operator signs in to Harbor with a basic-auth credential. This page says which type to use.

## Use a local admin user

Use a Harbor local user that is a system administrator. This type works for every resource the operator manages, with no extra setup.

A Harbor system robot account can also sign in. It has a limit when the operator creates robot accounts:

- A robot can create project robots and system robots only if it has the `robot` `create` permission.
- A robot can only give permissions that it holds itself. If a new robot asks for a permission that the creator does not have, Harbor refuses the request with `403`.
- Harbor has no `robot` `update` permission for system robots.

For this reason, a system robot would need every permission that any robot resource in the cluster can ask for. Use an admin user unless you accept this limit.

A system robot with the `project` `create`, `quota` and `robot` permissions, and with the permissions it hands out, can create both types of robot. The integration tests in `test/harborfixture` check this on Harbor 2.15.
