package access

import (
	"fmt"
	"slices"
	"strings"
)

// WatchedNamespaces lists the namespaces the operator watches. An empty list watches every namespace.
type WatchedNamespaces []string

// ParseWatchedNamespaces reads a comma-separated list of namespaces. Blank entries and duplicates are dropped.
func ParseWatchedNamespaces(list string) WatchedNamespaces {
	var watched WatchedNamespaces

	for namespace := range strings.SplitSeq(list, ",") {
		if namespace = strings.TrimSpace(namespace); namespace != "" && !slices.Contains(watched, namespace) {
			watched = append(watched, namespace)
		}
	}

	return watched
}

// Watches reports whether the operator watches namespace. A reference into a namespace it does not watch is not
// admitted: the operator's cache and RBAC do not reach that namespace, so it must not read or act on objects there.
// Callers check this before reading the referenced object, so the answer never depends on the cache's error.
func (w WatchedNamespaces) Watches(namespace string) bool {
	return len(w) == 0 || slices.Contains(w, namespace)
}

// NotWatchedMessage is the NotAllowed message for a reference into a namespace the operator does not watch.
func NotWatchedMessage(namespace string) string {
	return fmt.Sprintf("namespace %s is not watched by the operator", namespace)
}
