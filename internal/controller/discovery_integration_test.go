//go:build integration

package controller_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/discovery"
)

func TestDiscoveryListsCategoryAndShortNamesForEveryKind(t *testing.T) {
	t.Parallel()

	if sharedRestConfig == nil {
		t.Skipf("envtest is not available: %v", sharedStartErr)
	}

	shortNames := map[string]string{
		"harborprojects":             "hproj",
		"harborprojectrobots":        "hprobot",
		"harborsystemrobots":         "hsrobot",
		"harborconnections":          "hconn",
		"harborregistries":           "hreg",
		"harborretentionpolicies":    "hret",
		"harborimmutabilitypolicies": "himm",
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(sharedRestConfig)
	require.NoError(t, err)

	resources, err := discoveryClient.ServerResourcesForGroupVersion("harbor.soliddowant.dev/v1alpha1")
	require.NoError(t, err)

	seen := map[string]bool{}

	for _, resource := range resources.APIResources {
		want, ok := shortNames[resource.Name]
		if !ok {
			continue
		}

		seen[resource.Name] = true

		assert.Equal(t, []string{want}, resource.ShortNames, resource.Name)
		assert.Equal(t, []string{"harbor"}, resource.Categories, resource.Name)
	}

	assert.Len(t, seen, len(shortNames))
}
