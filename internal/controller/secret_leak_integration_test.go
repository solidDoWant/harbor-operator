//go:build integration

package controller_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/leakscan"
)

// scannedKinds returns fresh lists on every call because a list is filled in place and tests run in parallel.
func scannedKinds() []client.ObjectList {
	return []client.ObjectList{&harborv1alpha1.HarborConnectionList{}, &harborv1alpha1.HarborProjectList{}, &harborv1alpha1.HarborProjectRobotList{}}
}

func TestNoSecretAppearsInStatusEventsOrLogsDuringTheRobotLifecycle(t *testing.T) {
	t.Parallel()

	log := &leakscan.Log{}
	setup := readyRobotProjectLogging(t, log.Logger(), false, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true }, nil)
	registry := setup.instance.Registry(t)
	secrets := map[string]string{"connection password": setup.instance.Password}

	// Create, then lose the Secret, then rotate.
	robot := ciRobot(setup.project.Namespace, setup.project)
	robot.Spec.Secret.Rotate = "first"
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)
	awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	_, created := dockerCredentials(t, setup.cl, setup.project.Namespace, "ci-dockerconfig", registry)
	secrets["created robot secret"] = created

	require.NoError(t, setup.cl.Delete(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: setup.project.Namespace, Name: "ci-dockerconfig"}}))
	_, replaced := awaitNewDockerPassword(t, setup, "ci-dockerconfig", created)
	secrets["replacement robot secret"] = replaced

	updateRobot(t, setup.cl, key, func(robot *harborv1alpha1.HarborProjectRobot) { robot.Spec.Secret.Rotate = "second" })
	_, rotated := awaitNewDockerPassword(t, setup, "ci-dockerconfig", replaced)
	secrets["rotated robot secret"] = rotated

	// Adopt a robot made by hand; its original secret must not leak either.
	existing, err := setup.admin.CreateRobot(t.Context(), setup.project.Spec.Name, harbor.RobotSpec{
		Name: "legacy", Description: "made by hand", Duration: -1, Permissions: []harbor.RobotAccess{{Resource: "repository", Action: "pull"}},
	})
	require.NoError(t, err)

	secrets["adopted robot's original secret"] = existing.Secret

	adopted := ciRobot(setup.project.Namespace, setup.project)
	adopted.Name = "legacy"
	adopted.Spec.Adopt = true
	adopted.Spec.Secret.DockerConfigJSON.Name = "legacy-dockerconfig"
	adopted.Spec.Secret.Opaque.Name = "legacy-credentials"
	require.NoError(t, setup.cl.Create(t.Context(), adopted))
	ready := awaitRobot(t, setup.cl, client.ObjectKeyFromObject(adopted), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	awaitEvent(t, setup.cl, ready, harborv1alpha1.EventReasonAdopted)

	_, refreshed := dockerCredentials(t, setup.cl, setup.project.Namespace, "legacy-dockerconfig", registry)
	secrets["adopted robot's refreshed secret"] = refreshed

	for label, value := range secrets {
		require.NotEmpty(t, value, label)
	}

	found, err := leakscan.Scan(t.Context(), setup.cl, log, scannedKinds(), secrets)
	require.NoError(t, err)
	assert.Empty(t, found, "secrets leaked outside Kubernetes Secrets")
	assert.NotEmpty(t, log.String(), "the operator log was captured")
}

func TestScanFailsWhenTheOperatorLogsARobotSecret(t *testing.T) {
	t.Parallel()

	log := &leakscan.Log{}
	setup := readyRobotProjectLogging(t, log.Logger(), false, nil, nil)

	robot := ciRobot(setup.project.Namespace, setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	awaitRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	_, password := dockerCredentials(t, setup.cl, setup.project.Namespace, "ci-dockerconfig", setup.instance.Registry(t))
	log.Logger().Info("deliberate leak", "password", password)

	found, err := leakscan.Scan(t.Context(), setup.cl, log, scannedKinds(), map[string]string{"robot secret": password})
	require.NoError(t, err)
	assert.Equal(t, []string{"operator log contains robot secret"}, found)
}
