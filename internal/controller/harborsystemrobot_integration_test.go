//go:build integration

package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

var pullOnly = []harborv1alpha1.SystemRobotProjectAccess{{Resource: "repository", Action: "pull"}}

// newSystemRobot is a system robot with a name unique to the test that pulls from every project and pushes to the
// named projects. It is deleted from Harbor when the test ends, whatever its deletion policy.
func newSystemRobot(t *testing.T, setup *robotSetup, namespace string, pushProjects ...string) *harborv1alpha1.HarborSystemRobot {
	t.Helper()

	robotName := runUniqueName(t, "sysrobot")
	t.Cleanup(func() {
		if found, err := setup.admin.FindSystemRobot(context.Background(), robotName); err == nil {
			_ = setup.admin.DeleteRobot(context.Background(), found.ID)
		}
	})

	permissions := []harborv1alpha1.ProjectPermission{{Projects: []string{"*"}, Access: pullOnly}}
	if len(pushProjects) > 0 {
		permissions = append(permissions, harborv1alpha1.ProjectPermission{Projects: pushProjects, Access: []harborv1alpha1.SystemRobotProjectAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}}})
	}

	return &harborv1alpha1.HarborSystemRobot{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "deployer"},
		Spec: harborv1alpha1.HarborSystemRobotSpec{
			ConnectionRef:      harborv1alpha1.ObjectReference{Namespace: setup.connection.Namespace, Name: setup.connection.Name},
			Name:               robotName,
			Description:        "deploys everywhere",
			Duration:           -1,
			ProjectPermissions: permissions,
			DeletionPolicy:     harborv1alpha1.DeletionPolicyDelete,
			ResyncInterval:     &metav1.Duration{Duration: 2 * time.Second},
		},
	}
}

func awaitSystemRobot(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborSystemRobot {
	t.Helper()

	robot := &harborv1alpha1.HarborSystemRobot{}

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), key, robot); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(robot.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == status && condition.Reason == reason && condition.ObservedGeneration == robot.Generation
	}, projectWait, 200*time.Millisecond, "waiting for Ready=%s %s, have %+v", status, reason, robot.Status.Conditions)

	return robot
}

// pull reads the image from project/repository:tag with the credentials and returns the error.
func pull(t *testing.T, instance *harborfixture.Harbor, username, password, project, repository string) error {
	t.Helper()

	reference, err := name.ParseReference(instance.Registry(t)+"/"+project+"/"+repository+":latest", name.Insecure)
	require.NoError(t, err)

	image, err := remote.Image(reference, remote.WithAuth(&authn.Basic{Username: username, Password: password}), remote.WithContext(t.Context()))
	if err != nil {
		return err
	}

	_, err = image.Manifest()

	return err
}

func TestSystemRobotPullsFromEveryProjectAndPushesOnlyToNamedOnes(t *testing.T) {
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)
	second := setup.instance.CreateProject(t)
	// The fixture names projects after the test, so the third project is made here under another name.
	other := runUniqueName(t, "other")
	t.Cleanup(func() { setup.instance.DeleteProject(context.Background(), t, other) })
	_, err := setup.admin.CreateProject(t.Context(), other, harbor.ProjectSettings{}, nil)
	require.NoError(t, err)
	setup.instance.PushImage(t, other, "base", "latest")

	robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name, second)
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	ready := awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Nil(t, ready.Status.CreatePending, "the create marker is cleared once the Secrets are written")

	found, err := setup.admin.GetRobot(t.Context(), ready.Status.RobotID)
	require.NoError(t, err)
	assert.Equal(t, harbor.LevelSystem, found.Level)
	assert.Equal(t, found.Name, ready.Status.HarborName)

	username, password := dockerCredentials(t, setup.cl, setup.project.Namespace, "deployer-dockerconfig", setup.instance.Registry(t))
	assert.Equal(t, found.Name, username)

	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the robot pushes to the first named project")
	require.NoError(t, push(t, setup.instance, username, password, second, "app"), "the robot pushes to the second named project")
	require.Error(t, push(t, setup.instance, username, password, other, "app"), "the robot cannot push to a project it was not given")
	require.NoError(t, pull(t, setup.instance, username, password, other, "base"), "the robot pulls from any project")
}

