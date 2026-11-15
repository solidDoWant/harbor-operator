//go:build integration

package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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

var pushPull = []harborv1alpha1.RobotAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}}

// robotSetup is a ready connection and project for robot tests.
type robotSetup struct {
	cl         client.Client
	instance   *harborfixture.Harbor
	admin      *harbor.Client
	connection *harborv1alpha1.HarborConnection
	project    *harborv1alpha1.HarborProject
}

// readyRobotProject starts the operator with a connection and a ready project in the default namespace. The Harbor
// project is deleted with its repositories and robots once the operator has stopped.
func readyRobotProject(t *testing.T, changeConnection func(*harborv1alpha1.HarborConnection), changeProject func(*harborv1alpha1.HarborProject)) *robotSetup {
	t.Helper()

	return readyRobotProjectLogging(t, logr.Logger{}, false, changeConnection, changeProject)
}

// systemRobotPermissions are the permissions the admin docs list for the operator's system robot, with push and pull
// on all projects.
func systemRobotPermissions() []*models.RobotPermission {
	access := func(resource string, actions ...string) []*models.Access {
		result := make([]*models.Access, 0, len(actions))
		for _, action := range actions {
			result = append(result, &models.Access{Resource: resource, Action: action, Effect: "allow"})
		}

		return result
	}

	return []*models.RobotPermission{
		{Kind: "system", Namespace: "/", Access: append(append(access("project", "create", "list"),
			access("quota", "read", "update")...), access("robot", "create", "read", "list", "delete")...)},
		{Kind: "project", Namespace: "*", Access: append(append(access("project", "read", "update", "delete"),
			access("robot", "create", "read", "list", "delete")...), append(access("repository", "push", "pull"),
			access("tag-retention", "create", "read", "update", "delete")...)...)},
	}
}

// readyRobotProjectWithSystemRobot is readyRobotProject, with the connection signing in as a new system robot when
// useSystemRobot is set.
func readyRobotProjectWithSystemRobot(t *testing.T, useSystemRobot bool, changeConnection func(*harborv1alpha1.HarborConnection), changeProject func(*harborv1alpha1.HarborProject)) *robotSetup {
	return readyRobotProjectLogging(t, logr.Logger{}, useSystemRobot, changeConnection, changeProject)
}

// readyRobotProjectLogging is readyRobotProjectWithSystemRobot with the operator's log sent to logger.
func readyRobotProjectLogging(t *testing.T, logger logr.Logger, useSystemRobot bool, changeConnection func(*harborv1alpha1.HarborConnection), changeProject func(*harborv1alpha1.HarborProject)) *robotSetup {
	t.Helper()

	return readyRobotProjectWith(t, operatorOptions{logger: logger}, useSystemRobot, changeConnection, changeProject)
}

// readyRobotProjectWith is readyRobotProjectWithSystemRobot with the operator started with options; the project and
// robot controllers always run.
func readyRobotProjectWith(t *testing.T, options operatorOptions, useSystemRobot bool, changeConnection func(*harborv1alpha1.HarborConnection), changeProject func(*harborv1alpha1.HarborProject)) *robotSetup {
	t.Helper()

	instance := harborfixture.FromEnv(t)
	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	projectName := harborfixture.UniqueName(t, "op")
	t.Cleanup(func() { instance.DeleteProject(context.Background(), t, projectName) })

	options.withProjects = true
	cl := startOperatorWith(t, options)

	connectionInstance := *instance
	if useSystemRobot {
		connectionInstance.Username, connectionInstance.Password = instance.CreateSystemRobot(t, systemRobotPermissions())
	}

	connection := readyConnection(t, cl, &connectionInstance, "default", changeConnection)

	project := newProject("default", projectName, connection)
	if changeProject != nil {
		changeProject(project)
	}

	require.NoError(t, cl.Create(t.Context(), project))
	project = awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	return &robotSetup{cl: cl, instance: instance, admin: admin, connection: connection, project: project}
}

