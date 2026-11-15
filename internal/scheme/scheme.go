// Package scheme builds the runtime scheme the operator's manager and its tests share: the Kubernetes built-in
// types and the harbor.soliddowant.dev API group.
package scheme

import (
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// New returns a scheme with the Kubernetes built-in types and every harbor-operator API version registered.
func New() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		harborv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}

	return scheme, nil
}