func TestSystemRobotInANamespaceOnlyAllowedNamespacesAdmits(t *testing.T) {
	t.Parallel()

	namespace := newNamespace(t, startOperator(t), "apps")
	setup := readyRobotProject(t, func(connection *harborv1alpha1.HarborConnection) {
		connection.Spec.AllowedNamespaces.Names = []string{namespace}
	}, nil)

	robot := newSystemRobot(t, setup, namespace)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

	_, err := setup.admin.FindSystemRobot(t.Context(), robot.Spec.Name)
	require.True(t, harbor.IsNotFound(err), "no Harbor robot is created for a namespace systemAllowedNamespaces does not admit: %v", err)

	require.Eventually(t, func() bool {
		connection := &harborv1alpha1.HarborConnection{}
		if err := setup.cl.Get(t.Context(), client.ObjectKeyFromObject(setup.connection), connection); err != nil {
			return false
		}

		connection.Spec.SystemAllowedNamespaces.Names = []string{namespace}

		return setup.cl.Update(t.Context(), connection) == nil
	}, 10*time.Second, 100*time.Millisecond)

	ready := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.NotZero(t, ready.Status.RobotID)
}

func TestSystemRobotLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("drift in Harbor is put back", func(t *testing.T) {
		t.Parallel()
		setup := readyRobotProject(t, nil, nil)
		robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
		require.NoError(t, setup.cl.Create(t.Context(), robot))
		ready := awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		id := ready.Status.RobotID
		want := harbor.FlattenGrants([]harbor.RobotGrant{
			{Kind: harbor.LevelProject, Namespace: "*", Access: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}}},
			{Kind: harbor.LevelProject, Namespace: setup.project.Spec.Name, Access: []harbor.RobotAccess{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}}},
		})

		awaitPutBack := func() {
			var found *harbor.Robot

			require.Eventually(t, func() bool {
				var err error

				found, err = setup.admin.GetRobot(t.Context(), id)

				return err == nil && found.Description == "deploys everywhere" && !found.Disabled &&
					assert.ObjectsAreEqual(want, harbor.FlattenGrants(found.Grants))
			}, projectWait, 500*time.Millisecond, "waiting for Harbor's robot to match the spec again, have %+v", found)
		}

		require.NoError(t, setup.admin.UpdateRobot(t.Context(), id, "", harbor.RobotSpec{
			Description: "changed in the UI", Duration: -1, Disabled: true,
			Grants: []harbor.RobotGrant{{Kind: harbor.LevelProject, Namespace: "*", Access: []harbor.RobotAccess{{Resource: "repository", Action: "push"}}}},
		}))
		awaitPutBack()

		// Only the scope of a permission changes here: the same resource and action, moved from one project to all.
		require.NoError(t, setup.admin.UpdateRobot(t.Context(), id, "", harbor.RobotSpec{
			Description: "deploys everywhere", Duration: -1,
			Grants: []harbor.RobotGrant{
				{Kind: harbor.LevelProject, Namespace: "*", Access: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}, {Resource: "repository", Action: "push"}}},
			},
		}))
		awaitPutBack()
	})

	t.Run("a lost Secret gets a new secret", func(t *testing.T) {
		t.Parallel()
		setup := readyRobotProject(t, nil, nil)
		robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
		require.NoError(t, setup.cl.Create(t.Context(), robot))
		awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		_, oldPassword := dockerCredentials(t, setup.cl, setup.project.Namespace, "deployer-dockerconfig", setup.instance.Registry(t))
		require.NoError(t, setup.cl.Delete(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: setup.project.Namespace, Name: "deployer-dockerconfig"}}))

		username, password := awaitNewDockerSecret(t, setup, "deployer-dockerconfig", oldPassword)
		require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the new secret works")
		require.Error(t, push(t, setup.instance, username, oldPassword, setup.project.Spec.Name, "app"), "the old secret no longer works")
	})

	t.Run("changing rotate refreshes the secret", func(t *testing.T) {
		t.Parallel()
		setup := readyRobotProject(t, nil, nil)
		robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
		require.NoError(t, setup.cl.Create(t.Context(), robot))
		key := client.ObjectKeyFromObject(robot)
		awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		_, oldPassword := dockerCredentials(t, setup.cl, setup.project.Namespace, "deployer-dockerconfig", setup.instance.Registry(t))

		require.Eventually(t, func() bool {
			current := &harborv1alpha1.HarborSystemRobot{}
			if err := setup.cl.Get(t.Context(), key, current); err != nil {
				return false
			}

			current.Spec.Secret.Rotate = "2026-11"

			return setup.cl.Update(t.Context(), current) == nil
		}, 10*time.Second, 100*time.Millisecond)

		username, password := awaitNewDockerSecret(t, setup, "deployer-dockerconfig", oldPassword)
		require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the rotated secret works")

		rotated := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		assert.Equal(t, "2026-11", rotated.Status.ObservedRotate)
		assert.NotNil(t, rotated.Status.LastRotated)
	})

	t.Run("an existing Harbor robot is adopted only when asked", func(t *testing.T) {
		t.Parallel()
		setup := readyRobotProject(t, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true }, nil)
		robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)

		existing, err := setup.admin.CreateSystemRobot(t.Context(), harbor.RobotSpec{
			Name: robot.Spec.Name, Description: "made by hand", Duration: -1,
			Grants: []harbor.RobotGrant{{Kind: harbor.LevelProject, Namespace: "*", Access: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}}}},
		})
		require.NoError(t, err)

		require.NoError(t, setup.cl.Create(t.Context(), robot))
		key := client.ObjectKeyFromObject(robot)
		awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)

		require.Eventually(t, func() bool {
			current := &harborv1alpha1.HarborSystemRobot{}
			if err := setup.cl.Get(t.Context(), key, current); err != nil {
				return false
			}

			current.Spec.Adopt = true

			return setup.cl.Update(t.Context(), current) == nil
		}, 10*time.Second, 100*time.Millisecond)

		adopted := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		assert.Equal(t, existing.ID, adopted.Status.RobotID, "the existing robot is adopted, not re-created")

		username, password := dockerCredentials(t, setup.cl, setup.project.Namespace, "deployer-dockerconfig", setup.instance.Registry(t))
		assert.NotEqual(t, existing.Secret, password, "the adopted robot's unknown secret is refreshed")
		require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"))
	})
}