func ciRobot(namespace string, project *harborv1alpha1.HarborProject) *harborv1alpha1.HarborProjectRobot {
	return &harborv1alpha1.HarborProjectRobot{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ci"},
		Spec: harborv1alpha1.HarborProjectRobotSpec{
			ProjectRef:     harborv1alpha1.ObjectReference{Namespace: project.Namespace, Name: project.Name},
			Description:    "pushes from CI",
			Duration:       -1,
			Permissions:    pushPull,
			ResyncInterval: &metav1.Duration{Duration: 2 * time.Second},
		},
	}
}

func awaitRobot(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborProjectRobot {
	t.Helper()

	robot := &harborv1alpha1.HarborProjectRobot{}

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), key, robot); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(robot.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == status && condition.Reason == reason && condition.ObservedGeneration == robot.Generation
	}, projectWait, 200*time.Millisecond, "waiting for Ready=%s %s, have %+v", status, reason, robot.Status.Conditions)

	return robot
}

// dockerCredentials reads the username and password for registry from the robot's dockerconfigjson Secret.
func dockerCredentials(t *testing.T, cl client.Client, namespace, secretName, registry string) (string, string) {
	t.Helper()

	secret := &corev1.Secret{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: secretName}, secret))
	require.Equal(t, corev1.SecretTypeDockerConfigJson, secret.Type)

	var config struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config))
	require.Contains(t, config.Auths, registry)

	return config.Auths[registry].Username, config.Auths[registry].Password
}

// push writes a tiny image to project/repository:latest with the credentials and returns the error.
func push(t *testing.T, instance *harborfixture.Harbor, username, password, project, repository string) error {
	t.Helper()

	image, err := random.Image(64, 1)
	require.NoError(t, err)

	reference, err := name.ParseReference(fmt.Sprintf("%s/%s/%s:latest", instance.Registry(t), project, repository), name.Insecure)
	require.NoError(t, err)

	return remote.Write(reference, image, remote.WithAuth(&authn.Basic{Username: username, Password: password}), remote.WithContext(t.Context()))
}

func TestRobotWithPushAndPullCanPushOnlyToItsProject(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)
	other := setup.instance.CreateProject(t)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	ready := awaitRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Nil(t, ready.Status.CreatePending, "the create marker is cleared once the Secrets are written")

	found, err := setup.admin.GetRobot(t.Context(), ready.Status.RobotID)
	require.NoError(t, err)
	assert.Equal(t, found.Name, ready.Status.HarborName)
	assert.Equal(t, setup.project.Spec.Name, found.ProjectName)
	assert.ElementsMatch(t, []harbor.RobotAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}}, found.Permissions)

	username, password := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
	assert.Equal(t, found.Name, username)

	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the robot pushes to its own project")
	require.Error(t, push(t, setup.instance, username, password, other, "app"), "the robot cannot push to another project")
}

func TestRobotInANamespaceTheProjectDoesNotAdmit(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	namespace := harborfixture.UniqueName(t, "robots")
	require.NoError(t, setup.cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))

	robot := ciRobot(namespace, setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

	_, err := setup.admin.FindProjectRobot(t.Context(), int64(setup.project.Status.ProjectID), "ci")
	require.True(t, harbor.IsNotFound(err), "no Harbor robot is created for a namespace the project does not admit: %v", err)

	require.Eventually(t, func() bool {
		project := &harborv1alpha1.HarborProject{}
		if err := setup.cl.Get(t.Context(), client.ObjectKeyFromObject(setup.project), project); err != nil {
			return false
		}

		project.Spec.AllowedNamespaces.Names = []string{namespace}

		return setup.cl.Update(t.Context(), project) == nil
	}, 10*time.Second, 100*time.Millisecond)

	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.NotZero(t, ready.Status.RobotID)
}

