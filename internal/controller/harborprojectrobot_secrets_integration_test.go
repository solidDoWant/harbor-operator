//go:build integration

package controller_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// updateRobot applies change to the stored robot, retrying on conflicts with the operator's writes.
func updateRobot(t *testing.T, cl client.Client, key client.ObjectKey, change func(*harborv1alpha1.HarborProjectRobot)) {
	t.Helper()

	require.Eventually(t, func() bool {
		robot := &harborv1alpha1.HarborProjectRobot{}
		if err := cl.Get(t.Context(), key, robot); err != nil {
			return false
		}

		change(robot)

		return cl.Update(t.Context(), robot) == nil
	}, 10*time.Second, 100*time.Millisecond)
}

// secretGone reports whether the Secret does not exist.
func secretGone(t *testing.T, cl client.Client, namespace, name string) bool {
	t.Helper()

	return apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &corev1.Secret{}))
}

// awaitNewDockerPassword waits until the robot's dockerconfigjson Secret holds a password other than old and returns
// the credentials.
func awaitNewDockerPassword(t *testing.T, setup *robotSetup, secretName, old string) (string, string) {
	t.Helper()

	var username, password string

	require.Eventually(t, func() bool {
		secret := &corev1.Secret{}
		if err := setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: secretName}, secret); err != nil {
			return false
		}

		username, password = dockerCredentials(t, setup.cl, "default", secretName, setup.instance.Registry(t))

		return password != "" && password != old
	}, projectWait, 200*time.Millisecond, "waiting for Secret %s to hold a new robot secret", secretName)

	return username, password
}

func TestRobotSecretOptionsWriteExactlyTheRequestedSecrets(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	robot := ciRobot("default", setup.project)
	robot.Spec.Secret.DockerConfigJSON.Enabled = ptr(false)
	robot.Spec.Secret.Opaque = harborv1alpha1.SecretOutput{Enabled: ptr(true), Name: "ci-login"}
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	opaque := &corev1.Secret{}
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "ci-login"}, opaque))
	assert.Equal(t, corev1.SecretTypeOpaque, opaque.Type)
	assert.Equal(t, ready.Status.HarborName, string(opaque.Data["username"]))
	assert.Equal(t, setup.instance.Registry(t), string(opaque.Data["registry"]))
	assert.Len(t, opaque.Data, 3, "the opaque Secret has exactly the keys username, password and registry")

	password := string(opaque.Data["password"])
	require.NoError(t, push(t, setup.instance, ready.Status.HarborName, password, setup.project.Spec.Name, "app"), "the opaque Secret's credentials work")

	for _, name := range []string{"ci-dockerconfig", "ci-credentials"} {
		assert.True(t, secretGone(t, setup.cl, "default", name), "no Secret %s is written for a disabled or renamed type", name)
	}

	updateRobot(t, setup.cl, key, func(robot *harborv1alpha1.HarborProjectRobot) {
		robot.Spec.Secret.DockerConfigJSON = harborv1alpha1.SecretOutput{Enabled: ptr(true), Name: "ci-pull"}
		robot.Spec.Secret.Opaque.Enabled = ptr(false)
	})

	require.Eventually(t, func() bool {
		return secretGone(t, setup.cl, "default", "ci-login") && !secretGone(t, setup.cl, "default", "ci-pull")
	}, projectWait, 200*time.Millisecond, "waiting for the disabled opaque Secret to go and the dockerconfigjson Secret to be written")

	username, dockerPassword := dockerCredentials(t, setup.cl, "default", "ci-pull", setup.instance.Registry(t))
	assert.Equal(t, ready.Status.HarborName, username)
	assert.Equal(t, password, dockerPassword, "switching Secret types copies the credentials without a refresh")
	assert.True(t, secretGone(t, setup.cl, "default", "ci-dockerconfig"), "only the requested name is written")
}

