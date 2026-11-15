package secrets_test

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
	"github.com/solidDoWant/harbor-operator/internal/secrets"
)

func newInput(t *testing.T, spec harborv1alpha1.RobotSecretSpec) secrets.Input {
	t.Helper()

	runtimeScheme, err := scheme.New()
	require.NoError(t, err)

	return secrets.Input{
		Owner: &harborv1alpha1.HarborProjectRobot{
			ObjectMeta: metav1.ObjectMeta{Name: "ci", Namespace: "apps", UID: "uid-1"},
		},
		Namespace:    "apps",
		Spec:         spec,
		RegistryHost: "harbor.example.com:8443",
		HarborName:   "robot$proj+ci",
		Secret:       "s3cret",
		Scheme:       runtimeScheme,
	}
}

func TestRenderDefaultsWriteOnlyDockerConfigJSON(t *testing.T) {
	rendered, err := secrets.Render(newInput(t, harborv1alpha1.RobotSecretSpec{}))
	require.NoError(t, err)
	require.Len(t, rendered, 1)

	secret := rendered[0]
	assert.Equal(t, "ci-dockerconfig", secret.Name)
	assert.Equal(t, "apps", secret.Namespace)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, secret.Type)

	var config struct {
		Auths map[string]struct{ Username, Password, Auth string } `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config))
	require.Len(t, config.Auths, 1)
	entry := config.Auths["harbor.example.com:8443"]
	assert.Equal(t, "robot$proj+ci", entry.Username)
	assert.Equal(t, "s3cret", entry.Password)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("robot$proj+ci:s3cret")), entry.Auth)
}

func TestRenderOpaqueWithCustomNames(t *testing.T) {
	spec := harborv1alpha1.RobotSecretSpec{
		DockerConfigJSON: harborv1alpha1.SecretOutput{Name: "pull"},
		Opaque:           harborv1alpha1.SecretOutput{Enabled: ptr.To(true), Name: "creds"},
	}
	rendered, err := secrets.Render(newInput(t, spec))
	require.NoError(t, err)
	require.Len(t, rendered, 2)
	assert.Equal(t, "pull", rendered[0].Name)

	opaque := rendered[1]
	assert.Equal(t, "creds", opaque.Name)
	assert.Equal(t, corev1.SecretTypeOpaque, opaque.Type)
	assert.Equal(t, map[string][]byte{
		"username": []byte("robot$proj+ci"),
		"password": []byte("s3cret"),
		"registry": []byte("harbor.example.com:8443"),
	}, opaque.Data)
}

func TestRenderCanDisableDockerConfigJSON(t *testing.T) {
	spec := harborv1alpha1.RobotSecretSpec{
		DockerConfigJSON: harborv1alpha1.SecretOutput{Enabled: ptr.To(false)},
		Opaque:           harborv1alpha1.SecretOutput{Enabled: ptr.To(true)},
	}
	rendered, err := secrets.Render(newInput(t, spec))
	require.NoError(t, err)
	require.Len(t, rendered, 1)
	assert.Equal(t, "ci-credentials", rendered[0].Name)
}

func TestRenderedSecretCarriesOwnerLabelsAndHashOnly(t *testing.T) {
	input := newInput(t, harborv1alpha1.RobotSecretSpec{Opaque: harborv1alpha1.SecretOutput{Enabled: ptr.To(true)}})
	rendered, err := secrets.Render(input)
	require.NoError(t, err)

	for _, secret := range rendered {
		ref := metav1.GetControllerOf(secret)
		require.NotNil(t, ref)
		assert.Equal(t, "HarborProjectRobot", ref.Kind)
		assert.Equal(t, "ci", ref.Name)
		assert.Equal(t, secrets.ManagedByValue, secret.Labels[secrets.LabelManagedBy])
		// SHA-256 of "s3cret".
		assert.Equal(t, "1ec1c26b50d5d3c58d9583181af8076655fe00756bf7285940ba3670f99fcba0", secret.Annotations[secrets.AnnotationSecretHash])

		for _, value := range secret.Annotations {
			assert.NotContains(t, value, "s3cret")
		}

		for _, value := range secret.Labels {
			assert.NotContains(t, value, "s3cret")
		}
	}
}

func TestIsOwnedByDistinguishesForeignSecrets(t *testing.T) {
	input := newInput(t, harborv1alpha1.RobotSecretSpec{})
	rendered, err := secrets.Render(input)
	require.NoError(t, err)

	assert.True(t, secrets.IsOwnedBy(rendered[0], input.Owner))

	other := &harborv1alpha1.HarborProjectRobot{ObjectMeta: metav1.ObjectMeta{Name: "ci", Namespace: "apps", UID: "uid-2"}}
	assert.False(t, secrets.IsOwnedBy(rendered[0], other))
	assert.False(t, secrets.IsOwnedBy(&corev1.Secret{}, input.Owner))
}
