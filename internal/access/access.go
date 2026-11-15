// Package access decides whether an object's allowedNamespaces admits a namespace.
package access

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// Admits reports whether an object in ownerNamespace with the given allowedNamespaces admits candidate. The owner's
// own namespace and the listed names are admitted without reading anything; only a selector reads the candidate
// Namespace to match its labels. A selector follows Kubernetes semantics: an empty selector matches every namespace.
// A namespace that does not exist is not admitted by a selector.
func Admits(ctx context.Context, reader client.Reader, allowed v1alpha1.AllowedNamespaces, ownerNamespace, candidate string) (bool, error) {
	if candidate == ownerNamespace || slices.Contains(allowed.Names, candidate) {
		return true, nil
	}

	if allowed.Selector == nil {
		return false, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(allowed.Selector)
	if err != nil {
		return false, fmt.Errorf("invalid allowedNamespaces selector: %w", err)
	}

	namespace := &corev1.Namespace{}
	if err := reader.Get(ctx, client.ObjectKey{Name: candidate}, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, fmt.Errorf("reading namespace %q: %w", candidate, err)
	}

	return selector.Matches(labels.Set(namespace.Labels)), nil
}
