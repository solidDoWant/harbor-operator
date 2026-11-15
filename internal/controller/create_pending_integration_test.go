//go:build integration

package controller_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// forgedCreatePending is the create-pending annotation as a namespace user would set it on a resource they write.
func forgedCreatePending() map[string]string {
	return map[string]string{harborv1alpha1.AnnotationCreatePending: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
}

// setProjectCreatePending writes status.createPending as the operator would before a create call, then changes the
// spec so that the next Ready condition at the new generation comes from a reconcile that saw the marker.
func setProjectCreatePending(t *testing.T, cl client.Client, key client.ObjectKey, pending time.Time) {
	t.Helper()

	require.Eventually(t, func() bool {
		project := &harborv1alpha1.HarborProject{}
		if err := cl.Get(t.Context(), key, project); err != nil {
			return false
		}

		project.Status.CreatePending = &metav1.Time{Time: pending}

		return cl.Status().Update(t.Context(), project) == nil
	}, 10*time.Second, 100*time.Millisecond)

	updateProject(t, cl, key, func(project *harborv1alpha1.HarborProject) {
		project.Spec.ResyncInterval = &metav1.Duration{Duration: project.Spec.ResyncInterval.Duration + time.Second}
	})
}

func setRobotCreatePending(t *testing.T, cl client.Client, key client.ObjectKey, pending time.Time) {
	t.Helper()

	require.Eventually(t, func() bool {
		robot := &harborv1alpha1.HarborProjectRobot{}
		if err := cl.Get(t.Context(), key, robot); err != nil {
			return false
		}

		robot.Status.CreatePending = &metav1.Time{Time: pending}

		return cl.Status().Update(t.Context(), robot) == nil
	}, 10*time.Second, 100*time.Millisecond)

	updateRobot(t, cl, key, func(robot *harborv1alpha1.HarborProjectRobot) {
		robot.Spec.ResyncInterval = &metav1.Duration{Duration: robot.Spec.ResyncInterval.Duration + time.Second}
	})
}

func TestProjectWithAUserSetCreatePendingAnnotationCannotTakeOverAnExistingProject(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)

	existing, err := admin.CreateProject(t.Context(), name, harbor.ProjectSettings{Public: ptr(false)}, nil)
	require.NoError(t, err)

	t.Run("without adopt reports AlreadyExists, changes nothing and leaves the project on deletion", func(t *testing.T) {
		connection := readyConnection(t, cl, instance, "default", nil)

		project := newProject("default", name, connection)
		project.Annotations = forgedCreatePending()
		project.Spec.Public = ptr(true)
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)

		reported := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)
		assert.Zero(t, reported.Status.ProjectID)
		assert.NotContains(t, reported.Annotations, harborv1alpha1.AnnotationCreatePending, "the user-set annotation is removed")

		found, _ := harborSettings(t, admin, name)
		assert.Equal(t, ptr(false), found.Settings.Public, "the project's settings are not overwritten")

		require.NoError(t, cl.Delete(t.Context(), project))
		awaitGone(t, cl, key, "a resource without an ID is released at once")

		found, err := admin.GetProjectByName(t.Context(), name)
		require.NoError(t, err, "the project the resource did not create is not deleted")
		assert.Equal(t, existing.ID, found.ID)
	})

	t.Run("adopt without allowAdoption reports AdoptionNotAllowed", func(t *testing.T) {
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "forged-no-adoption"}}))
		connection := readyConnection(t, cl, instance, "forged-no-adoption", nil)

		project := newProject("forged-no-adoption", name, connection)
		project.Annotations = forgedCreatePending()
		project.Spec.Adopt = true
		project.Spec.Public = ptr(true)
		require.NoError(t, cl.Create(t.Context(), project))

		reported := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonAdoptionNotAllowed)
		assert.Zero(t, reported.Status.ProjectID)

		found, _ := harborSettings(t, admin, name)
		assert.Equal(t, ptr(false), found.Settings.Public)

		require.NoError(t, cl.Delete(t.Context(), project))
	})
}