func TestRobotDriftInHarborIsPutBack(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	ready := awaitRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	id := ready.Status.RobotID
	require.NoError(t, setup.admin.UpdateRobot(t.Context(), id, setup.project.Spec.Name, harbor.RobotSpec{
		Description: "changed in the UI",
		Duration:    30,
		Disabled:    true,
		Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}, {Resource: "artifact", Action: "delete"}},
	}))

	var found *harbor.Robot

	require.Eventually(t, func() bool {
		var err error

		found, err = setup.admin.GetRobot(t.Context(), id)

		return err == nil && found.Description == "pushes from CI" && found.Duration == -1 && !found.Disabled && len(found.Permissions) == 2 &&
			assert.ObjectsAreEqual(map[harbor.RobotAccess]bool{{Resource: "repository", Action: "push"}: true, {Resource: "repository", Action: "pull"}: true},
				map[harbor.RobotAccess]bool{found.Permissions[0]: true, found.Permissions[1]: true})
	}, projectWait, 500*time.Millisecond, "waiting for Harbor's robot to match the spec again, have %+v", found)

	username, password := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the robot's Secret still works")
}

// driftRobotInHarbor changes the robot's description, permissions, duration and disabled state in Harbor as an admin.
func driftRobotInHarbor(t *testing.T, setup *robotSetup, id int64) {
	t.Helper()

	require.NoError(t, setup.admin.UpdateRobot(t.Context(), id, setup.project.Spec.Name, harbor.RobotSpec{
		Description: "changed in the UI",
		Duration:    30,
		Disabled:    true,
		Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}, {Resource: "artifact", Action: "delete"}},
	}))
}

func TestRobotDriftInHarborIsPutBackByReplacingTheRobotWithASystemRobotConnection(t *testing.T) {
	setup := readyRobotProjectWithSystemRobot(t, true, nil, nil)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)
	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	username, old := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
	oldID := ready.Status.RobotID
	driftRobotInHarbor(t, setup, oldID)

	replaced := &harborv1alpha1.HarborProjectRobot{}

	require.Eventually(t, func() bool {
		return setup.cl.Get(t.Context(), key, replaced) == nil && replaced.Status.RobotID != 0 && replaced.Status.RobotID != oldID
	}, projectWait, 500*time.Millisecond, "waiting for status.robotID to change")
	awaitEvent(t, setup.cl, replaced, harborv1alpha1.EventReasonReplaced)

	found, err := setup.admin.GetRobot(t.Context(), replaced.Status.RobotID)
	require.NoError(t, err)
	assert.Equal(t, "pushes from CI", found.Description)
	assert.EqualValues(t, -1, found.Duration)
	assert.False(t, found.Disabled)
	assert.ElementsMatch(t, []harbor.RobotAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}}, found.Permissions)

	_, password := awaitNewDockerPassword(t, setup, "ci-dockerconfig", old)
	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the rewritten Secret works")
}

func TestRobotIsNotReplacedWhenTheConnectionLacksADesiredPermission(t *testing.T) {
	setup := readyRobotProjectWithSystemRobot(t, true, nil, nil)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)
	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	// The connection's system robot holds repository push and pull on all projects, but not artifact delete.
	updateRobot(t, setup.cl, key, func(robot *harborv1alpha1.HarborProjectRobot) {
		robot.Spec.Permissions = append(robot.Spec.Permissions, harborv1alpha1.RobotAccess{Resource: "artifact", Action: "delete"})
	})

	refused := awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonForbidden)
	condition := meta.FindStatusCondition(refused.Status.Conditions, harborv1alpha1.ConditionReady)
	require.NotNil(t, condition)
	assert.Contains(t, condition.Message, "artifact delete")
	assert.NotContains(t, condition.Message, "repository")
	assert.Equal(t, ready.Status.RobotID, refused.Status.RobotID, "the robot is not replaced")

	found, err := setup.admin.GetRobot(t.Context(), ready.Status.RobotID)
	require.NoError(t, err, "the Harbor robot is not deleted")
	assert.ElementsMatch(t, []harbor.RobotAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}}, found.Permissions)
}

