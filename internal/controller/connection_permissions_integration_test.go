//go:build integration

package controller_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// shortConnectionResync re-checks the connection every two seconds.
func shortConnectionResync(connection *harborv1alpha1.HarborConnection) {
	connection.Spec.Defaults.ResyncInterval = &metav1.Duration{Duration: 2 * time.Second}
}

func permissionsCondition(t *testing.T, cl client.Client, key client.ObjectKey) *metav1.Condition {
	t.Helper()

	var connection harborv1alpha1.HarborConnection
	if err := cl.Get(t.Context(), key, &connection); err != nil {
		return nil
	}

	return meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionPermissionsSufficient)
}

func awaitPermissions(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *metav1.Condition {
	t.Helper()

	var condition *metav1.Condition

	require.Eventually(t, func() bool {
		condition = permissionsCondition(t, cl, key)

		return condition != nil && condition.Status == status && condition.Reason == reason
	}, projectWait, 200*time.Millisecond, "waiting for PermissionsSufficient=%s %s, have %+v", status, reason, condition)

	return condition
}

func eventsWithReason(t *testing.T, cl client.Client, object client.Object, reason string) int {
	t.Helper()

	var list eventsv1.EventList
	require.NoError(t, cl.List(t.Context(), &list, client.InNamespace(object.GetNamespace())))

	count := 0

	for _, event := range list.Items {
		if event.Regarding.UID == object.GetUID() && event.Reason == reason {
			count++
		}
	}

	return count
}

// connectionRobotGrants are the grants of the connection's system robot, with extra access added in every project.
func connectionRobotGrants(extra ...harbor.RobotAccess) []harbor.RobotGrant {
	var grants []harbor.RobotGrant

	for _, permission := range systemRobotPermissions() {
		access := make([]harbor.RobotAccess, 0, len(permission.Access))
		for _, entry := range permission.Access {
			access = append(access, harbor.RobotAccess{Resource: entry.Resource, Action: entry.Action})
		}

		if permission.Kind == harbor.LevelProject {
			access = append(access, extra...)
		}

		grants = append(grants, harbor.RobotGrant{Kind: permission.Kind, Namespace: permission.Namespace, Access: access})
	}

	return grants
}

func TestConnectionReportsWhetherItsCredentialsHoldTheNeededPermissions(t *testing.T) {
	t.Parallel()

	t.Run("a missing permission is listed until it is granted", func(t *testing.T) {
		t.Parallel()

		setup := readyRobotProjectWithSystemRobot(t, true, shortConnectionResync, nil)
		key := client.ObjectKeyFromObject(setup.connection)
		awaitPermissions(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonPermissionsSufficient)

		// The connection's robot holds no immutable-tag permission.
		require.NoError(t, setup.cl.Create(t.Context(), immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("v*", false))))

		missing := awaitPermissions(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonInsufficientPermissions)
		for _, action := range []string{"create", "delete", "list", "update"} {
			assert.Contains(t, missing.Message, "project "+setup.project.Spec.Name+" immutable-tag:"+action)
		}

		assert.NotContains(t, missing.Message, "system ", "the robot holds every system permission a project needs")
		awaitEvent(t, setup.cl, setup.connection, harborv1alpha1.ReasonInsufficientPermissions)

		var connection harborv1alpha1.HarborConnection
		require.NoError(t, setup.cl.Get(t.Context(), key, &connection))
		status, reason := readyReason(&connection)
		assert.Equal(t, metav1.ConditionTrue, status, "missing permissions are not part of Ready")
		assert.Equal(t, harborv1alpha1.ReasonReady, reason)

		username := connectionUsername(t, setup)
		robot, err := setup.admin.FindSystemRobot(t.Context(), username)
		require.NoError(t, err)
		require.NoError(t, setup.admin.UpdateRobot(t.Context(), robot.ID, "", harbor.RobotSpec{
			Duration: -1,
			Grants: connectionRobotGrants(
				harbor.RobotAccess{Resource: "immutable-tag", Action: "create"}, harbor.RobotAccess{Resource: "immutable-tag", Action: "delete"},
				harbor.RobotAccess{Resource: "immutable-tag", Action: "list"}, harbor.RobotAccess{Resource: "immutable-tag", Action: "update"},
			),
		}))

		awaitPermissions(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonPermissionsSufficient)
		awaitEvent(t, setup.cl, setup.connection, harborv1alpha1.ReasonPermissionsSufficient)
	})

	allowContentDeletion := func(connection *harborv1alpha1.HarborConnection) {
		shortConnectionResync(connection)
		connection.Spec.AllowContentDeletion = true
	}

	t.Run("deleting a project's contents needs repository permissions", func(t *testing.T) {
		t.Parallel()

		withContents := func(project *harborv1alpha1.HarborProject) {
			project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDeleteWithContents
		}

		setup := readyRobotProjectWithSystemRobot(t, true, allowContentDeletion, withContents)
		missing := awaitPermissions(t, setup.cl, client.ObjectKeyFromObject(setup.connection), metav1.ConditionFalse, harborv1alpha1.ReasonInsufficientPermissions)
		assert.Equal(t, "the credentials lack project "+setup.project.Spec.Name+" repository:delete, project "+setup.project.Spec.Name+" repository:list", missing.Message)
	})

	t.Run("a project that keeps its contents needs no repository permissions", func(t *testing.T) {
		t.Parallel()

		plain := readyRobotProjectWithSystemRobot(t, true, allowContentDeletion, nil)
		plainKey := client.ObjectKeyFromObject(plain.connection)
		awaitPermissions(t, plain.cl, plainKey, metav1.ConditionTrue, harborv1alpha1.ReasonPermissionsSufficient)
		require.Never(t, func() bool {
			condition := permissionsCondition(t, plain.cl, plainKey)

			return condition == nil || condition.Status != metav1.ConditionTrue
		}, 6*time.Second, 200*time.Millisecond, "a project that keeps its contents needs no repository permissions")
	})

	t.Run("an administrator holds every permission", func(t *testing.T) {
		t.Parallel()

		setup := readyRobotProjectWithSystemRobot(t, false, shortConnectionResync, nil)
		require.NoError(t, setup.cl.Create(t.Context(), immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("v*", false))))

		robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
		robot.Spec.SystemPermissions = []harborv1alpha1.SystemRobotAccess{{Resource: "registry", Action: "list"}}
		require.NoError(t, setup.cl.Create(t.Context(), robot))
		awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		awaitPermissions(t, setup.cl, client.ObjectKeyFromObject(setup.connection), metav1.ConditionTrue, harborv1alpha1.ReasonPermissionsSufficient)
	})

	t.Run("resources in namespaces that are not admitted change nothing", func(t *testing.T) {
		t.Parallel()

		setup := readyRobotProjectWithSystemRobot(t, true, shortConnectionResync, nil)
		key := client.ObjectKeyFromObject(setup.connection)
		awaitPermissions(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonPermissionsSufficient)

		other := newNamespace(t, setup.cl, "other")

		// The project admits only its own namespace, and the connection admits system-wide kinds only in its own.
		require.NoError(t, setup.cl.Create(t.Context(), immutabilityPolicy(other, setup.project, immutabilityRule("v*", false))))

		robot := newSystemRobot(t, setup, other)
		robot.Spec.SystemPermissions = []harborv1alpha1.SystemRobotAccess{{Resource: "registry", Action: "list"}}
		require.NoError(t, setup.cl.Create(t.Context(), robot))
		awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

		// Several resyncs of the connection pass without the condition changing.
		require.Never(t, func() bool {
			condition := permissionsCondition(t, setup.cl, key)

			return condition == nil || condition.Status != metav1.ConditionTrue
		}, 8*time.Second, 200*time.Millisecond, "a resource that is not admitted changed PermissionsSufficient")
		assert.Zero(t, eventsWithReason(t, setup.cl, setup.connection, harborv1alpha1.ReasonInsufficientPermissions))
	})
}

// connectionUsername returns the username the connection signs in with.
func connectionUsername(t *testing.T, setup *robotSetup) string {
	t.Helper()

	var connection harborv1alpha1.HarborConnection
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKeyFromObject(setup.connection), &connection))

	secret := credentials("", "", "", "")
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKey{Namespace: connection.Namespace, Name: connection.Spec.CredentialsSecretRef.Name}, secret))

	username := string(secret.Data["username"])
	require.True(t, strings.HasPrefix(username, "robot$"), "the connection signs in as a robot, have %q", username)

	return username
}
