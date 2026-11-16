//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

const (
	rootRobotSecretName = "harbor-operator-credentials"
	rootWait            = 3 * time.Minute
)

// rootChain is the admin connection, the operator's own robot and the connection that uses the robot's Secret.
type rootChain struct {
	cl        client.Client
	admin     *harbor.Client
	namespace string
	robotName string
	robotKey  client.ObjectKey
	instance  *harborfixture.Harbor
	url       string
}

// rootRobotGrants is what the operator's own robot holds, built from the permissions the reconcilers need.
func rootRobotGrants() ([]harborv1alpha1.SystemRobotAccess, []harborv1alpha1.SystemRobotProjectAccess) {
	var (
		system   []harborv1alpha1.SystemRobotAccess
		projects []harborv1alpha1.SystemRobotProjectAccess
	)

	seen := map[harbor.RequiredPermission]bool{}

	for _, required := range harbor.RequiredPermissions {
		// Robot credentials replace robots instead of updating them.
		if required.UserCredentialsOnly {
			continue
		}

		key := harbor.RequiredPermission{Scope: required.Scope, Resource: required.Resource, Action: required.Action}
		if seen[key] {
			continue
		}

		seen[key] = true

		if required.Scope == "system" {
			system = append(system, harborv1alpha1.SystemRobotAccess{Resource: required.Resource, Action: required.Action})
		} else {
			projects = append(projects, harborv1alpha1.SystemRobotProjectAccess{Resource: required.Resource, Action: required.Action})
		}
	}

	return system, projects
}

func rootRobotSpec(connectionName, namespace string, secretName string) harborv1alpha1.HarborSystemRobotSpec {
	system, projects := rootRobotGrants()

	return harborv1alpha1.HarborSystemRobotSpec{
		ConnectionRef:      harborv1alpha1.ObjectReference{Namespace: namespace, Name: connectionName},
		Description:        "the operator's own robot",
		SystemPermissions:  system,
		ProjectPermissions: []harborv1alpha1.ProjectPermission{{Projects: []string{"*"}, Access: projects}},
		ResyncInterval:     &metav1.Duration{Duration: 10 * time.Second},
		Secret: harborv1alpha1.RobotSecretSpec{
			DockerConfigJSON: harborv1alpha1.SecretOutput{Enabled: ptr(false)},
			Opaque:           harborv1alpha1.SecretOutput{Enabled: ptr(true), Name: secretName},
		},
	}
}

func adminConnection(namespace, internalURL string, allowAdoption bool) *harborv1alpha1.HarborConnection {
	return &harborv1alpha1.HarborConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "harbor-admin"},
		Spec: harborv1alpha1.HarborConnectionSpec{
			URL:                  internalURL,
			CredentialsSecretRef: harborv1alpha1.CredentialsSecretReference{Name: "harbor-admin-credentials"},
			AllowAdoption:        allowAdoption,
		},
	}
}

func mainConnection(namespace, internalURL, secretName string) *harborv1alpha1.HarborConnection {
	return &harborv1alpha1.HarborConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "harbor"},
		Spec: harborv1alpha1.HarborConnectionSpec{
			URL:                  internalURL,
			CredentialsSecretRef: harborv1alpha1.CredentialsSecretReference{Name: secretName},
		},
	}
}

func newRootNamespace(t *testing.T, cl client.Client, admin *harbor.Client, prefix string, projectNames ...*string) string {
	t.Helper()

	namespace := harborfixture.UniqueName(t, prefix)
	require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))

	t.Cleanup(func() {
		// t.Context() is already canceled here.
		cleanup := context.WithoutCancel(t.Context())

		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborProject{}, client.InNamespace(namespace))
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborSystemRobot{}, client.InNamespace(namespace))
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborConnection{}, client.InNamespace(namespace))
		_ = cl.Delete(cleanup, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})

		for _, name := range projectNames {
			_ = admin.DeleteProject(cleanup, *name)
		}
	})

	return namespace
}

func awaitReady(t *testing.T, cl client.Client, object client.Object, conditions *[]metav1.Condition) {
	t.Helper()

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(object), object); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(*conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == metav1.ConditionTrue
	}, rootWait, time.Second, "waiting for %T %s to be Ready, have %+v", object, object.GetName(), *conditions)
}