func TestRobotSecretDeletedGetsANewRobotSecret(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	awaitRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	username, old := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
	require.NoError(t, setup.cl.Delete(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ci-dockerconfig"}}))

	newUsername, password := awaitNewDockerPassword(t, setup, "ci-dockerconfig", old)
	assert.Equal(t, username, newUsername)
	require.NoError(t, push(t, setup.instance, newUsername, password, setup.project.Spec.Name, "app"), "the new secret works")
	require.Error(t, push(t, setup.instance, username, old, setup.project.Spec.Name, "app"), "the lost secret no longer works")
}

// laggingSecretList is a client whose Secret lists still show a Secret after it was deleted, until a Secret of that
// name exists again. It reproduces a reconcile that listed the robot's Secrets just before one was deleted.
type laggingSecretList struct {
	client.Client

	mutex sync.Mutex
	stale *corev1.Secret
}

// lag makes lists show secret once it is gone.
func (lagging *laggingSecretList) lag(secret *corev1.Secret) {
	lagging.mutex.Lock()
	defer lagging.mutex.Unlock()

	lagging.stale = secret.DeepCopy()
}

func (lagging *laggingSecretList) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	if err := lagging.Client.List(ctx, list, options...); err != nil {
		return err
	}

	secretList, ok := list.(*corev1.SecretList)
	if !ok {
		return nil
	}

	lagging.mutex.Lock()
	defer lagging.mutex.Unlock()

	if lagging.stale == nil {
		return nil
	}

	for _, secret := range secretList.Items {
		if secret.Namespace == lagging.stale.Namespace && secret.Name == lagging.stale.Name {
			if secret.UID != lagging.stale.UID {
				lagging.stale = nil
			}

			return nil
		}
	}

	secretList.Items = append(secretList.Items, *lagging.stale.DeepCopy())

	return nil
}

func TestRobotSecretDeletedDuringAReconcileGetsANewRobotSecret(t *testing.T) {
	lagging := &laggingSecretList{}
	setup := readyRobotProjectWith(t, operatorOptions{wrapRobotClient: func(cached client.Client) client.Client {
		lagging.Client = cached

		return lagging
	}}, false, nil, nil)

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	awaitRobot(t, setup.cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	username, old := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))

	secret := &corev1.Secret{}
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "ci-dockerconfig"}, secret))
	lagging.lag(secret)
	require.NoError(t, setup.cl.Delete(t.Context(), secret))

	newUsername, password := awaitNewDockerPassword(t, setup, "ci-dockerconfig", old)
	assert.Equal(t, username, newUsername)
	require.NoError(t, push(t, setup.instance, newUsername, password, setup.project.Spec.Name, "app"), "the new secret works")
	require.Error(t, push(t, setup.instance, username, old, setup.project.Spec.Name, "app"), "the lost secret no longer works")
}

func TestRobotRotateGivesTheRobotANewSecret(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	robot := ciRobot("default", setup.project)
	robot.Spec.Secret.Rotate = "first"
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	created := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, "first", created.Status.ObservedRotate, "the rotate value is recorded on create")

	username, old := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))

	updateRobot(t, setup.cl, key, func(robot *harborv1alpha1.HarborProjectRobot) { robot.Spec.Secret.Rotate = "second" })

	_, password := awaitNewDockerPassword(t, setup, "ci-dockerconfig", old)

	rotated := &harborv1alpha1.HarborProjectRobot{}

	require.Eventually(t, func() bool {
		return setup.cl.Get(t.Context(), key, rotated) == nil && rotated.Status.ObservedRotate == "second"
	}, projectWait, 200*time.Millisecond, "waiting for status.observedRotate")
	require.NotNil(t, rotated.Status.LastRotated, "status.lastRotated is set")

	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "the rotated secret works")
	require.Error(t, push(t, setup.instance, username, old, setup.project.Spec.Name, "app"), "the old secret no longer works")
}

