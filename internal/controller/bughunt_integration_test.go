//go:build integration

package controller_test

import (
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/configure"
	projectapi "github.com/goharbor/go-client/pkg/sdk/v2.0/client/project"
	robotapi "github.com/goharbor/go-client/pkg/sdk/v2.0/client/robot"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// A robot whose dockerconfigjson and opaque Secrets share a name: the two writes fight forever.
func TestBughuntSecretNameCollisionFlaps(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	robot := ciRobot("default", setup.project)
	robot.Spec.ResyncInterval = &metav1.Duration{Duration: time.Hour}
	robot.Spec.Secret.DockerConfigJSON.Name = "same"
	robot.Spec.Secret.Opaque.Enabled = ptr(true)
	robot.Spec.Secret.Opaque.Name = "same"
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	time.Sleep(5 * time.Second)

	uids := map[string]string{}
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		secret := &corev1.Secret{}
		if err := setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "same"}, secret); err == nil {
			uids[string(secret.UID)] = string(secret.Type)
		}

		time.Sleep(50 * time.Millisecond)
	}

	current := &harborv1alpha1.HarborProjectRobot{}
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKeyFromObject(robot), current))
	t.Logf("distinct Secret UIDs seen in 10s: %d; robot conditions %+v", len(uids), current.Status.Conditions)
	assert.LessOrEqual(t, len(uids), 1, "the Secret keeps being deleted and re-created")
}

// Admission withdrawn, then the connection deleted (it does not wait for a project it doesn't admit): deleting the
// project afterwards never finishes.
func TestBughuntProjectDeletionAfterConnectionGone(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperatorLogging(t, testr.New(t), true)

	require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}}))

	connection := readyConnection(t, cl, instance, "default", func(connection *harborv1alpha1.HarborConnection) {
		connection.Spec.AllowedNamespaces.Names = []string{"apps"}
	})

	project := newProject("apps", name, connection)
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)
	awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

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
	awaitGone(t, cl, client.ObjectKeyFromObject(connection), "the connection goes: it admits no project")
	// Let the operator's cache see the connection go.
	time.Sleep(5 * time.Second)

	before := &harborv1alpha1.HarborProject{}
	require.NoError(t, cl.Get(t.Context(), key, before))
	t.Logf("before delete: projectID=%d conditions=%+v", before.Status.ProjectID, before.Status.Conditions)

	require.NoError(t, cl.Delete(t.Context(), project))
	defer func() {
		_, err := admin.GetProjectByName(t.Context(), name)
		t.Logf("Harbor project after: err=%v", err)
	}()

	gone := false

	require.Eventually(t, func() bool {
		gone = apierrors.IsNotFound(cl.Get(t.Context(), key, &harborv1alpha1.HarborProject{}))
		return gone
	}, 30*time.Second, 200*time.Millisecond, "deleting a project whose admission was withdrawn should orphan it")
}

// A robot in Conflict that also records the robot's ID deletes the owner's Harbor robot when it is deleted.
func TestBughuntConflictingRobotDeletesTheOwnersRobot(t *testing.T) {
	setup := readyRobotProject(t, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true },
		func(project *harborv1alpha1.HarborProject) {
			project.Spec.AllowedNamespaces.Names = []string{"team-a", "team-b"}
		})
	cl := setup.cl

	for _, namespace := range []string{"team-a", "team-b"} {
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	}

	setAllowed := func(names ...string) {
		updateProject(t, cl, client.ObjectKeyFromObject(setup.project), func(project *harborv1alpha1.HarborProject) {
			project.Spec.AllowedNamespaces.Names = names
		})
	}

	first := ciRobot("team-a", setup.project)
	require.NoError(t, cl.Create(t.Context(), first))
	owner := awaitRobot(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	setAllowed("team-b")
	awaitRobot(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

	second := ciRobot("team-b", setup.project)
	second.Spec.Adopt = true
	require.NoError(t, cl.Create(t.Context(), second))
	adopted := awaitRobot(t, cl, client.ObjectKeyFromObject(second), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	require.Equal(t, owner.Status.RobotID, adopted.Status.RobotID)

	setAllowed("team-a", "team-b")
	awaitRobot(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	loser := awaitRobot(t, cl, client.ObjectKeyFromObject(second), metav1.ConditionFalse, harborv1alpha1.ReasonConflict)
	t.Logf("loser status robotID=%d", loser.Status.RobotID)

	require.NoError(t, cl.Delete(t.Context(), loser))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(loser), &harborv1alpha1.HarborProjectRobot{}))
	}, projectWait, 200*time.Millisecond)

	_, err := setup.admin.GetRobot(t.Context(), owner.Status.RobotID)
	require.NoError(t, err, "deleting the Conflict loser must not delete the owner's Harbor robot")
}

