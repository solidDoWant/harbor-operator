//go:build integration

package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// useSecret creates a connection in namespace that signs in with the Secret secretName and deletes it when the test
// ends. The connection need not become ready: naming the Secret is what makes it a user.
func useSecret(t *testing.T, setup *robotSetup, namespace, name, secretName string) *harborv1alpha1.HarborConnection {
	t.Helper()

	connection := newConnection(namespace, name, setup.connection.Spec.URL, secretName)
	require.NoError(t, setup.cl.Create(t.Context(), connection))
	t.Cleanup(func() { _ = setup.cl.Delete(context.Background(), connection) })

	return connection
}

func readyMessageOf(conditions []metav1.Condition) string {
	if condition := meta.FindStatusCondition(conditions, harborv1alpha1.ConditionReady); condition != nil {
		return condition.Message
	}

	return ""
}

func secretExists(t *testing.T, cl client.Client, namespace, name string) bool {
	t.Helper()

	err := cl.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &corev1.Secret{})
	if apierrors.IsNotFound(err) {
		return false
	}

	require.NoError(t, err)

	return true
}

func TestSystemRobotDeletionWaitsForConnectionsUsingItsSecret(t *testing.T) {
	t.Parallel()

	for _, policy := range []harborv1alpha1.DeletionPolicy{harborv1alpha1.DeletionPolicyDelete, harborv1alpha1.DeletionPolicyOrphan} {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()

			setup := readyRobotProject(t, nil, nil)
			namespace := setup.connection.Namespace

			robot := newSystemRobot(t, setup, namespace)
			robot.Name = "root"
			robot.Spec.DeletionPolicy = policy
			robot.Spec.Secret.Opaque.Enabled = ptr(true)
			require.NoError(t, setup.cl.Create(t.Context(), robot))

			key := client.ObjectKeyFromObject(robot)
			ready := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
			robotID := ready.Status.RobotID

			connection := useSecret(t, setup, namespace, "main", "root-credentials")

			require.NoError(t, setup.cl.Delete(t.Context(), robot))

			waiting := awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
			assert.Contains(t, readyMessageOf(waiting.Status.Conditions), "main", "the message names the connection")

			_, err := setup.admin.GetRobot(t.Context(), robotID)
			require.NoError(t, err, "the Harbor robot stays while a connection uses its Secret")
			assert.True(t, secretExists(t, setup.cl, namespace, "root-credentials"), "the Secret the connection uses stays")

			// The robot's resync is short, but a waiting deletion is not re-queued by it: the connection watch ends the
			// wait.
			require.NoError(t, setup.cl.Delete(t.Context(), connection))
			awaitObjectGone(t, setup.cl, &harborv1alpha1.HarborSystemRobot{ObjectMeta: robot.ObjectMeta}, "the robot deletion finishes once the connection is gone")

			_, err = setup.admin.GetRobot(t.Context(), robotID)
			if policy == harborv1alpha1.DeletionPolicyDelete {
				assert.True(t, harbor.IsNotFound(err), "the Harbor robot is deleted, have %v", err)
			} else {
				assert.NoError(t, err, "an orphaned Harbor robot stays")
			}
		})
	}
}

func TestProjectRobotDeletionWaitsForConnectionsUsingItsSecret(t *testing.T) {
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)
	namespace := setup.project.Namespace

	robot := ciRobot(namespace, setup.project)
	robot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	key := client.ObjectKeyFromObject(robot)
	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	robotID := ready.Status.RobotID

	connection := useSecret(t, setup, namespace, "pulls-as-ci", "ci-dockerconfig")

	require.NoError(t, setup.cl.Delete(t.Context(), robot))

	waiting := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	assert.Contains(t, readyMessageOf(waiting.Status.Conditions), "pulls-as-ci", "the message names the connection")

	_, err := setup.admin.GetRobot(t.Context(), robotID)
	require.NoError(t, err, "the Harbor robot stays while a connection uses its Secret")
	assert.True(t, secretExists(t, setup.cl, namespace, "ci-dockerconfig"), "the Secret the connection uses stays")

	require.NoError(t, setup.cl.Delete(t.Context(), connection))
	awaitObjectGone(t, setup.cl, &harborv1alpha1.HarborProjectRobot{ObjectMeta: robot.ObjectMeta}, "the robot deletion finishes once the connection is gone")

	_, err = setup.admin.GetRobot(t.Context(), robotID)
	assert.True(t, harbor.IsNotFound(err), "the Harbor robot is deleted, have %v", err)
}

func TestRobotKeepsADroppedSecretWhileAConnectionUsesIt(t *testing.T) {
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)
	namespace := setup.project.Namespace

	tests := []struct {
		name string
		// drop stops the spec naming the robot's opaque Secret.
		drop func(*harborv1alpha1.RobotSecretSpec)
		// wanted is the Secret the spec names afterwards, if any.
		wanted string
	}{
		{
			name:   "renamed",
			drop:   func(spec *harborv1alpha1.RobotSecretSpec) { spec.Opaque.Name = "renamed-credentials-v2" },
			wanted: "renamed-credentials-v2",
		},
		{
			name: "disabled",
			drop: func(spec *harborv1alpha1.RobotSecretSpec) { spec.Opaque.Enabled = ptr(false) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			robot := ciRobot(namespace, setup.project)
			robot.Name = test.name
			robot.Spec.Secret.Opaque.Enabled = ptr(true)
			require.NoError(t, setup.cl.Create(t.Context(), robot))

			key := client.ObjectKeyFromObject(robot)
			awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			oldSecret := test.name + "-credentials"
			connection := useSecret(t, setup, namespace, "uses-"+test.name, oldSecret)

			require.NoError(t, setup.cl.Get(t.Context(), key, robot))
			test.drop(&robot.Spec.Secret)
			require.NoError(t, setup.cl.Update(t.Context(), robot))

			waiting := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
			assert.Contains(t, readyMessageOf(waiting.Status.Conditions), "uses-"+test.name, "the message names the connection")
			assert.True(t, secretExists(t, setup.cl, namespace, oldSecret), "the Secret the connection uses is not deleted")

			if test.wanted != "" {
				assert.True(t, secretExists(t, setup.cl, namespace, test.wanted), "the newly named Secret is written")
			}

			require.NoError(t, setup.cl.Delete(t.Context(), connection))
			awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
			assert.Eventually(t, func() bool {
				return apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: oldSecret}, &corev1.Secret{}))
			}, projectWait, 200*time.Millisecond, "the dropped Secret is deleted once no connection uses it")
		})
	}
}