// awaitNewDockerSecret waits for the robot's dockerconfigjson Secret to hold a password other than old.
func awaitNewDockerSecret(t *testing.T, setup *robotSetup, secretName, old string) (string, string) {
	t.Helper()

	var username, password string

	require.Eventually(t, func() bool {
		secret := &corev1.Secret{}
		if err := setup.cl.Get(t.Context(), client.ObjectKey{Namespace: setup.project.Namespace, Name: secretName}, secret); err != nil {
			return false
		}

		username, password = dockerCredentials(t, setup.cl, setup.project.Namespace, secretName, setup.instance.Registry(t))

		return password != old
	}, projectWait, 200*time.Millisecond, "waiting for a new secret in %s", secretName)

	return username, password
}

func TestSystemRobotDeletion(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		policy      harborv1alpha1.DeletionPolicy
		harborStays bool
	}{
		{name: "Delete removes the Harbor robot", policy: harborv1alpha1.DeletionPolicyDelete},
		{name: "DeleteWithContents removes the Harbor robot like Delete", policy: harborv1alpha1.DeletionPolicyDeleteWithContents},
		{name: "Orphan leaves the Harbor robot", policy: harborv1alpha1.DeletionPolicyOrphan, harborStays: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			setup := readyRobotProject(t, nil, nil)

			robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
			robot.Spec.DeletionPolicy = test.policy
			robot.Spec.Secret.Opaque.Enabled = ptr(true)
			require.NoError(t, setup.cl.Create(t.Context(), robot))
			key := client.ObjectKeyFromObject(robot)
			ready := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			require.NoError(t, setup.cl.Delete(t.Context(), ready))

			require.Eventually(t, func() bool {
				return apierrors.IsNotFound(setup.cl.Get(t.Context(), key, &harborv1alpha1.HarborSystemRobot{}))
			}, projectWait, 200*time.Millisecond, "waiting for the robot resource to go")

			for _, secretName := range []string{"deployer-dockerconfig", "deployer-credentials"} {
				err := setup.cl.Get(t.Context(), client.ObjectKey{Namespace: setup.project.Namespace, Name: secretName}, &corev1.Secret{})
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

func TestSystemRobotNamedAsTheOperatorsCredentialIsNeverManaged(t *testing.T) {
	t.Parallel()

	setup := readyRobotProjectWithSystemRobot(t, true, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true }, nil)

	secret := &corev1.Secret{}
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKey{Namespace: setup.project.Namespace, Name: "harbor-creds"}, secret))
	operatorName := string(secret.Data["username"])

	before, err := setup.admin.FindSystemRobot(t.Context(), operatorName[len("robot$"):])
	require.NoError(t, err)

	robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
	robot.Spec.Name = before.Name[len("robot$"):]
	robot.Spec.Adopt = true
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonOperatorCredential)

	after, err := setup.admin.GetRobot(t.Context(), before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Description, after.Description, "the operator's robot is not updated")
	assert.Equal(t, harbor.FlattenGrants(before.Grants), harbor.FlattenGrants(after.Grants), "the operator's robot keeps its permissions")

	require.NoError(t, setup.cl.Delete(t.Context(), robot))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(setup.cl.Get(t.Context(), key, &harborv1alpha1.HarborSystemRobot{}))
	}, projectWait, 200*time.Millisecond, "waiting for the robot resource to go")

	_, err = setup.admin.GetRobot(t.Context(), before.ID)
	require.NoError(t, err, "the operator's robot is never deleted")
}

