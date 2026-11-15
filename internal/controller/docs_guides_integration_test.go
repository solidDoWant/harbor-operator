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
)

// createDocExample applies every document of the YAML block that follows the marker in a user guide.
// replacements are exact text swaps applied to the block first, for values that cannot work in a test.
func createDocExample(t *testing.T, cl client.Client, namespace, page, marker string, replacements ...string) {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", "..", "docs", "user", page))
	require.NoError(t, err)

	block := regexp.MustCompile("(?s)<!-- example:" + regexp.QuoteMeta(marker) + " -->\n```yaml\n(.*?)\n```")
	match := block.FindSubmatch(content)
	require.NotNil(t, match, "docs/user/%s has no example block marked %q", page, marker)

	example := match[1]

	for i := 0; i < len(replacements); i += 2 {
		require.Contains(t, string(example), replacements[i], "the example no longer has the text the test replaces")
		example = bytes.ReplaceAll(example, []byte(replacements[i]), []byte(replacements[i+1]))
	}

	for _, document := range bytes.Split(example, []byte("\n---\n")) {
		object := &unstructured.Unstructured{}
		require.NoError(t, yaml.Unmarshal(document, &object.Object))

		if object.GetNamespace() == "" {
			object.SetNamespace(namespace)
		}

		require.NoError(t, cl.Create(t.Context(), object))
	}
}

func TestUserDocGuidesBecomeReady(t *testing.T) {
	// Serial: the guides name fixed Harbor projects and registries.
	setup := readyRegistrySetup(t, nil, nil)
	cl := setup.cl

	t.Run("the pull-through cache example", func(t *testing.T) {
		setup.registries = append(setup.registries, "ghcr")
		setup.projects = append(setup.projects, "ghcr-cache")

		// Harbor checks the upstream when it creates the registry, and the placeholder token in the guide is not a
		// real ghcr.io login. The test points the registry at the plain registry in the Harbor cluster instead.
		createDocExample(t, cl, setup.ownNamespace, "proxy-cache.md", "proxy-cache",
			"type: github-ghcr", "type: docker-registry",
			"url: https://ghcr.io", "url: "+setup.upstreamURL)

		awaitRegistry(t, cl, client.ObjectKey{Namespace: setup.ownNamespace, Name: "ghcr"}, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		awaitProject(t, cl, client.ObjectKey{Namespace: setup.ownNamespace, Name: "ghcr-cache"}, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	})

	// The policy guides name a project that the reader already has.
	newReadyProject := func(t *testing.T, name string) {
		t.Helper()

		setup.projects = append(setup.projects, name)

		project := newProject(setup.ownNamespace, name, setup.connection)
		project.Name = name
		require.NoError(t, cl.Create(context.Background(), project))
		awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	}

	t.Run("the retention example", func(t *testing.T) {
		newReadyProject(t, "app")
		createDocExample(t, cl, setup.ownNamespace, "retention.md", "retention")
		awaitRetention(t, cl, client.ObjectKey{Namespace: setup.ownNamespace, Name: "app"}, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	})

	t.Run("the immutability example", func(t *testing.T) {
		newReadyProject(t, "my-app")
		createDocExample(t, cl, setup.ownNamespace, "immutability.md", "immutability")
		awaitImmutabilityPolicy(t, cl, client.ObjectKey{Namespace: setup.ownNamespace, Name: "my-app"}, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	})
}
