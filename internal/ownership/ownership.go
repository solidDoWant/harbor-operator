// Package ownership decides which custom resource owns a Harbor object when several claim it.
//
// Every claimant records a key in status.harborKey: the normalized identity of the Harbor it reaches, the object type
// and the object's Harbor name. Claimants are found through a field index on that key, so two connections that reach
// the same Harbor through differently written URLs share one ownership space.
package ownership

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// NormalizeURL returns the Harbor identity of a URL: its scheme, lower-cased host and port, without the scheme's
// default port, a trailing slash, user info, query or fragment.
func NormalizeURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parsing Harbor URL: %w", err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("harbor URL %q: scheme must be http or https", raw)
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("harbor URL %q has no host", raw)
	}

	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}

	normalized := url.URL{Scheme: scheme, Host: host, Path: strings.TrimRight(parsed.Path, "/")}
	switch {
	case port != "":
		normalized.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		// An IPv6 address keeps its brackets without a port.
		normalized.Host = "[" + host + "]"
	}

	return normalized.String(), nil
}

// ProjectKey is the status.harborKey of a project named projectName in the Harbor at identity.
func ProjectKey(identity, projectName string) string {
	return identity + "/projects/" + projectName
}

// RobotKey is the status.harborKey of a project robot named robotName in project projectName of the Harbor at
// identity.
func RobotKey(identity, projectName, robotName string) string {
	return ProjectKey(identity, projectName) + "/robots/" + robotName
}

// RetentionKey is the status.harborKey of the tag retention policy of project projectName in the Harbor at identity.
func RetentionKey(identity, projectName string) string {
	return ProjectKey(identity, projectName) + "/retention"
}

// ImmutabilityKey is the status.harborKey of the immutability rule set of project projectName in the Harbor at
// identity. A project has exactly one rule set, so the key names the project and the object type.
func ImmutabilityKey(identity, projectName string) string {
	return ProjectKey(identity, projectName) + "/immutability"
}

// RegistryKey is the status.harborKey of a registry endpoint named registryName in the Harbor at identity.
func RegistryKey(identity, registryName string) string {
	return identity + "/registries/" + registryName
}

// Claimant is what the ownership rule needs to know about a resource that claims a Harbor object.
type Claimant struct {
	UID               types.UID
	CreationTimestamp metav1.Time
	// Deleting is true once the resource has a deletion timestamp. It still owns its object until its finalizer is
	// removed.
	Deleting bool
	// HasID is true when the resource's status records the object's Harbor ID for this Harbor identity.
	HasID bool
	// Admitted is true when the resource's reference is admitted by the object it points at.
	Admitted bool
}

// ErrNoOwner is returned by Owner when no claimant counts.
var ErrNoOwner = errors.New("no claimant owns the Harbor object")

// Owner picks the owner among the claimants of one Harbor object. Claimants that are not admitted, or are being deleted
// without an ID, do not count. Among the rest a claimant that records the object's ID wins; otherwise, and between
// several with an ID, the oldest wins: earliest creation time, then the lowest UID.
func Owner(claimants []Claimant) (Claimant, error) {
	counted := slices.DeleteFunc(slices.Clone(claimants), func(claimant Claimant) bool {
		return !claimant.Admitted || (claimant.Deleting && !claimant.HasID)
	})
	if len(counted) == 0 {
		return Claimant{}, ErrNoOwner
	}

	return slices.MinFunc(counted, func(left, right Claimant) int {
		switch {
		case left.HasID != right.HasID:
			if left.HasID {
				return -1
			}

			return 1
		case !left.CreationTimestamp.Equal(&right.CreationTimestamp):
			if left.CreationTimestamp.Before(&right.CreationTimestamp) {
				return -1
			}

			return 1
		default:
			return strings.Compare(string(left.UID), string(right.UID))
		}
	}), nil
}