func awaitConnectionReady(t *testing.T, cl client.Client, namespace, name string) {
	t.Helper()

	connection := &harborv1alpha1.HarborConnection{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	awaitReady(t, cl, connection, &connection.Status.Conditions)
}

func awaitRobotReady(t *testing.T, cl client.Client, namespace, name string) *harborv1alpha1.HarborSystemRobot {
	t.Helper()

	robot := &harborv1alpha1.HarborSystemRobot{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	awaitReady(t, cl, robot, &robot.Status.Conditions)

	return robot
}

// awaitProjectThroughMainConnection creates a project through the connection "harbor" and waits for it to be Ready.
func awaitProjectThroughMainConnection(t *testing.T, cl client.Client, namespace, resourceName, harborName string) {
	t.Helper()

	project := &harborv1alpha1.HarborProject{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: resourceName},
		Spec: harborv1alpha1.HarborProjectSpec{
			ConnectionRef: harborv1alpha1.ObjectReference{Namespace: namespace, Name: "harbor"},
			Name:          harborName,
		},
	}
	require.NoError(t, cl.Create(t.Context(), project))
	awaitReady(t, cl, project, &project.Status.Conditions)
}

func opaqueSecret(t *testing.T, cl client.Client, namespace, name string) *corev1.Secret {
	t.Helper()

	secret := &corev1.Secret{}

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.NoError(c, cl.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: name}, secret))
		require.NotEmpty(c, secret.Data["password"])
	}, rootWait, time.Second, "waiting for Secret %s", name)

	return secret
}

func e2eHarbor(t *testing.T) (*harborfixture.Harbor, *harbor.Client, string) {
	t.Helper()

	instance := harborfixture.FromEnv(t)
	internalURL := os.Getenv("HARBOR_INTERNAL_URL")
	require.NotEmpty(t, internalURL, "HARBOR_INTERNAL_URL is set by test/e2e/up.sh")

	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	return instance, admin, internalURL
}

func adminCredentialsSecret(namespace string, instance *harborfixture.Harbor) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "harbor-admin-credentials"},
		StringData: map[string]string{"username": instance.Username, "password": instance.Password},
	}
}

// applyRootChain applies the admin connection, the operator's robot and the connection using the robot's Secret
// at once, in the order that has the dependent connection first.
func applyRootChain(t *testing.T, prefix string) *rootChain {
	t.Helper()

	instance, admin, internalURL := e2eHarbor(t)
	cl := kubeClient(t)
	ctx := t.Context()

	var projectName, robotName string

	namespace := newRootNamespace(t, cl, admin, prefix, &projectName)
	robotName = harborfixture.UniqueName(t, "root")
	projectName = namespace

	t.Cleanup(func() {
		// The robot is deleted with the namespace; this covers a failed run.
		cleanup := context.WithoutCancel(ctx)
		if found, err := admin.FindSystemRobot(cleanup, robotName); err == nil {
			_ = admin.DeleteRobot(cleanup, found.ID)
		}
	})

	robot := &harborv1alpha1.HarborSystemRobot{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: robotName},
		Spec:       rootRobotSpec("harbor-admin", namespace, rootRobotSecretName),
	}

	require.NoError(t, cl.Create(ctx, mainConnection(namespace, internalURL, rootRobotSecretName)))
	require.NoError(t, cl.Create(ctx, robot))
	require.NoError(t, cl.Create(ctx, adminConnection(namespace, internalURL, false)))
	require.NoError(t, cl.Create(ctx, adminCredentialsSecret(namespace, instance)))

	chain := &rootChain{
		cl: cl, admin: admin, namespace: namespace, robotName: robotName, instance: instance, url: internalURL,
		robotKey: client.ObjectKeyFromObject(robot),
	}

	awaitConnectionReady(t, cl, namespace, "harbor-admin")
	awaitRobotReady(t, cl, namespace, robotName)
	awaitConnectionReady(t, cl, namespace, "harbor")

	return chain
}

func TestRootRobotChainAppliedToAnEmptyNamespaceBecomesReadyWithoutRestart(t *testing.T) {
	chain := applyRootChain(t, "root")

	awaitProjectThroughMainConnection(t, chain.cl, chain.namespace, "project", chain.namespace)

	found, err := chain.admin.GetProjectByName(t.Context(), chain.namespace)
	require.NoError(t, err)
	assert.Equal(t, chain.namespace, found.Name)
}

func TestRootRobotRotationKeepsTheRobotAndTheConnectionKeepsWorking(t *testing.T) {
	chain := applyRootChain(t, "rot")
	ctx := t.Context()

	before := awaitRobotReady(t, chain.cl, chain.namespace, chain.robotName)
	oldSecret := opaqueSecret(t, chain.cl, chain.namespace, rootRobotSecretName)

	awaitProjectThroughMainConnection(t, chain.cl, chain.namespace, "project", chain.namespace)

	require.NoError(t, chain.cl.Get(ctx, chain.robotKey, before))

	rotated := before.DeepCopy()
	rotated.Spec.Secret.Rotate = "second"
	require.NoError(t, chain.cl.Update(ctx, rotated))

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		current := &corev1.Secret{}
		require.NoError(c, chain.cl.Get(ctx, client.ObjectKey{Namespace: chain.namespace, Name: rootRobotSecretName}, current))
		assert.NotEqual(c, string(oldSecret.Data["password"]), string(current.Data["password"]))
	}, rootWait, time.Second, "waiting for the Secret to hold a new password")

	after := awaitRobotReady(t, chain.cl, chain.namespace, chain.robotName)
	assert.Equal(t, before.Status.RobotID, after.Status.RobotID, "rotation keeps the Harbor robot")

	awaitConnectionReady(t, chain.cl, chain.namespace, "harbor")

	// A second project proves the connection signs in with the new secret.
	second := harborfixture.UniqueName(t, "rot2")

	t.Cleanup(func() { _ = chain.admin.DeleteProject(context.WithoutCancel(ctx), second) })

	awaitProjectThroughMainConnection(t, chain.cl, chain.namespace, "project-two", second)
}