func TestRobotSecretRenewalReplacesTheRobotWithASystemRobotConnection(t *testing.T) {
	tests := []struct {
		name  string
		renew func(t *testing.T, setup *robotSetup, key client.ObjectKey)
	}{
		{name: "Secret deleted", renew: func(t *testing.T, setup *robotSetup, _ client.ObjectKey) {
			require.NoError(t, setup.cl.Delete(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ci-dockerconfig"}}))
		}},
		{name: "rotate changed", renew: func(t *testing.T, setup *robotSetup, key client.ObjectKey) {
			updateRobot(t, setup.cl, key, func(robot *harborv1alpha1.HarborProjectRobot) { robot.Spec.Secret.Rotate = "second" })
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := readyRobotProjectWithSystemRobot(t, true, nil, nil)

			robot := ciRobot("default", setup.project)
			robot.Spec.Secret.Rotate = "first"
			require.NoError(t, setup.cl.Create(t.Context(), robot))
			key := client.ObjectKeyFromObject(robot)
			ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			username, old := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
			test.renew(t, setup, key)

			newUsername, password := awaitNewDockerPassword(t, setup, "ci-dockerconfig", old)
			require.NoError(t, push(t, setup.instance, newUsername, password, setup.project.Spec.Name, "app"), "the new secret works")
			require.Error(t, push(t, setup.instance, username, old, setup.project.Spec.Name, "app"), "the old secret no longer works")

			renewed := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
			assert.NotEqual(t, ready.Status.RobotID, renewed.Status.RobotID, "the robot is replaced")
		})
	}
}

func TestRobotAdoptsAnExistingHarborRobot(t *testing.T) {
	setup := readyRobotProject(t, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true }, nil)

	existing, err := setup.admin.CreateRobot(t.Context(), setup.project.Spec.Name, harbor.RobotSpec{
		Name: "ci", Description: "made by hand", Duration: -1, Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}},
	})
	require.NoError(t, err)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)

	secret := &corev1.Secret{}
	require.True(t, apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "ci-dockerconfig"}, secret)),
		"no Secret is written for a robot that is not adopted")

	require.Eventually(t, func() bool {
		current := &harborv1alpha1.HarborProjectRobot{}
		if err := setup.cl.Get(t.Context(), key, current); err != nil {
			return false
		}

		current.Spec.Adopt = true

		return setup.cl.Update(t.Context(), current) == nil
	}, 10*time.Second, 100*time.Millisecond)

	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, existing.ID, ready.Status.RobotID)
	awaitEvent(t, setup.cl, ready, harborv1alpha1.EventReasonAdopted)

	username, password := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
	assert.Equal(t, existing.Name, username)
	assert.NotEqual(t, existing.Secret, password, "adoption refreshes the robot's secret")
	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the refreshed secret works")
	require.Error(t, push(t, setup.instance, existing.Name, existing.Secret, setup.project.Spec.Name, "app"), "the old secret no longer works")
}

func TestRobotAdoptionNeedsTheConnectionToAllowIt(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	_, err := setup.admin.CreateRobot(t.Context(), setup.project.Spec.Name, harbor.RobotSpec{
		Name: "ci", Duration: -1, Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}},
	})
	require.NoError(t, err)

	robot := ciRobot("default", setup.project)
	robot.Spec.Adopt = true
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	awaitRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionFalse, harborv1alpha1.ReasonAdoptionNotAllowed)
}

