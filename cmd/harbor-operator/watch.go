package main

import (
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// cacheOptions limits the manager's cache to the namespaces in the comma-separated watchNamespaces. An empty list
// keeps the cache cluster-wide. With a list, the operator's RBAC covers only those namespaces, so a cluster-wide
// watch would be denied.
func cacheOptions(watchNamespaces string) cache.Options {
	namespaces := map[string]cache.Config{}

	for namespace := range strings.SplitSeq(watchNamespaces, ",") {
		if namespace = strings.TrimSpace(namespace); namespace != "" {
			namespaces[namespace] = cache.Config{}
		}
	}

	if len(namespaces) == 0 {
		return cache.Options{}
	}

	return cache.Options{DefaultNamespaces: namespaces}
}
