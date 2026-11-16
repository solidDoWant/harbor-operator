//go:build integration

package controller_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// systemRobotWithinTheConnectionsRobot is a system robot asking only for permissions the connection's system robot
// holds: "project" from Harbor's system permission list, "quota" and "robot" from the connection robot's own record,
// and push and pull in one named project.
func systemRobotWithinTheConnectionsRobot(t *testing.T, setup *robotSetup) *harborv1alpha1.HarborSystemRobot {
	t.Helper()

	robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
	robot.Spec.SystemPermissions = []harborv1alpha1.SystemRobotAccess{
		{Resource: "project", Action: "list"}, {Resource: "quota", Action: "list"}, {Resource: "robot", Action: "list"},
	}
	robot.Spec.ProjectPermissions = []harborv1alpha1.ProjectPermission{{Projects: []string{setup.project.Spec.Name}, Access: []harborv1alpha1.SystemRobotProjectAccess{
		{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"},
	}}}

	return robot
}

func TestSystemRobotThroughARobotConnectionThatCannotUpdateRobots(t *testing.T) {
	t.Parallel()

	t.Run("drift in Harbor is put back by replacing the robot", func(t *testing.T) {
		t.Parallel()

		setup := readyRobotProjectWithSystemRobot(t, true, nil, nil)
		robot := systemRobotWithinTheConnectionsRobot(t, setup)
		require.NoError(t, setup.cl.Create(t.Context(), robot))
		key := client.ObjectKeyFromObject(robot)
		ready := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		oldID := ready.Status.RobotID
		require.NoError(t, setup.admin.UpdateRobot(t.Context(), oldID, "", harbor.RobotSpec{
			Description: "changed in the UI", Duration: -1,
			Grants: []harbor.RobotGrant{{Kind: harbor.LevelProject, Namespace: setup.project.Spec.Name, Access: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}}}},
		}))

		replaced := &harborv1alpha1.HarborSystemRobot{}

		require.Eventually(t, func() bool {
			return setup.cl.Get(t.Context(), key, replaced) == nil && replaced.Status.RobotID != 0 && replaced.Status.RobotID != oldID
		}, projectWait, 500*time.Millisecond, "waiting for status.robotID to change, have conditions %+v", replaced.Status.Conditions)
		awaitEvent(t, setup.cl, replaced, harborv1alpha1.EventReasonReplaced)
		awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		found, err := setup.admin.GetRobot(t.Context(), replaced.Status.RobotID)
		require.NoError(t, err)
		assert.Equal(t, "deploys everywhere", found.Description)
		assert.Equal(t, harbor.FlattenGrants([]harbor.RobotGrant{
			{Kind: harbor.LevelSystem, Namespace: "/", Access: []harbor.RobotAccess{
				{Resource: "project", Action: "list"}, {Resource: "quota", Action: "list"}, {Resource: "robot", Action: "list"},
			}},
			{Kind: harbor.LevelProject, Namespace: setup.project.Spec.Name, Access: []harbor.RobotAccess{
				{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"},
			}},
		}), harbor.FlattenGrants(found.Grants))
	})

	t.Run("a permission the connection lacks is named and the robot is kept", func(t *testing.T) {
		t.Parallel()

		setup := readyRobotProjectWithSystemRobot(t, true, nil, nil)
		robot := systemRobotWithinTheConnectionsRobot(t, setup)
		require.NoError(t, setup.cl.Create(t.Context(), robot))
		key := client.ObjectKeyFromObject(robot)
		ready := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.Eventually(t, func() bool {
			current := &harborv1alpha1.HarborSystemRobot{}
			if err := setup.cl.Get(t.Context(), key, current); err != nil {
				return false
			}

			current.Spec.SystemPermissions = append(current.Spec.SystemPermissions, harborv1alpha1.SystemRobotAccess{Resource: "registry", Action: "list"})

			return setup.cl.Update(t.Context(), current) == nil
		}, 10*time.Second, 100*time.Millisecond)

		refused := awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonForbidden)
		condition := meta.FindStatusCondition(refused.Status.Conditions, harborv1alpha1.ConditionReady)
		require.NotNil(t, condition)
		assert.True(t, strings.HasSuffix(condition.Message, "lack these permissions: system: registry list"),
			"only the missing permission is named, have %q", condition.Message)
		assert.Equal(t, ready.Status.RobotID, refused.Status.RobotID, "the robot is not replaced")

		_, err := setup.admin.GetRobot(t.Context(), ready.Status.RobotID)
		require.NoError(t, err, "the Harbor robot is not deleted")
	})
}