func TestRobotSecretNameTakenByAForeignSecret(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ci-dockerconfig"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"token": []byte("not the robot's")},
	}
	require.NoError(t, setup.cl.Create(t.Context(), foreign))

	robot := ciRobot("default", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)

	awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonSecretConflict)

	// The robot resyncs every two seconds; across several rounds the foreign Secret must stay as it was.
	// The condition gets its own object: assert.Never can return while a last check still runs in its goroutine.
	assert.Never(t, func() bool {
		current := &corev1.Secret{}

		return setup.cl.Get(t.Context(), client.ObjectKeyFromObject(foreign), current) != nil || current.ResourceVersion != foreign.ResourceVersion
	}, 3*robot.Spec.ResyncInterval.Duration, 200*time.Millisecond, "the foreign Secret is not written or deleted")

	have := &corev1.Secret{}
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKeyFromObject(foreign), have))
	assert.Equal(t, foreign.Data, have.Data)
	assert.Empty(t, have.OwnerReferences)

	awaitRobot(t, setup.cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonSecretConflict)

	require.NoError(t, setup.cl.Delete(t.Context(), have))
	awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	username, password := dockerCredentials(t, setup.cl, "default", "ci-dockerconfig", setup.instance.Registry(t))
	require.NoError(t, push(t, setup.instance, username, password, setup.project.Spec.Name, "app"), "once the name is free the robot's Secret is written")
}

func TestRobotRejectsTwoEnabledSecretsWithTheSameName(t *testing.T) {
	setup := readyRobotProject(t, nil, nil)

	tests := []struct {
		name   string
		change func(*harborv1alpha1.RobotSecretSpec)
		assert require.ErrorAssertionFunc
	}{
		{name: "same explicit name", change: func(spec *harborv1alpha1.RobotSecretSpec) {
			spec.DockerConfigJSON.Name = "same"
			spec.Opaque = harborv1alpha1.SecretOutput{Enabled: ptr(true), Name: "same"}
		}, assert: require.Error},
		{name: "opaque name equals the dockerconfigjson default", change: func(spec *harborv1alpha1.RobotSecretSpec) {
			spec.Opaque = harborv1alpha1.SecretOutput{Enabled: ptr(true), Name: "ci-dockerconfig"}
		}, assert: require.Error},
		{name: "dockerconfigjson name equals the opaque default", change: func(spec *harborv1alpha1.RobotSecretSpec) {
			spec.DockerConfigJSON.Name = "ci-credentials"
			spec.Opaque.Enabled = ptr(true)
		}, assert: require.Error},
		{name: "same name but opaque disabled", change: func(spec *harborv1alpha1.RobotSecretSpec) {
			spec.DockerConfigJSON.Name = "same"
			spec.Opaque = harborv1alpha1.SecretOutput{Name: "same"}
		}, assert: require.NoError},
		{name: "same name but dockerconfigjson disabled", change: func(spec *harborv1alpha1.RobotSecretSpec) {
			spec.DockerConfigJSON = harborv1alpha1.SecretOutput{Enabled: ptr(false), Name: "same"}
			spec.Opaque = harborv1alpha1.SecretOutput{Enabled: ptr(true), Name: "same"}
		}, assert: require.NoError},
		{name: "both enabled with different names", change: func(spec *harborv1alpha1.RobotSecretSpec) {
			spec.Opaque.Enabled = ptr(true)
		}, assert: require.NoError},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			robot := ciRobot("default", setup.project)
			// Rejected specs are never stored, so they can share a name; accepted ones are reconciled and need their own.
			if index >= 3 {
				robot.Name = fmt.Sprintf("ci-accepted-%d", index)
			}

			test.change(&robot.Spec.Secret)
			err := setup.cl.Create(t.Context(), robot)
			test.assert(t, err)

			if err != nil {
				assert.Contains(t, err.Error(), "must have different names")
			}
		})
	}
}
