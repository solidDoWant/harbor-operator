//go:build integration

package controller_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// deleteConnectionFirst deletes the connection and waits until its finalizer holds it for its dependents.
func deleteConnectionFirst(t *testing.T, cl client.Client, connection *harborv1alpha1.HarborConnection) {
	t.Helper()

	require.NoError(t, cl.Delete(t.Context(), connection))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
}

func awaitObjectGone(t *testing.T, cl client.Client, object client.Object, message string) {
	t.Helper()

	key := client.ObjectKeyFromObject(object)

	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(cl.Get(t.Context(), key, object))
	}, projectWait, 200*time.Millisecond, message)
}

func TestRegistryDeletedAfterItsConnectionIsDeletedFromHarbor(t *testing.T) {
	setup := readyRegistrySetup(t, nil, nil)
	harborName := setup.harborRegistryName(t, "")

	registry := setup.newRegistry("default", "upstream", harborName)
	registry.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	deleteConnectionFirst(t, setup.cl, setup.connection)

	require.NoError(t, setup.cl.Delete(t.Context(), ready))
	awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(registry))
	registryAbsent(t, setup.admin, harborName)

	awaitObjectGone(t, setup.cl, &harborv1alpha1.HarborConnection{ObjectMeta: setup.connection.ObjectMeta}, "the connection goes after its registry")
}

func TestProjectDependentsDeletedAfterTheirConnectionIsDeletedFollowTheirDeletionPolicy(t *testing.T) {
	setup := readyRobotProject(t, nil, func(project *harborv1alpha1.HarborProject) {
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	})
	projectID := setup.project.Status.ProjectID

	retention := keepVersionsAndRecentPulls("default", "retention", setup.project)
	retention.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, setup.cl.Create(t.Context(), retention))

	immutability := immutabilityPolicy("default", setup.project, immutabilityRule("v*", false))
	immutability.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, setup.cl.Create(t.Context(), immutability))

	projectRobot := newRobot("default", "puller", client.ObjectKeyFromObject(setup.project))
	projectRobot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, setup.cl.Create(t.Context(), projectRobot))

	systemRobot := newSystemRobot(t, setup, "default", setup.project.Spec.Name)
	require.NoError(t, setup.cl.Create(t.Context(), systemRobot))

	awaitRetention(t, setup.cl, client.ObjectKeyFromObject(retention), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	awaitImmutabilityPolicy(t, setup.cl, client.ObjectKeyFromObject(immutability), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	readyProjectRobot := awaitRobot(t, setup.cl, client.ObjectKeyFromObject(projectRobot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	readySystemRobot := awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(systemRobot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	deleteConnectionFirst(t, setup.cl, setup.connection)

	require.NoError(t, setup.cl.Delete(t.Context(), retention))
	require.NoError(t, setup.cl.Delete(t.Context(), immutability))
	awaitObjectGone(t, setup.cl, retention, "the retention policy goes")
	awaitObjectGone(t, setup.cl, immutability, "the immutability policy goes")

	have := projectRetention(t, setup.admin, projectID)
	require.NotNil(t, have)
	assert.Empty(t, have.Rules, "the retention rules are cleared")
	assert.Empty(t, immutabilityRulesWithoutIDs(t, setup.admin, projectID), "the immutability rules are deleted")

	t.Run("an unreachable Harbor keeps the dependent waiting", func(t *testing.T) {
		original := setup.connection.Spec.URL
		setConnectionURL(t, setup.cl, setup.connection, "https://127.0.0.1:1")

		require.NoError(t, setup.cl.Delete(t.Context(), readySystemRobot))
		held := awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(systemRobot), metav1.ConditionFalse, harborv1alpha1.ReasonHarborUnavailable)
		assert.Contains(t, held.Finalizers, controller.RobotFinalizer, "the robot resource keeps its finalizer")

		_, err := setup.admin.GetRobot(t.Context(), readySystemRobot.Status.RobotID)
		require.NoError(t, err, "the Harbor robot is not deleted while Harbor is unreachable")

		setConnectionURL(t, setup.cl, setup.connection, original)
	})

	awaitObjectGone(t, setup.cl, systemRobot, "the system robot goes once Harbor is reachable again")

	_, err := setup.admin.GetRobot(t.Context(), readySystemRobot.Status.RobotID)
	assert.True(t, harbor.IsNotFound(err), "the Harbor system robot is deleted: %v", err)

	require.NoError(t, setup.cl.Delete(t.Context(), readyProjectRobot))
	require.NoError(t, setup.cl.Delete(t.Context(), setup.project))
	awaitObjectGone(t, setup.cl, projectRobot, "the project robot goes")
	awaitObjectGone(t, setup.cl, setup.project, "the project goes")

	_, err = setup.admin.GetProject(t.Context(), int64(projectID))
	assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted: %v", err)

	awaitObjectGone(t, setup.cl, &harborv1alpha1.HarborConnection{ObjectMeta: setup.connection.ObjectMeta}, "the connection goes after its dependents")
}

// setConnectionURL points the connection at url; a terminating connection's spec can still change.
func setConnectionURL(t *testing.T, cl client.Client, connection *harborv1alpha1.HarborConnection, url string) {
	t.Helper()

	require.Eventually(t, func() bool {
		var current harborv1alpha1.HarborConnection
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current); err != nil {
			return false
		}

		current.Spec.URL = url

		return cl.Update(t.Context(), &current) == nil
	}, projectWait, 200*time.Millisecond, "updating the connection's URL")
}
