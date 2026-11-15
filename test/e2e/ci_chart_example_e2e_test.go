//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

func TestCIChartExampleRendersAndAppliesToReadyResources(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := kubeClient(t)
	internalURL := os.Getenv("HARBOR_INTERNAL_URL")
	require.NotEmpty(t, internalURL, "HARBOR_INTERNAL_URL is set by test/e2e/up.sh")

	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	name := harborfixture.UniqueName(t, "ci")
	connectionNamespace := name + "-conn"
	ciNamespace := name + "-ns"
	label := "harbor-operator-e2e/" + name
	ctx := t.Context()

	require.NoError(t, cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: connectionNamespace}}))
	t.Cleanup(func() {
		// t.Context() is already canceled here.
		cleanup := context.WithoutCancel(ctx)
		// Deleting the namespace removes the robot and policy first, so the operator can then remove the project.
		_ = cl.Delete(cleanup, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ciNamespace}})
		_ = wait.PollUntilContextTimeout(cleanup, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
			return apierrors.IsNotFound(cl.Get(ctx, client.ObjectKey{Name: ciNamespace}, &corev1.Namespace{})), nil
		})
		_ = admin.DeleteProject(cleanup, name)
		_ = cl.Delete(cleanup, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: connectionNamespace}})
	})

	require.NoError(t, cl.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: connectionNamespace, Name: "harbor-creds"},
		StringData: map[string]string{"username": instance.Username, "password": instance.Password},
	}))
	require.NoError(t, cl.Create(ctx, &harborv1alpha1.HarborConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: connectionNamespace, Name: "harbor"},
		Spec: harborv1alpha1.HarborConnectionSpec{
			URL:                  internalURL,
			CredentialsSecretRef: harborv1alpha1.LocalSecretReference{Name: "harbor-creds"},
			AllowedNamespaces:    harborv1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{label: "true"}}},
		},
	}))

	// The label key contains dots and slashes, so pass the map through a values file instead of --set.
	valuesFile := t.TempDir() + "/values.yaml"
	require.NoError(t, os.WriteFile(valuesFile, []byte(
		"namespace: "+ciNamespace+"\nprojectName: "+name+"\nconnection:\n  name: harbor\n  namespace: "+connectionNamespace+
			"\nnamespaceLabels:\n  \""+label+"\": \"true\"\n"), 0o600))

	rendered, err := exec.CommandContext(ctx, "helm", "template", "ci", "../../docs/user/examples/ci-chart", "--values", valuesFile).Output()
	require.NoError(t, err)

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)

	var applied []*unstructured.Unstructured

	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(&object.Object); err != nil {
			require.ErrorIs(t, err, io.EOF)

			break
		}

		if len(object.Object) == 0 {
			continue
		}

		require.NoError(t, cl.Create(ctx, object))

		applied = append(applied, object)
	}

	require.Len(t, applied, 4)

	for _, object := range applied {
		if object.GetKind() == "Namespace" {
			continue
		}

		require.Eventually(t, func() bool {
			current := &unstructured.Unstructured{}
			current.SetGroupVersionKind(object.GroupVersionKind())

			if err := cl.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
				return false
			}

			conditions, _, _ := unstructured.NestedSlice(current.Object, "status", "conditions")
			for _, raw := range conditions {
				condition, _ := raw.(map[string]any)
				if condition["type"] == harborv1alpha1.ConditionReady && condition["status"] == string(metav1.ConditionTrue) {
					return true
				}
			}

			return false
		}, 3*time.Minute, time.Second, "waiting for %s %s to be Ready", object.GetKind(), object.GetName())
	}

	secret := &corev1.Secret{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: ciNamespace, Name: "push-dockerconfig"}, secret))
	assert.NotEmpty(t, secret.Data[corev1.DockerConfigJsonKey])

	found, err := admin.GetProjectByName(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, name, found.Name)
}
