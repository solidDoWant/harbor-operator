//go:build integration

package harbor_test

import (
	"context"
	"testing"
	"time"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

func newClient(t *testing.T) (*harbor.Client, *harborfixture.Harbor) {
	t.Helper()

	instance := harborfixture.FromEnv(t)
	client, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	return client, instance
}

func ptr[T any](value T) *T { return &value }

func TestProjectLifecycleRoundTrips(t *testing.T) {
	client, _ := newClient(t)
	ctx := t.Context()
	name := harborfixture.UniqueName(t, "op")
	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	settings := harbor.ProjectSettings{
		Public:                  ptr(true),
		PreventVulnerableImages: ptr(true),
		Severity:                ptr("high"),
		ScanOnPush:              ptr(true),
		UseSystemAllowlist:      ptr(false),
		CVEAllowlist:            &harbor.CVEAllowlist{Items: []string{"CVE-2024-0001", "CVE-2024-0002"}, ExpiresAt: &expires},
	}

	created, err := client.CreateProject(ctx, name, settings, ptr(int64(1<<30)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.DeleteProject(t.Context(), name) })
	assert.Equal(t, name, created.Name)
	assert.Equal(t, settings, created.Settings)

	limit, err := client.GetProjectStorageQuota(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<30), limit)

	changed := harbor.ProjectSettings{
		Public:       ptr(false),
		Severity:     ptr("low"),
		CVEAllowlist: &harbor.CVEAllowlist{Items: []string{"CVE-2024-0003"}},
	}
	require.NoError(t, client.UpdateProjectMetadata(ctx, name, changed))
	require.NoError(t, client.SetProjectStorageQuota(ctx, created.ID, harbor.UnlimitedQuota))

	got, err := client.GetProjectByName(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, changed.Public, got.Settings.Public)
	assert.Equal(t, changed.Severity, got.Settings.Severity)
	assert.Equal(t, changed.CVEAllowlist, got.Settings.CVEAllowlist)
	assert.Equal(t, settings.PreventVulnerableImages, got.Settings.PreventVulnerableImages, "unset fields are left alone")
	assert.Equal(t, settings.ScanOnPush, got.Settings.ScanOnPush)

	limit, err = client.GetProjectStorageQuota(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, harbor.UnlimitedQuota, limit)

	require.NoError(t, client.DeleteProject(ctx, name))

	_, err = client.GetProjectByName(ctx, name)
	require.Error(t, err)
	assert.True(t, harbor.IsNotFound(err))
	reason, _ := harbor.MapError(err)
	assert.Equal(t, harbor.ReasonNotFound, reason)
}

func TestProjectRobotLifecycleAndSecretRefresh(t *testing.T) {
	client, instance := newClient(t)
	ctx := t.Context()
	projectName := instance.CreateProject(t)
	found, err := client.GetProjectByName(ctx, projectName)
	require.NoError(t, err)

	spec := harbor.RobotSpec{
		Name:        "ci",
		Description: "first",
		Duration:    -1,
		Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}},
	}
	created, err := client.CreateRobot(ctx, projectName, spec)
	require.NoError(t, err)
	assert.NotEmpty(t, created.Secret)
	assert.Contains(t, created.Name, "+ci")

	byName, err := client.FindProjectRobot(ctx, found.ID, "ci")
	require.NoError(t, err)
	assert.Equal(t, created.ID, byName.ID)

	_, err = client.FindProjectRobot(ctx, found.ID, "absent")
	assert.True(t, harbor.IsNotFound(err))

	spec.Description = "second"
	spec.Disabled = true
	spec.Permissions = []harbor.RobotAccess{{Resource: "repository", Action: "pull"}, {Resource: "repository", Action: "push"}}
	require.NoError(t, client.UpdateRobot(ctx, created.ID, projectName, spec))

	got, err := client.GetRobot(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "second", got.Description)
	assert.True(t, got.Disabled)
	assert.ElementsMatch(t, spec.Permissions, got.Permissions)
	assert.Equal(t, created.Name, got.Name)

	spec.Disabled = false
	require.NoError(t, client.UpdateRobot(ctx, created.ID, projectName, spec))

	asRobot := func(secret string) error {
		robotClient, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: created.Name, Password: secret})
		require.NoError(t, err)

		return robotClient.CheckCredentials(ctx)
	}
	require.NoError(t, asRobot(created.Secret))

	refreshed, err := client.RefreshRobotSecret(ctx, created.ID)
	require.NoError(t, err)
	assert.NotEqual(t, created.Secret, refreshed)
	require.NoError(t, asRobot(refreshed))
	require.Error(t, asRobot(created.Secret))

	require.NoError(t, client.DeleteRobot(ctx, created.ID))

	_, err = client.GetRobot(ctx, created.ID)
	assert.True(t, harbor.IsNotFound(err))
}

func TestSystemRobotSeesAMissingProjectAsNotFound(t *testing.T) {
	admin, instance := newClient(t)
	access := []*models.Access{{Resource: "project", Action: "read", Effect: "allow"}, {Resource: "project", Action: "delete", Effect: "allow"}}
	username, secret := instance.CreateSystemRobot(t, []*models.RobotPermission{{Kind: "project", Namespace: "*", Access: access}})
	robot, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: username, Password: secret})
	require.NoError(t, err)

	name := harborfixture.UniqueName(t, "op")
	_, err = robot.GetProjectByName(t.Context(), name)
	require.Error(t, err)
	assert.True(t, harbor.IsNotFound(err), "Harbor answers a robot's read of a missing project with 403, which is not a failure to report")

	_, err = admin.CreateProject(t.Context(), name, harbor.ProjectSettings{}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.DeleteProject(context.Background(), name) })

	found, err := robot.GetProjectByName(t.Context(), name)
	require.NoError(t, err)
	assert.Equal(t, name, found.Name)
}