// A create that Harbor refused leaves the create-pending annotation; a project made later by someone else with that
// name is then taken as the resource's own, without adopt.
func TestBughuntStaleCreatePendingTakesOverAForeignProject(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperatorLogging(t, testr.New(t), true)

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

	connection := readyConnection(t, cl, &connectionInstance, "default", nil)

	// Only admins may create projects, so the operator's create is refused.
	restrict := func(value string) {
		_, err := instance.Client(t).Configure.UpdateConfigurations(t.Context(), &configure.UpdateConfigurationsParams{
			Configurations: &models.Configurations{ProjectCreationRestriction: &value},
		})
		require.NoError(t, err)
	}
	restrict("adminonly")
	t.Cleanup(func() { restrict("everyone") })

	project := newProject("default", name, connection)
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)
	time.Sleep(15 * time.Second)
	failed := &harborv1alpha1.HarborProject{}
	require.NoError(t, cl.Get(t.Context(), key, failed))
	t.Logf("after the failed create: annotations=%v status=%+v", failed.Annotations, failed.Status)
	if _, err := admin.GetProjectByName(t.Context(), name); err == nil {
		t.Fatalf("the project exists in Harbor already")
	}

	restrict("everyone")

	// Someone else creates a project of that name in Harbor.
	public := false
	_, err := instance.Client(t).Project.CreateProject(t.Context(), &projectapi.CreateProjectParams{Project: &models.ProjectReq{ProjectName: name, Public: &public}})
	require.NoError(t, err)

	foreign, err := admin.GetProjectByName(t.Context(), name)
	require.NoError(t, err)

	time.Sleep(10 * time.Second)

	current := &harborv1alpha1.HarborProject{}
	require.NoError(t, cl.Get(t.Context(), key, current))
	ready := meta.FindStatusCondition(current.Status.Conditions, harborv1alpha1.ConditionReady)
	t.Logf("after the foreign create: projectID=%d (foreign %d) Ready=%+v", current.Status.ProjectID, foreign.ID, ready)
	assert.Equal(t, harborv1alpha1.ReasonAlreadyExists, ready.Reason, "a project the resource did not create is not taken over without adopt")

	require.NoError(t, cl.Delete(t.Context(), current))
	awaitGone(t, cl, key, "resource deleted")

	_, err = admin.GetProjectByName(t.Context(), name)
	assert.NoError(t, err, "the foreign project must not be deleted: %v", err)
	_ = harborfixture.UniqueName
	_ = harbor.IsNotFound
}

// The robot variant: a refused create leaves create-pending, and a robot made later in Harbor is taken as its own.
func TestBughuntStaleCreatePendingTakesOverAForeignRobot(t *testing.T) {
	setup := readyRobotProjectWithSystemRobot(t, true, nil, nil)

	robot := ciRobot("default", setup.project)
	// The connection's system robot doesn't hold artifact:delete, so Harbor refuses the create.
	robot.Spec.Permissions = []harborv1alpha1.RobotAccess{{Resource: "artifact", Action: "delete"}}
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)
	failed := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonForbidden)
	t.Logf("after the refused create: annotations=%v", failed.Annotations)

	created, err := setup.instance.Client(t).Robot.CreateRobot(t.Context(), &robotapi.CreateRobotParams{Robot: &models.RobotCreate{
		Name: "ci", Description: "made by hand", Duration: -1, Level: "project",
		Permissions: []*models.RobotPermission{{Kind: "project", Namespace: setup.project.Spec.Name, Access: []*models.Access{{Resource: "repository", Action: "pull", Effect: "allow"}}}},
	}})
	require.NoError(t, err)
	foreign := struct {
		ID           int64
		Name, Secret string
	}{created.Payload.ID, created.Payload.Name, created.Payload.Secret}

	time.Sleep(10 * time.Second)

	current := &harborv1alpha1.HarborProjectRobot{}
	require.NoError(t, setup.cl.Get(t.Context(), key, current))
	ready := meta.FindStatusCondition(current.Status.Conditions, harborv1alpha1.ConditionReady)
	t.Logf("after the foreign create: robotID=%d (foreign %d) Ready=%s/%s", current.Status.RobotID, foreign.ID, ready.Status, ready.Reason)

	_, err = setup.admin.GetRobot(t.Context(), foreign.ID)
	assert.NoError(t, err, "the hand-made robot must not be deleted")
	assert.NoError(t, push(t, setup.instance, foreign.Name, foreign.Secret, setup.project.Spec.Name, "app"), "the hand-made robot's secret still works")
	assert.Equal(t, harborv1alpha1.ReasonAlreadyExists, ready.Reason)
}
