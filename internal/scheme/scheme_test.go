package scheme_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

func TestNew(t *testing.T) {
	built, err := scheme.New()
	require.NoError(t, err)

	tests := []struct {
		name         string
		groupVersion schema.GroupVersion
	}{
		{name: "harbor API group", groupVersion: harborv1alpha1.GroupVersion},
		{name: "core API group", groupVersion: corev1.SchemeGroupVersion},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.True(t, built.IsVersionRegistered(test.groupVersion), "%s is not registered", test.groupVersion)
		})
	}
}

func TestNewRegistersSecrets(t *testing.T) {
	built, err := scheme.New()
	require.NoError(t, err)

	kinds, _, err := built.ObjectKinds(&corev1.Secret{})
	require.NoError(t, err)
	assert.Contains(t, kinds, corev1.SchemeGroupVersion.WithKind("Secret"))
}
