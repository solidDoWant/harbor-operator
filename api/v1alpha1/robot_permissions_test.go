package v1alpha1_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// The CEL rule in the CRD is generated from a marker, so the Go list and the CRD must be compared by content.
func TestRobotPermissionsMatchCRD(t *testing.T) {
	crd, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", "harbor.soliddowant.dev_harborprojectrobots.yaml"))
	require.NoError(t, err)

	count := 0

	for resource, actions := range v1alpha1.RobotPermissions {
		for _, action := range actions {
			require.Contains(t, string(crd), "'"+resource+":"+action+"'")

			count++
		}
	}

	require.Greater(t, count, 0)
}

func TestRobotPermissionsDocListsEveryPair(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "user", "robot-permissions.md"))
	require.NoError(t, err)

	for resource, actions := range v1alpha1.RobotPermissions {
		for _, action := range actions {
			require.Contains(t, string(doc), "`"+resource+"`")
			require.Contains(t, string(doc), "`"+action+"`")
		}
	}
}

func TestSystemRobotPermissionsMatchCRD(t *testing.T) {
	crd, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", "harbor.soliddowant.dev_harborsystemrobots.yaml"))
	require.NoError(t, err)

	count := 0

	for resource, actions := range v1alpha1.SystemRobotPermissions {
		for _, action := range actions {
			require.Contains(t, string(crd), "'"+resource+":"+action+"'")

			count++
		}
	}

	require.Greater(t, count, 0)
}

func TestSystemRobotPermissionsDocListsEveryPair(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "user", "robot-permissions.md"))
	require.NoError(t, err)

	_, system, found := strings.Cut(string(doc), "## System robot permissions")
	require.True(t, found)

	for resource, actions := range v1alpha1.SystemRobotPermissions {
		for _, action := range actions {
			require.Contains(t, system, "| `"+resource+"` |")
			require.Contains(t, system, "`"+action+"`")
		}
	}
}