func TestSystemRobotSigningInAConnectionInAnotherNamespaceIsNeverManaged(t *testing.T) {
	t.Parallel()

	// The managing connection is an admin connection that allows adoption, so only the guard stops the takeover.
	setup := readyRobotProject(t, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true }, nil)

	username, password := setup.instance.CreateSystemRobot(t, systemRobotPermissions())
	otherNamespace := newNamespace(t, setup.cl, "other-conn")
	// The username sits under a mapped key, so the guard must read the key the connection names.
	require.NoError(t, setup.cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: otherNamespace, Name: "robot-creds"},
		StringData: map[string]string{"robot-user": username, "password": password},
	}))

	other := newConnection(otherNamespace, "harbor", setup.instance.URL, "robot-creds")
	other.Spec.CredentialsSecretRef.UsernameKey = "robot-user"
	require.NoError(t, setup.cl.Create(t.Context(), other))
	awaitReady(t, setup.cl, client.ObjectKeyFromObject(other), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	before, err := setup.admin.FindSystemRobot(t.Context(), username[len("robot$"):])
	require.NoError(t, err)

	robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
	robot.Spec.Name = username[len("robot$"):]
	robot.Spec.Adopt = true
	robot.Spec.Secret.Opaque.Enabled = ptr(true)
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	refused := awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionFalse, harborv1alpha1.ReasonOperatorCredential)
	assert.Zero(t, refused.Status.RobotID, "the robot is not adopted")

	after, err := setup.admin.GetRobot(t.Context(), before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Description, after.Description, "the other namespace's robot is not updated")
	assert.Equal(t, harbor.FlattenGrants(before.Grants), harbor.FlattenGrants(after.Grants), "the other namespace's robot keeps its permissions")

	for _, secretName := range []string{"deployer-dockerconfig", "deployer-credentials"} {
		err := setup.cl.Get(t.Context(), client.ObjectKey{Namespace: setup.project.Namespace, Name: secretName}, &corev1.Secret{})
		assert.True(t, apierrors.IsNotFound(err), "no Secret %s is written: %v", secretName, err)
	}

	robotClient, err := harbor.NewClient(harbor.ConnectionConfig{URL: setup.instance.URL, Username: username, Password: password})
	require.NoError(t, err)
	require.NoError(t, robotClient.CheckCredentials(t.Context()), "the other namespace's secret is not refreshed")
}

func TestSystemRobotManagedThroughAnAdminConnectionUpdatesInPlace(t *testing.T) {
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)

	robot := newSystemRobot(t, setup, setup.project.Namespace, setup.project.Spec.Name)
	robot.Spec.Secret.Opaque.Enabled = ptr(true)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)
	ready := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	id := ready.Status.RobotID

	// A second connection in the robot's namespace signs in with the robot's own output Secret.
	signedIn := newConnection(setup.project.Namespace, "signed-in-as-robot", setup.instance.URL, "deployer-credentials")
	require.NoError(t, setup.cl.Create(t.Context(), signedIn))
	awaitReady(t, setup.cl, client.ObjectKeyFromObject(signedIn), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	_, oldPassword := dockerCredentials(t, setup.cl, setup.project.Namespace, "deployer-dockerconfig", setup.instance.Registry(t))

	require.Eventually(t, func() bool {
		current := &harborv1alpha1.HarborSystemRobot{}
		if err := setup.cl.Get(t.Context(), key, current); err != nil {
			return false
		}

		current.Spec.Description = "updated through the admin connection"
		current.Spec.Secret.Rotate = "2026-11"

		return setup.cl.Update(t.Context(), current) == nil
	}, 10*time.Second, 100*time.Millisecond)

	username, password := awaitNewDockerSecret(t, setup, "deployer-dockerconfig", oldPassword)
	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the refreshed secret works")

	updated := awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, id, updated.Status.RobotID, "the robot is updated in place, not replaced")
	assert.Equal(t, "2026-11", updated.Status.ObservedRotate)

	found, err := setup.admin.GetRobot(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "updated through the admin connection", found.Description)
}

