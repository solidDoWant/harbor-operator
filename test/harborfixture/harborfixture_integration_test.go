//go:build integration

package harborfixture_test

import (
	"context"
	"testing"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/project"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/repository"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/robot"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
	"github.com/stretchr/testify/require"

	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

func TestProjectCreatedPushedAndListed(t *testing.T) {
	harborInstance := harborfixture.FromEnv(t)
	projectName := harborInstance.CreateProject(t)
	harborInstance.PushImage(t, projectName, "app", "v1")

	repositories, err := harborInstance.Client(t).Repository.ListRepositories(t.Context(), &repository.ListRepositoriesParams{ProjectName: projectName})
	require.NoError(t, err)
	require.Len(t, repositories.Payload, 1)
	require.Equal(t, projectName+"/app", repositories.Payload[0].Name)
}

func TestProjectDeletedAtCleanup(t *testing.T) {
	harborInstance := harborfixture.FromEnv(t)

	var projectName string

	t.Run("create", func(t *testing.T) {
		projectName = harborInstance.CreateProject(t)
		harborInstance.PushImage(t, projectName, "app", "v1")
	})
	_, err := harborInstance.Client(t).Project.GetProject(t.Context(), &project.GetProjectParams{ProjectNameOrID: projectName})
	require.Error(t, err)
}

// A system robot with robot-create permission can create project and system
// robots, but only with permissions it holds itself. The result is recorded in
// docs/admin.
func TestSystemRobotCreatesRobotsWithinItsOwnPermissions(t *testing.T) {
	harborInstance := harborfixture.FromEnv(t)
	projectName := harborInstance.CreateProject(t)
	pull := &models.Access{Resource: "repository", Action: "pull"}
	robotCreate := &models.Access{Resource: "robot", Action: "create"}
	childPermissions := []*models.RobotPermission{{Kind: "project", Namespace: projectName, Access: []*models.Access{pull}}}

	tests := []struct {
		name          string
		creatorAccess []*models.Access
		assertError   require.ErrorAssertionFunc
	}{
		{name: "creator holds the permissions it grants", creatorAccess: []*models.Access{robotCreate, pull}, assertError: require.NoError},
		{name: "creator lacks a permission it grants", creatorAccess: []*models.Access{robotCreate}, assertError: require.Error},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			username, secret := harborInstance.CreateSystemRobot(t, []*models.RobotPermission{
				{Kind: "system", Namespace: "/", Access: []*models.Access{robotCreate}},
				{Kind: "project", Namespace: "*", Access: test.creatorAccess},
			})
			client := harborInstance.ClientFor(t, username, secret)
			admin := harborInstance.Client(t)

			for _, level := range []string{"project", "system"} {
				created, err := client.Robot.CreateRobot(t.Context(), &robot.CreateRobotParams{Robot: &models.RobotCreate{
					Name: harborfixture.UniqueName(t, level[:2]), Level: level, Duration: -1, Permissions: childPermissions,
				}})
				test.assertError(t, err, level+" robot")

				if err == nil {
					t.Cleanup(func() {
						_, _ = admin.Robot.DeleteRobot(context.Background(), &robot.DeleteRobotParams{RobotID: created.Payload.ID})
					})
				}
			}
		})
	}
}
