//go:build integration

package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// deletionSetup creates a namespace with a ready connection, and a unique Harbor project name that is cleaned up,
// with its repositories, when the test ends.
func deletionSetup(t *testing.T, cl client.Client, instance *harborfixture.Harbor, namespace string) (*harborv1alpha1.HarborConnection, string) {
	t.Helper()

	require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))

	name := harborfixture.UniqueName(t, "op")
	t.Cleanup(func() { instance.DeleteProject(context.Background(), t, name) })

	return readyConnection(t, cl, instance, namespace, nil), name
}

func awaitGone(t *testing.T, cl client.Client, key client.ObjectKey, message string) {
	t.Helper()

	require.Eventually(t, func() bool {
		return cl.Get(t.Context(), key, &harborv1alpha1.HarborProject{}) != nil
	}, projectWait, 200*time.Millisecond, message)
}

func newRobot(namespace, name string, project client.ObjectKey) *harborv1alpha1.HarborProjectRobot {
	return &harborv1alpha1.HarborProjectRobot{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: harborv1alpha1.HarborProjectRobotSpec{
			ProjectRef:  harborv1alpha1.ObjectReference{Namespace: project.Namespace, Name: project.Name},
			Permissions: []harborv1alpha1.RobotAccess{{Resource: "repository", Action: "pull"}},
		},
	}
}

func TestProjectDeletion(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	cl := startOperator(t, true)

	t.Run("policy Delete deletes the empty Harbor project", func(t *testing.T) {
		connection, name := deletionSetup(t, cl, instance, "delete-empty")

		project := newProject("delete-empty", name, connection)
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
		require.NoError(t, cl.Create(t.Context(), project))
		awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.NoError(t, cl.Delete(t.Context(), project))
		awaitGone(t, cl, client.ObjectKeyFromObject(project), "the finalizer is removed once the project is deleted")

		_, err := admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})

	t.Run("policy Orphan leaves the Harbor project", func(t *testing.T) {
		connection, name := deletionSetup(t, cl, instance, "delete-orphan")

		project := newProject("delete-orphan", name, connection)
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyOrphan
		require.NoError(t, cl.Create(t.Context(), project))
		ready := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.NoError(t, cl.Delete(t.Context(), project))
		awaitGone(t, cl, client.ObjectKeyFromObject(project), "the finalizer is removed")

		found, err := admin.GetProjectByName(t.Context(), name)
		require.NoError(t, err, "the Harbor project stays")
		assert.Equal(t, int64(ready.Status.ProjectID), found.ID)
	})

	t.Run("a project holding an image reports DeleteBlocked until the image is removed", func(t *testing.T) {
		connection, name := deletionSetup(t, cl, instance, "delete-blocked")

		project := newProject("delete-blocked", name, connection)
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		instance.PushImage(t, name, "app", "v1")

		require.NoError(t, cl.Delete(t.Context(), project))
		blocked := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDeleteBlocked)
		assert.NotEmpty(t, readyMessage(blocked), "the message is Harbor's")

		_, err := admin.GetProjectByName(t.Context(), name)
		require.NoError(t, err, "the Harbor project stays while it holds an image")

		// Removing the repositories (the project itself stays: the resource's finalizer still holds it) lets the
		// deletion finish.
		removeRepositories(t, instance, name)
		awaitGone(t, cl, key, "the deletion finishes once the image is removed")

		_, err = admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})

	t.Run("deletion waits for robots in namespaces the project admits", func(t *testing.T) {
		connection, name := deletionSetup(t, cl, instance, "delete-robots")
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "delete-robots-other"}}))

		project := newProject("delete-robots", name, connection)
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		admitted := newRobot("delete-robots", "ci", key)
		require.NoError(t, cl.Create(t.Context(), admitted))
		// The project does not admit this namespace, so its robot does not hold the deletion.
		outsider := newRobot("delete-robots-other", "outsider", key)
		require.NoError(t, cl.Create(t.Context(), outsider))

		require.NoError(t, cl.Delete(t.Context(), project))
		waiting := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
		assert.Contains(t, readyMessage(waiting), "delete-robots/ci")
		assert.NotContains(t, readyMessage(waiting), "outsider")

		_, err := admin.GetProjectByName(t.Context(), name)
		require.NoError(t, err, "the Harbor project stays while a robot references it")

		require.NoError(t, cl.Delete(t.Context(), admitted))
		awaitGone(t, cl, key, "the deletion finishes once the robot is gone")

		_, err = admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})

	t.Run("a resource in Conflict never touches the project, even with an ID", func(t *testing.T) {
		connection, name := deletionSetup(t, cl, instance, "delete-conflict")

		owner := newProject("delete-conflict", name, connection)
		require.NoError(t, cl.Create(t.Context(), owner))
		ready := awaitProject(t, cl, client.ObjectKeyFromObject(owner), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		// The owner must be the older claimant; creation times have one-second resolution.
		require.Eventually(t, func() bool {
			return time.Now().Add(-time.Second).After(ready.CreationTimestamp.Time)
		}, 5*time.Second, 50*time.Millisecond)

		second := newProject("delete-conflict", name, connection)
		second.Name = "second"
		require.NoError(t, cl.Create(t.Context(), second))
		secondKey := client.ObjectKeyFromObject(second)
		awaitProject(t, cl, secondKey, metav1.ConditionFalse, harborv1alpha1.ReasonConflict)

		// The second resource records the owner's ID, as a stale or copied status would.
		require.Eventually(t, func() bool {
			current := &harborv1alpha1.HarborProject{}
			if err := cl.Get(t.Context(), secondKey, current); err != nil {
				return false
			}

			current.Status.ProjectID = ready.Status.ProjectID

			return cl.Status().Update(t.Context(), current) == nil
		}, 10*time.Second, 100*time.Millisecond)

		require.NoError(t, cl.Delete(t.Context(), second))
		awaitGone(t, cl, secondKey, "the second resource is released")

		found, err := admin.GetProjectByName(t.Context(), name)
		require.NoError(t, err, "the owner's Harbor project stays")
		assert.Equal(t, int64(ready.Status.ProjectID), found.ID)
		awaitProject(t, cl, client.ObjectKeyFromObject(owner), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	})
}

// removeRepositories deletes every repository of the Harbor project.
func removeRepositories(t *testing.T, instance *harborfixture.Harbor, projectName string) {
	t.Helper()

	require.NoError(t, instance.DeleteRepositories(t, projectName))
}
