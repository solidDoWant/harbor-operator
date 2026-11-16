package permtable

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialsDocLeadsWithTheRootRobotPattern(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/admin/credentials.md")
	require.NoError(t, err)

	page := string(raw)

	rootRobot := strings.Index(page, "root-robot pattern")
	handMade := strings.Index(page, "## Or use a robot you made by hand")

	require.NotEqual(t, -1, rootRobot)
	require.NotEqual(t, -1, handMade)
	assert.Less(t, rootRobot, handMade, "the root-robot pattern comes before the hand-made robot")

	for _, want := range []string{
		"HARBOR_ADMIN_PASSWORD",
		"existingSecretAdminPassword",
		"<release>-core",
		"initial** admin password",
		"required-permissions.md",
		"adopt: true",
		"`allowAdoption` off",
		"same as permission to read every Secret",
		"must not come from a robot that the same connection manages",
		"renders `<release>-core`", // the chart re-renders on upgrade
		"default",
	} {
		assert.Contains(t, page, want)
	}
}
