//go:build integration

package controller_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

var exampleBlock = regexp.MustCompile("(?s)<!-- example:project-and-robot -->\n```yaml\n(.*?)\n```")

func TestUserDocExampleBecomesReady(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "user", "example.md"))
	require.NoError(t, err)

	match := exampleBlock.FindSubmatch(page)
	require.NotNil(t, match, "docs/user/example.md has no marked example block")

	instance := harborfixture.FromEnv(t)
	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	// The example names its project, so make sure no earlier run left it behind.
	const projectName = "my-app"

	_ = admin.DeleteProject(context.Background(), projectName)

	t.Cleanup(func() { instance.DeleteProject(context.Background(), t, projectName) })

	cl := startOperator(t, true)
	namespace := newNamespace(t, cl, "docs")
	readyConnection(t, cl, instance, namespace, nil)

	for _, document := range bytes.Split(match[1], []byte("\n---\n")) {
		object := &unstructured.Unstructured{}
		require.NoError(t, yaml.Unmarshal(document, &object.Object))

		if object.GetNamespace() == "" {
			object.SetNamespace(namespace)
		}

		require.NoError(t, cl.Create(t.Context(), object))
	}

	awaitProject(t, cl, client.ObjectKey{Namespace: namespace, Name: "my-app"}, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	awaitRobot(t, cl, client.ObjectKey{Namespace: namespace, Name: "ci"}, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
}