func TestRootRobotDriftInHarborIsPutBackWithTheSameRobot(t *testing.T) {
	chain := applyRootChain(t, "drift")
	ctx := t.Context()

	robot := awaitRobotReady(t, chain.cl, chain.namespace, chain.robotName)
	id := robot.Status.RobotID

	before, err := chain.admin.GetRobot(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, before.Grants)

	// Change the description and drop all but one system permission, as someone using Harbor's API could.
	require.NoError(t, chain.admin.UpdateRobot(ctx, id, "", harbor.RobotSpec{
		Name:        chain.robotName,
		Description: "changed in Harbor",
		Duration:    before.Duration,
		Grants: []harbor.RobotGrant{{
			Kind: harbor.LevelSystem, Namespace: "/", Access: []harbor.RobotAccess{{Resource: "project", Action: "list"}},
		}},
	}))

	changed, err := chain.admin.GetRobot(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "changed in Harbor", changed.Description)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		current, err := chain.admin.GetRobot(ctx, id)
		require.NoError(c, err)
		assert.Equal(c, "the operator's own robot", current.Description)
		assert.Equal(c, harbor.FlattenGrants(before.Grants), harbor.FlattenGrants(current.Grants))
	}, rootWait, time.Second, "waiting for Harbor's robot to match the spec again")

	found, err := chain.admin.FindSystemRobot(ctx, chain.robotName)
	require.NoError(t, err)
	assert.Equal(t, id, found.ID, "the robot was updated in place")
}

func TestHandMadeSystemRobotAdoptedWithANewSecretBecomesTheConnectionsCredential(t *testing.T) {
	instance, admin, internalURL := e2eHarbor(t)
	cl := kubeClient(t)
	ctx := t.Context()

	var projectName string

	namespace := newRootNamespace(t, cl, admin, "mig", &projectName)
	projectName = namespace
	handMade := harborfixture.UniqueName(t, "hand")

	system, projects := rootRobotGrants()

	var grants []harbor.RobotGrant

	grant := harbor.RobotGrant{Kind: harbor.LevelSystem, Namespace: "/"}
	for _, access := range system {
		grant.Access = append(grant.Access, harbor.RobotAccess{Resource: access.Resource, Action: access.Action})
	}

	projectGrant := harbor.RobotGrant{Kind: harbor.LevelProject, Namespace: "*"}
	for _, access := range projects {
		projectGrant.Access = append(projectGrant.Access, harbor.RobotAccess{Resource: access.Resource, Action: access.Action})
	}

	grants = append(grants, grant, projectGrant)

	created, err := admin.CreateSystemRobot(ctx, harbor.RobotSpec{Name: handMade, Description: "made by hand", Duration: -1, Grants: grants})
	require.NoError(t, err)

	t.Cleanup(func() { _ = admin.DeleteRobot(context.WithoutCancel(ctx), created.ID) })

	require.NoError(t, cl.Create(ctx, adminCredentialsSecret(namespace, instance)))
	require.NoError(t, cl.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "hand-made-credentials"},
		StringData: map[string]string{"username": created.Name, "password": created.Secret},
	}))
	require.NoError(t, cl.Create(ctx, adminConnection(namespace, internalURL, true)))
	require.NoError(t, cl.Create(ctx, mainConnection(namespace, internalURL, "hand-made-credentials")))
	awaitConnectionReady(t, cl, namespace, "harbor-admin")
	awaitConnectionReady(t, cl, namespace, "harbor")

	// The migration: adopt the robot into a new Secret and point the main connection at it in the same change.
	spec := rootRobotSpec("harbor-admin", namespace, rootRobotSecretName)
	spec.Name = handMade
	spec.Description = "made by hand"
	spec.Adopt = true

	require.NoError(t, cl.Create(ctx, &harborv1alpha1.HarborSystemRobot{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "operator"}, Spec: spec}))

	connection := &harborv1alpha1.HarborConnection{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "harbor"}, connection))
	connection.Spec.CredentialsSecretRef.Name = rootRobotSecretName
	require.NoError(t, cl.Update(ctx, connection))

	robot := awaitRobotReady(t, cl, namespace, "operator")
	assert.Equal(t, created.ID, robot.Status.RobotID, "the hand-made robot was adopted")

	secret := opaqueSecret(t, cl, namespace, rootRobotSecretName)
	assert.NotEqual(t, created.Secret, string(secret.Data["password"]), "adoption refreshed the secret")
	assert.Equal(t, created.Name, string(secret.Data["username"]))

	awaitConnectionReady(t, cl, namespace, "harbor")
	awaitProjectThroughMainConnection(t, cl, namespace, "project", namespace)
}