func TestRobotDeletion(t *testing.T) {
	for _, test := range []struct {
		name        string
		policy      harborv1alpha1.DeletionPolicy
		harborStays bool
	}{
		{name: "Delete removes the Harbor robot", policy: harborv1alpha1.DeletionPolicyDelete},
		{name: "Orphan leaves the Harbor robot", policy: harborv1alpha1.DeletionPolicyOrphan, harborStays: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setup := readyRobotProject(t, nil, nil)

			robot := ciRobot("default", setup.project)
			robot.Spec.DeletionPolicy = test.policy
			robot.Spec.Secret.Opaque.Enabled = ptr(true)
			require.NoError(t, setup.cl.Create(t.Context(), robot))
			key := client.ObjectKeyFromObject(robot)
			ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			for _, secretName := range []string{"ci-dockerconfig", "ci-credentials"} {
				require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: secretName}, &corev1.Secret{}))
			}

			require.NoError(t, setup.cl.Delete(t.Context(), ready))

			require.Eventually(t, func() bool {
				return apierrors.IsNotFound(setup.cl.Get(t.Context(), key, &harborv1alpha1.HarborProjectRobot{}))
			}, projectWait, 200*time.Millisecond, "waiting for the robot resource to go")

			for _, secretName := range []string{"ci-dockerconfig", "ci-credentials"} {
				err := setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: secretName}, &corev1.Secret{})
				assert.True(t, apierrors.IsNotFound(err), "Secret %s is deleted with the robot: %v", secretName, err)
			}

			_, err := setup.admin.GetRobot(t.Context(), ready.Status.RobotID)
			if test.harborStays {
				require.NoError(t, err, "the Harbor robot stays")
			} else {
				require.True(t, harbor.IsNotFound(err), "the Harbor robot is deleted: %v", err)
			}
		})
	}
}

// A robot in Conflict can still hold the owner's robot ID in status: here the second resource adopted the robot while
// the owner's namespace was not admitted. Deleting it with policy Delete must leave the owner's Harbor robot alone.
func TestRobotInConflictDeletionLeavesTheOwnersHarborRobot(t *testing.T) {
	setup := readyRobotProject(t, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true },
		func(project *harborv1alpha1.HarborProject) {
			project.Spec.AllowedNamespaces.Names = []string{"team-a", "team-b"}
		})
	cl := setup.cl

	for _, namespace := range []string{"team-a", "team-b"} {
		require.NoError(t, client.IgnoreAlreadyExists(cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})))
	}

	setAllowed := func(names ...string) {
		updateProject(t, cl, client.ObjectKeyFromObject(setup.project), func(project *harborv1alpha1.HarborProject) {
			project.Spec.AllowedNamespaces.Names = names
		})
	}

	first := ciRobot("team-a", setup.project)
	require.NoError(t, cl.Create(t.Context(), first))
	t.Cleanup(func() { _ = cl.Delete(context.Background(), first) })

	owner := awaitRobot(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	setAllowed("team-b")
	awaitRobot(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

	second := ciRobot("team-b", setup.project)
	second.Spec.Adopt = true
	second.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, cl.Create(t.Context(), second))
	t.Cleanup(func() { _ = cl.Delete(context.Background(), second) })

	adopted := awaitRobot(t, cl, client.ObjectKeyFromObject(second), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	require.Equal(t, owner.Status.RobotID, adopted.Status.RobotID)

	setAllowed("team-a", "team-b")
	awaitRobot(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	loser := awaitRobot(t, cl, client.ObjectKeyFromObject(second), metav1.ConditionFalse, harborv1alpha1.ReasonConflict)
	require.Equal(t, owner.Status.RobotID, loser.Status.RobotID, "the resource in Conflict still records the robot's ID")

	require.NoError(t, cl.Delete(t.Context(), loser))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(loser), &harborv1alpha1.HarborProjectRobot{}))
	}, projectWait, 200*time.Millisecond, "waiting for the resource in Conflict to go")

	_, err := setup.admin.GetRobot(t.Context(), owner.Status.RobotID)
	require.NoError(t, err, "the owner's Harbor robot stays")

	kept := awaitRobot(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, owner.Status.RobotID, kept.Status.RobotID, "the owner keeps the same Harbor robot")
}
