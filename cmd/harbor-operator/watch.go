package main

import (
	"sigs.k8s.io/controller-runtime/pkg/cache"

	"github.com/solidDoWant/harbor-operator/internal/access"
)

// cacheOptions limits the manager's cache to the watched namespaces. An empty list keeps the cache cluster-wide.
// With a list, the operator's RBAC covers only those namespaces, so a cluster-wide watch would be denied.
func cacheOptions(watched access.WatchedNamespaces) cache.Options {
	if len(watched) == 0 {
		return cache.Options{}
	}

	namespaces := map[string]cache.Config{}
	for _, namespace := range watched {
		namespaces[namespace] = cache.Config{}
	}

	return cache.Options{DefaultNamespaces: namespaces}
}