func TestSystemRobotWaitsForAMissingProject(t *testing.T) {
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)
	missing := runUniqueName(t, "later")
	t.Cleanup(func() { setup.instance.DeleteProject(context.Background(), t, missing) })

	robot := newSystemRobot(t, setup, setup.project.Namespace, missing)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	waiting := awaitSystemRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonProjectNotFound)
	assert.Contains(t, meta.FindStatusCondition(waiting.Status.Conditions, harborv1alpha1.ConditionReady).Message, missing)

	_, err := setup.admin.FindSystemRobot(t.Context(), robot.Spec.Name)
	require.True(t, harbor.IsNotFound(err), "nothing is written while a project is missing: %v", err)

	_, err = setup.admin.CreateProject(t.Context(), missing, harbor.ProjectSettings{}, nil)
	require.NoError(t, err)

	awaitSystemRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
}

func TestConnectionDeletionWaitsForAdmittedSystemRobots(t *testing.T) {
	t.Parallel()

	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)

	connectionNamespace := newNamespace(t, cl, "system-conn")
	systemAdmitted, systemOutsider := newNamespace(t, cl, "system-admitted"), newNamespace(t, cl, "system-outsider")

	require.NoError(t, cl.Create(t.Context(), credentials(connectionNamespace, "harbor-creds", instance.Username, instance.Password)))

	connection := newConnection(connectionNamespace, "harbor", instance.URL, "harbor-creds")
	connection.Spec.AllowedNamespaces.Names = []string{systemOutsider}
	connection.Spec.SystemAllowedNamespaces.Names = []string{systemAdmitted}
	require.NoError(t, cl.Create(t.Context(), connection))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	newRobot := func(namespace string) *harborv1alpha1.HarborSystemRobot {
		return &harborv1alpha1.HarborSystemRobot{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "robot", Finalizers: []string{"test/hold"}},
			Spec: harborv1alpha1.HarborSystemRobotSpec{
				ConnectionRef:      harborv1alpha1.ObjectReference{Namespace: connectionNamespace, Name: "harbor"},
				ProjectPermissions: []harborv1alpha1.ProjectPermission{{Projects: []string{"*"}, Access: pullOnly}},
			},
		}
	}

	admitted, outsider := newRobot(systemAdmitted), newRobot(systemOutsider)
	require.NoError(t, cl.Create(t.Context(), admitted))
	require.NoError(t, cl.Create(t.Context(), outsider))

	require.NoError(t, cl.Delete(t.Context(), connection))

	held := awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	assert.Contains(t, held.Status.Conditions[0].Message, systemAdmitted+"/robot")
	assert.NotContains(t, held.Status.Conditions[0].Message, systemOutsider+"/robot",
		"system robots in namespaces that only allowedNamespaces admits are ignored")
	assert.Contains(t, held.Finalizers, controller.ConnectionFinalizer)

	// Removing the robot lets the connection go, with no other trigger.
	// The robot controller also writes to this object, so the update is retried on a conflict.
	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(admitted), admitted); err != nil {
			return false
		}

		admitted.Finalizers = nil

		return cl.Update(t.Context(), admitted) == nil
	}, 10*time.Second, 100*time.Millisecond)
	require.NoError(t, cl.Delete(t.Context(), admitted))

	require.Eventually(t, func() bool {
		var current harborv1alpha1.HarborConnection
		return client.IgnoreNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current)) == nil && current.Name == ""
	}, 60*time.Second, 200*time.Millisecond, "connection should be gone")
}

func TestSystemRobotWithProjectReadUpdateDeleteCanReadAndUpdateAProject(t *testing.T) {
	t.Parallel()

	setup := readyRobotProject(t, nil, nil)

	robot := newSystemRobot(t, setup, setup.project.Namespace)
	robot.Spec.ProjectPermissions = []harborv1alpha1.ProjectPermission{{
		Projects: []string{setup.project.Spec.Name},
		Access: []harborv1alpha1.SystemRobotProjectAccess{
			{Resource: "project", Action: "read"},
			{Resource: "project", Action: "update"},
			{Resource: "project", Action: "delete"},
		},
	}}
	require.NoError(t, setup.cl.Create(t.Context(), robot))

	awaitSystemRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	username, password := dockerCredentials(t, setup.cl, setup.project.Namespace, "deployer-dockerconfig", setup.instance.Registry(t))
	asRobot, err := harbor.NewClient(harbor.ConnectionConfig{URL: setup.instance.URL, Username: username, Password: password})
	require.NoError(t, err)

	_, err = asRobot.GetProjectByName(t.Context(), setup.project.Spec.Name)
	require.NoError(t, err, "the robot reads the project")

	public := true
	require.NoError(t, asRobot.UpdateProjectMetadata(t.Context(), setup.project.Spec.Name, harbor.ProjectSettings{Public: &public}), "the robot updates the project")
}
