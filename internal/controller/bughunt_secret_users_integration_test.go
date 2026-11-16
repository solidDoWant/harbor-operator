//go:build integration

package controller_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// A robot whose spec names a Secret it does not own (SecretConflict) never wrote or owns that Secret, so deleting the
// robot can't take the Secret away from a connection. Its deletion must not wait for that connection.
func TestBughuntRobotDeletionWaitsForASecretItDoesNotOwn(t *testing.T) {
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)
	namespace := setup.connection.Namespace

	handMade := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "hand-made-credentials"},
		StringData: map[string]string{"username": "robot$someone", "password": "not-a-real-secret"},
	}
	require.NoError(t, setup.cl.Create(t.Context(), handMade))

	useSecret(t, setup, namespace, "main", "hand-made-credentials")

	robot := newSystemRobot(t, setup, namespace)
	robot.Name = "squatter"
	robot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	robot.Spec.Secret.Opaque.Enabled = ptr(true)
	robot.Spec.Secret.Opaque.Name = "hand-made-credentials"
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	key := client.ObjectKeyFromObject(robot)
	conflicted := awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonSecretConflict)
	t.Logf("before delete: %s", readyMessageOf(conflicted.Status.Conditions))

	require.NoError(t, setup.cl.Delete(t.Context(), robot))

	waiting := awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	t.Logf("after delete: %s", readyMessageOf(waiting.Status.Conditions))

	awaitObjectGone(t, setup.cl, &harborv1alpha1.HarborSystemRobot{ObjectMeta: robot.ObjectMeta}, "a robot that owns no Secret a connection uses is deleted")
}

// The migration done through the wrong connection: the robot names the managing connection's own Secret and is
// refused with OperatorCredential, so it never writes or owns that Secret. Deleting it must not wait for the
// connection, which in turn waits for the robot.
func TestBughuntRefusedRobotNamingItsConnectionsSecretDeadlocksOnDeletion(t *testing.T) {
	t.Parallel()

	setup := readyRobotProjectWithSystemRobot(t, true, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true }, nil)

	secret := &corev1.Secret{}
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKey{Namespace: setup.connection.Namespace, Name: setup.connection.Spec.CredentialsSecretRef.Name}, secret))
	operatorName := string(secret.Data["username"])

	robot := newSystemRobot(t, setup, setup.connection.Namespace)
	robot.Spec.Name = operatorName[len("robot$"):]
	robot.Spec.Adopt = true
	robot.Spec.Secret.Opaque.Enabled = ptr(true)
	robot.Spec.Secret.Opaque.Name = setup.connection.Spec.CredentialsSecretRef.Name
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	key := client.ObjectKeyFromObject(robot)
	awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonOperatorCredential)

	require.NoError(t, setup.cl.Delete(t.Context(), robot))
	waiting := awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	t.Logf("robot after delete: %s", readyMessageOf(waiting.Status.Conditions))

	// The project is the connection's other dependent; it goes first so only the robot holds the connection.
	require.NoError(t, setup.cl.Delete(t.Context(), setup.project))
	awaitObjectGone(t, setup.cl, &harborv1alpha1.HarborProject{ObjectMeta: setup.project.ObjectMeta}, "the project goes")

	require.NoError(t, setup.cl.Delete(t.Context(), setup.connection))

	defer func() {
		connection := &harborv1alpha1.HarborConnection{}
		if err := setup.cl.Get(context.Background(), client.ObjectKeyFromObject(setup.connection), connection); err == nil {
			t.Logf("connection at the end: deleting=%v ready=%s", !connection.DeletionTimestamp.IsZero(), readyMessageOf(connection.Status.Conditions))
		} else {
			t.Logf("connection at the end: %v", err)
		}
	}()

	awaitObjectGone(t, setup.cl, &harborv1alpha1.HarborSystemRobot{ObjectMeta: robot.ObjectMeta}, "the refused robot goes")
}
