//go:build integration

package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/configure"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
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
	t.Parallel()

	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "pending")

	existing, err := admin.CreateProject(t.Context(), name, harbor.ProjectSettings{Public: ptr(false)}, nil)
	require.NoError(t, err)

	t.Run("without adopt reports AlreadyExists, changes nothing and leaves the project on deletion", func(t *testing.T) {
		connection := readyConnection(t, cl, instance, ns, nil)

		project := newProject(ns, name, connection)
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
		noAdoption := newNamespace(t, cl, "forged-no-adoption")
		connection := readyConnection(t, cl, instance, noAdoption, nil)

		project := newProject(noAdoption, name, connection)
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
	t.Parallel()

	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "pending")
	connection := readyConnection(t, cl, instance, ns, nil)

	beforeCreate := time.Now().UTC().Truncate(time.Second)
	existing, err := admin.CreateProject(t.Context(), name, harbor.ProjectSettings{Public: ptr(false)}, nil)
	require.NoError(t, err)

	project := newProject(ns, name, connection)
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
	t.Parallel()

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
			robot := ciRobot(setup.project.Namespace, setup.project)
			robot.Annotations = forgedCreatePending()
			robot.Spec.Adopt = test.adopt
			robot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
			require.NoError(t, setup.cl.Create(t.Context(), robot))
			key := client.ObjectKeyFromObject(robot)

			reported := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, test.reason)
			assert.Zero(t, reported.Status.RobotID)
			assert.NotContains(t, reported.Annotations, harborv1alpha1.AnnotationCreatePending, "the user-set annotation is removed")

			secret := &corev1.Secret{}
			require.True(t, apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKey{Namespace: setup.project.Namespace, Name: "ci-dockerconfig"}, secret)),
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
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)

	beforeCreate := time.Now().UTC().Truncate(time.Second)
	existing, err := setup.admin.CreateRobot(t.Context(), setup.project.Spec.Name, harbor.RobotSpec{
		Name: "ci", Description: "made by hand", Duration: -1, Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}},
	})
	require.NoError(t, err)

	robot := ciRobot(setup.project.Namespace, setup.project)
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

	username, password := dockerCredentials(t, setup.cl, setup.project.Namespace, "ci-dockerconfig", setup.instance.Registry(t))
	assert.Equal(t, existing.Name, username)
	assert.NotEqual(t, existing.Secret, password, "the lost secret is refreshed")
	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the refreshed secret works")
}

// restrictProjectCreation sets who may create projects in Harbor: "adminonly" or "everyone".
func restrictProjectCreation(t *testing.T, instance *harborfixture.Harbor, value string) {
	t.Helper()

	_, err := instance.Client(t).Configure.UpdateConfigurations(context.Background(), &configure.UpdateConfigurationsParams{
		Configurations: &models.Configurations{ProjectCreationRestriction: &value},
	})
	require.NoError(t, err)
}

func TestProjectWhoseCreateHarborRefusedDoesNotTakeOverAProjectMadeLater(t *testing.T) {
	// Serial: it restricts project creation Harbor-wide, which would fail other tests that create projects as a non-admin.
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "pending")

	access := func(resource string, actions ...string) []*models.Access {
		result := make([]*models.Access, 0, len(actions))
		for _, action := range actions {
			result = append(result, &models.Access{Resource: resource, Action: action, Effect: "allow"})
		}

		return result
	}

	connectionInstance := *instance
	connectionInstance.Username, connectionInstance.Password = instance.CreateSystemRobot(t, []*models.RobotPermission{
		{Kind: "system", Namespace: "/", Access: append(access("project", "list"), access("quota", "read", "update")...)},
		{Kind: "project", Namespace: "*", Access: access("project", "read", "update", "delete")},
	})
	connection := readyConnection(t, cl, &connectionInstance, ns, nil)

	// Only admins may create projects, so Harbor refuses the connection's create. The restriction stays until the
	// admin has created the project, so the operator's retries cannot create it first.
	restrictProjectCreation(t, instance, "adminonly")
	t.Cleanup(func() { restrictProjectCreation(t, instance, "everyone") })

	project := newProject(ns, name, connection)
	project.Spec.Public = ptr(true)
	project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)

	// Each retry sets the marker again just before its create call, so wait for a moment between retries.
	awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonForbidden)
	require.Eventually(t, func() bool {
		refused := &harborv1alpha1.HarborProject{}
		return cl.Get(t.Context(), key, refused) == nil && refused.Status.CreatePending == nil
	}, projectWait, 50*time.Millisecond, "a create Harbor refused leaves no marker")

	existing, err := admin.CreateProject(t.Context(), name, harbor.ProjectSettings{Public: ptr(false)}, nil)
	require.NoError(t, err)
	restrictProjectCreation(t, instance, "everyone")

	reported := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)
	assert.Zero(t, reported.Status.ProjectID)

	found, _ := harborSettings(t, admin, name)
	assert.Equal(t, ptr(false), found.Settings.Public, "the project's settings are not overwritten")

	require.NoError(t, cl.Delete(t.Context(), project))
	awaitGone(t, cl, key, "a resource without an ID is released at once")

	found, err = admin.GetProjectByName(t.Context(), name)
	require.NoError(t, err, "the project the resource did not create is not deleted")
	assert.Equal(t, existing.ID, found.ID)
}

func TestRobotWhoseCreateHarborRefusedDoesNotTakeOverARobotMadeLater(t *testing.T) {
	t.Parallel()

	setup := readyRobotProjectWithSystemRobot(t, true, nil, nil)

	// The connection's system robot does not hold artifact:delete, so Harbor refuses to create a robot with it.
	robot := ciRobot(setup.project.Namespace, setup.project)
	robot.Spec.Permissions = []harborv1alpha1.RobotAccess{{Resource: "artifact", Action: "delete"}}
	robot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	// Each retry sets the marker again just before its create call, so wait for a moment between retries.
	awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonForbidden)
	require.Eventually(t, func() bool {
		refused := &harborv1alpha1.HarborProjectRobot{}
		return setup.cl.Get(t.Context(), key, refused) == nil && refused.Status.CreatePending == nil
	}, projectWait, 50*time.Millisecond, "a create Harbor refused leaves no marker")

	// The robot can push, so a push with its secret shows that the secret was not refreshed.
	existing, err := setup.admin.CreateRobot(t.Context(), setup.project.Spec.Name, harbor.RobotSpec{
		Name: "ci", Description: "made by hand", Duration: -1,
		Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}},
	})
	require.NoError(t, err)

	reported := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)
	assert.Zero(t, reported.Status.RobotID)

	found, err := setup.admin.GetRobot(t.Context(), existing.ID)
	require.NoError(t, err, "the hand-made robot is not deleted")
	assert.Equal(t, "made by hand", found.Description, "the robot is not changed")
	require.NoError(t, push(t, setup.instance, existing.Name, existing.Secret, setup.project.Spec.Name, "app"), "the robot's secret still works")

	require.NoError(t, setup.cl.Delete(t.Context(), robot))
	awaitGone(t, setup.cl, key, "a resource without an ID is released at once")

	_, err = setup.admin.GetRobot(t.Context(), existing.ID)
	require.NoError(t, err, "the robot the resource did not create is not deleted")
}
