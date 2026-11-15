//go:build integration

package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// deletionSetup creates a ready connection in namespace, and a unique Harbor project name that is cleaned up,
// with its repositories, when the test ends.
func deletionSetup(t *testing.T, cl client.Client, instance *harborfixture.Harbor, namespace string) (*harborv1alpha1.HarborConnection, string) {
	t.Helper()

	name := runUniqueName(t, "op")
	t.Cleanup(func() { instance.DeleteProject(context.Background(), t, name) })

	return readyConnection(t, cl, instance, namespace, nil), name
}

// runUniqueName is harborfixture.UniqueName with a random suffix. Resources of earlier runs stay in the shared API server
// (namespaces are never removed there), so a name that repeats between runs (-count) would be claimed by both.
func runUniqueName(t *testing.T, prefix string) string {
	t.Helper()

	return harborfixture.UniqueName(t, prefix) + "-" + randomHex(t, 3)
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
	t.Parallel()

	instance := harborfixture.FromEnv(t)
	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	cl := startOperator(t, true)

	t.Run("policy Delete deletes the empty Harbor project", func(t *testing.T) {
		t.Parallel()
		connection, name := deletionSetup(t, cl, instance, newNamespace(t, cl, "delete-empty"))

		project := newProject(connection.Namespace, name, connection)
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
		require.NoError(t, cl.Create(t.Context(), project))
		awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.NoError(t, cl.Delete(t.Context(), project))
		awaitGone(t, cl, client.ObjectKeyFromObject(project), "the finalizer is removed once the project is deleted")

		_, err := admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})

	t.Run("policy Orphan leaves the Harbor project", func(t *testing.T) {
		t.Parallel()
		connection, name := deletionSetup(t, cl, instance, newNamespace(t, cl, "delete-orphan"))

		project := newProject(connection.Namespace, name, connection)
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
		t.Parallel()
		connection, name := deletionSetup(t, cl, instance, newNamespace(t, cl, "delete-blocked"))

		project := newProject(connection.Namespace, name, connection)
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
		t.Parallel()
		connection, name := deletionSetup(t, cl, instance, newNamespace(t, cl, "delete-robots"))
		otherNamespace := newNamespace(t, cl, "delete-robots-other")

		project := newProject(connection.Namespace, name, connection)
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		admitted := newRobot(connection.Namespace, "ci", key)
		require.NoError(t, cl.Create(t.Context(), admitted))
		// The project does not admit this namespace, so its robot does not hold the deletion.
		outsider := newRobot(otherNamespace, "outsider", key)
		require.NoError(t, cl.Create(t.Context(), outsider))

		require.NoError(t, cl.Delete(t.Context(), project))
		waiting := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
		assert.Contains(t, readyMessage(waiting), connection.Namespace+"/ci")
		assert.NotContains(t, readyMessage(waiting), otherNamespace+"/outsider")

		_, err := admin.GetProjectByName(t.Context(), name)
		require.NoError(t, err, "the Harbor project stays while a robot references it")

		require.NoError(t, cl.Delete(t.Context(), admitted))
		awaitGone(t, cl, key, "the deletion finishes once the robot is gone")

		_, err = admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})

	t.Run("deletion waits for retention policies the project admits", func(t *testing.T) {
		t.Parallel()
		connection, name := deletionSetup(t, cl, instance, newNamespace(t, cl, "delete-retention"))

		project := newProject(connection.Namespace, name, connection)
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		policy := keepVersionsAndRecentPulls(project.Namespace, "retention", project)
		require.NoError(t, cl.Create(t.Context(), policy))

		require.NoError(t, cl.Delete(t.Context(), project))
		waiting := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
		assert.Contains(t, readyMessage(waiting), "HarborRetentionPolicy "+project.Namespace+"/retention")

		require.NoError(t, cl.Delete(t.Context(), policy))
		awaitGone(t, cl, key, "the deletion finishes once the retention policy is gone")
	})

	t.Run("a resource in Conflict never touches the project, even with an ID", func(t *testing.T) {
		t.Parallel()
		connection, name := deletionSetup(t, cl, instance, newNamespace(t, cl, "delete-conflict"))

		owner := newProject(connection.Namespace, name, connection)
		require.NoError(t, cl.Create(t.Context(), owner))
		ready := awaitProject(t, cl, client.ObjectKeyFromObject(owner), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		// The owner must be the older claimant; creation times have one-second resolution.
		require.Eventually(t, func() bool {
			return time.Now().Add(-time.Second).After(ready.CreationTimestamp.Time)
		}, 5*time.Second, 50*time.Millisecond)

		second := newProject(connection.Namespace, name, connection)
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

	t.Run("a project whose connection stopped admitting it and was then deleted is orphaned", func(t *testing.T) {
		t.Parallel()
		setup := withdrawnAndDeletedConnection(t, cl, instance, "gone-conn", false)

		require.NoError(t, cl.Delete(t.Context(), setup.project))
		awaitGone(t, cl, setup.key, "the finalizer is removed without the connection")
		awaitEvent(t, cl, setup.project, harborv1alpha1.ReasonNotAllowed)

		found, err := admin.GetProjectByName(t.Context(), setup.harborName)
		require.NoError(t, err, "the Harbor project stays")
		assert.Equal(t, int64(setup.project.Status.ProjectID), found.ID)
	})

	t.Run("a robot and its project whose connection stopped admitting them and was then deleted are orphaned", func(t *testing.T) {
		t.Parallel()
		setup := withdrawnAndDeletedConnection(t, cl, instance, "gone-conn-robot", true)

		require.NoError(t, cl.Delete(t.Context(), setup.robot))
		require.NoError(t, cl.Delete(t.Context(), setup.project))

		robotKey := client.ObjectKeyFromObject(setup.robot)

		require.Eventually(t, func() bool {
			return apierrors.IsNotFound(cl.Get(t.Context(), robotKey, &harborv1alpha1.HarborProjectRobot{}))
		}, projectWait, 200*time.Millisecond, "the robot's finalizer is removed without the connection")
		awaitGone(t, cl, setup.key, "the project's finalizer is removed once the robot is gone")

		_, err := admin.GetRobot(t.Context(), setup.robot.Status.RobotID)
		require.NoError(t, err, "the Harbor robot stays")

		_, err = admin.GetProjectByName(t.Context(), setup.harborName)
		require.NoError(t, err, "the Harbor project stays")
	})

	t.Run("deletion of an admitted project waits while its connection's credentials are missing", func(t *testing.T) {
		t.Parallel()
		connection, name := deletionSetup(t, cl, instance, newNamespace(t, cl, "delete-no-creds"))

		project := newProject(connection.Namespace, name, connection)
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.NoError(t, cl.Delete(t.Context(), credentials(connection.Namespace, "harbor-creds", "", "")))
		awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonCredentialsInvalid)

		require.NoError(t, cl.Delete(t.Context(), project))
		awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonCredentialsInvalid)

		_, err := admin.GetProjectByName(t.Context(), name)
		require.NoError(t, err, "the Harbor project stays while the deletion waits")

		// With the credentials back the deletion finishes.
		require.NoError(t, cl.Create(t.Context(), credentials(connection.Namespace, "harbor-creds", instance.Username, instance.Password)))
		awaitGone(t, cl, key, "the deletion finishes once the credentials are back")

		_, err = admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})
}

// goneConnectionSetup is a ready project, and optionally a ready robot, whose connection no longer exists.
type goneConnectionSetup struct {
	project    *harborv1alpha1.HarborProject
	robot      *harborv1alpha1.HarborProjectRobot
	key        client.ObjectKey
	harborName string
}

// withdrawnAndDeletedConnection creates a connection in its own namespace that admits the namespace prefix, a ready
// project (and, withRobot, a ready robot) there, then withdraws the admission and deletes the connection. It returns once the operator has
// seen the connection go.
func withdrawnAndDeletedConnection(t *testing.T, cl client.Client, instance *harborfixture.Harbor, prefix string, withRobot bool) goneConnectionSetup {
	t.Helper()

	connectionNamespace, namespace := newNamespace(t, cl, prefix+"-harbor"), newNamespace(t, cl, prefix)

	harborName := runUniqueName(t, "op")
	t.Cleanup(func() { instance.DeleteProject(context.Background(), t, harborName) })

	connection := readyConnection(t, cl, instance, connectionNamespace, func(connection *harborv1alpha1.HarborConnection) {
		connection.Spec.AllowedNamespaces.Names = []string{namespace}
	})

	project := newProject(namespace, harborName, connection)
	project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)
	project = awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	var robot *harborv1alpha1.HarborProjectRobot
	if withRobot {
		robot = newRobot(namespace, "ci", key)
		robot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
		require.NoError(t, cl.Create(t.Context(), robot))
		robot = awaitRobot(t, cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	}

	require.Eventually(t, func() bool {
		current := &harborv1alpha1.HarborConnection{}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(connection), current); err != nil {
			return false
		}

		current.Spec.AllowedNamespaces.Names = nil

		return cl.Update(t.Context(), current) == nil
	}, 10*time.Second, 100*time.Millisecond)
	awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

	require.NoError(t, cl.Delete(t.Context(), connection))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &harborv1alpha1.HarborConnection{}))
	}, projectWait, 200*time.Millisecond, "the connection goes: it admits no resource")
	// The project reports the missing connection only once the operator's cache no longer holds it.
	awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonNotFound)

	return goneConnectionSetup{project: project, robot: robot, key: key, harborName: harborName}
}

// removeRepositories deletes every repository of the Harbor project.
func removeRepositories(t *testing.T, instance *harborfixture.Harbor, projectName string) {
	t.Helper()

	require.NoError(t, instance.DeleteRepositories(t, projectName))
}
