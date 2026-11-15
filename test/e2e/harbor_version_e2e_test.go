//go:build e2e

package e2e_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// The matrix job installs the Harbor minor named by HARBOR_MINOR. This proves
// the suite runs against that release and not a different one.
func TestHarborRunsTheMatrixMinorRelease(t *testing.T) {
	instance := harborfixture.FromEnv(t)

	minor := os.Getenv("HARBOR_MINOR")
	require.NotEmpty(t, minor, "test/harbor/up.sh writes HARBOR_MINOR")

	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	info, err := admin.GetSystemInfo(t.Context())
	require.NoError(t, err)
	require.Truef(t, strings.HasPrefix(info.Version, "v"+minor+"."), "Harbor reports %q, want a v%s release", info.Version, minor)
}