func TestProjectCreateMarkerInStatusClaimsOnlyAProjectCreatedAfterIt(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	connection := readyConnection(t, cl, instance, "default", nil)

	beforeCreate := time.Now().UTC().Truncate(time.Second)
	existing, err := admin.CreateProject(t.Context(), name, harbor.ProjectSettings{Public: ptr(false)}, nil)
	require.NoError(t, err)

	project := newProject("default", name, connection)
	project.Spec.Public = ptr(true)
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)
	awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)

	// A marker set well after Harbor created the project, beyond the clock-skew allowance, does not prove the create.
	setProjectCreatePending(t, cl, key, beforeCreate.Add(10*time.Minute))
	reported := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)
	assert.Zero(t, reported.Status.ProjectID)

	found, _ := harborSettings(t, admin, name)
	assert.Equal(t, ptr(false), found.Settings.Public)

	// A marker set before the create call, as left by an operator restarted mid-create, finishes the create.
	setProjectCreatePending(t, cl, key, beforeCreate)
	ready := awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, int32(existing.ID), ready.Status.ProjectID) //nolint:gosec // test IDs are small
	assert.Nil(t, ready.Status.CreatePending, "the marker is cleared once the ID is recorded")

	found, _ = harborSettings(t, admin, name)
	assert.Equal(t, ptr(true), found.Settings.Public, "the resource's own project gets the spec's settings")
}

func TestRobotWithAUserSetCreatePendingAnnotationCannotTakeOverAnExistingRobot(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	// The robot can push, so a push with its secret shows that the secret was not refreshed.
	existing, err := setup.admin.CreateRobot(t.Context(), setup.project.Spec.Name, harbor.RobotSpec{
		Name: "ci", Description: "made by hand", Duration: -1,
		Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}},
	})
	require.NoError(t, err)

	for _, test := range []struct {
		name   string
		adopt  bool
		reason string
	}{
		{name: "without adopt reports AlreadyExists", reason: harborv1alpha1.ReasonAlreadyExists},
		{name: "adopt without allowAdoption reports AdoptionNotAllowed", adopt: true, reason: harborv1alpha1.ReasonAdoptionNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			robot := ciRobot("default", setup.project)
			robot.Annotations = forgedCreatePending()
			robot.Spec.Adopt = test.adopt
			robot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
			require.NoError(t, setup.cl.Create(t.Context(), robot))
			key := client.ObjectKeyFromObject(robot)

			reported := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, test.reason)
			assert.Zero(t, reported.Status.RobotID)
			assert.NotContains(t, reported.Annotations, harborv1alpha1.AnnotationCreatePending, "the user-set annotation is removed")

			secret := &corev1.Secret{}
			require.True(t, apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "ci-dockerconfig"}, secret)),
				"no Secret is written for a robot the resource does not own")

			found, err := setup.admin.GetRobot(t.Context(), existing.ID)
			require.NoError(t, err)
			assert.Equal(t, "made by hand", found.Description, "the robot is not changed")
			require.NoError(t, push(t, setup.instance, existing.Name, existing.Secret, setup.project.Spec.Name, "app"), "the robot's secret is not refreshed")

			require.NoError(t, setup.cl.Delete(t.Context(), robot))
			awaitGone(t, setup.cl, key, "a resource without an ID is released at once")

			_, err = setup.admin.GetRobot(t.Context(), existing.ID)
			require.NoError(t, err, "the robot the resource did not create is not deleted")
		})
	}
}

func TestRobotCreateMarkerInStatusClaimsOnlyARobotCreatedAfterIt(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	beforeCreate := time.Now().UTC().Truncate(time.Second)
	existing, err := setup.admin.CreateRobot(t.Context(), setup.project.Spec.Name, harbor.RobotSpec{
		Name: "ci", Description: "made by hand", Duration: -1, Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}},
	})
	require.NoError(t, err)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)
	awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)

	// A marker set well after Harbor created the robot, beyond the clock-skew allowance, does not prove the create.
	setRobotCreatePending(t, setup.cl, key, beforeCreate.Add(10*time.Minute))
	reported := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)
	assert.Zero(t, reported.Status.RobotID)

	// A marker set before the create call, as left by an operator restarted mid-create, finishes the create: the
	// secret was lost with the restart, so it is refreshed.
	setRobotCreatePending(t, setup.cl, key, beforeCreate)
	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, existing.ID, ready.Status.RobotID)
	assert.Nil(t, ready.Status.CreatePending, "the marker is cleared once the Secrets are written")

	username, password := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
	assert.Equal(t, existing.Name, username)
	assert.NotEqual(t, existing.Secret, password, "the lost secret is refreshed")
	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the refreshed secret works")
}
