package main

// The manager's own permissions: leader election (a Lease, and Events recording leadership changes). The controllers
// declare theirs next to their reconcilers. `make generate` writes them all into config/rbac/role.yaml.

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
