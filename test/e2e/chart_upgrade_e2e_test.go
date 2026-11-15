//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

const (
	operatorNamespace = "harbor-operator-system"
	previousChart     = "oci://ghcr.io/soliddowant/charts/harbor-operator"
)

// helm runs the helm binary against the e2e cluster.
func helm(t *testing.T, args ...string) {
	t.Helper()

	full := append([]string{"--kube-context", os.Getenv("E2E_KUBE_CONTEXT"), "--namespace", operatorNamespace}, args...)
	command := exec.CommandContext(context.WithoutCancel(t.Context()), "helm", full...)

	var output bytes.Buffer

	command.Stdout, command.Stderr = &output, &output
	require.NoErrorf(t, command.Run(), "helm %s:\n%s", strings.Join(args, " "), output.String())
}

func installLocalChart(t *testing.T) {
	t.Helper()

	helm(t, "upgrade", "--install", "harbor-operator", "../../deploy/charts/harbor-operator", "--create-namespace",
		"--set", "image.tag=e2e", "--set", "image.pullPolicy=Never", "--wait", "--timeout", "5m")
}

func applyManifest(t *testing.T, cl client.Client, manifest string) []*unstructured.Unstructured {
	t.Helper()

	var applied []*unstructured.Unstructured

	for _, document := range strings.Split(manifest, "\n---\n") {
		object := &unstructured.Unstructured{}
		require.NoError(t, yaml.Unmarshal([]byte(document), &object.Object))
		require.NoError(t, cl.Create(t.Context(), object))
		applied = append(applied, object)
	}

	return applied
}

// harborState is everything the operator manages in Harbor for the test's resources.
type harborState struct {
	Project      *harbor.Project
	Quota        int64
	Registry     *harbor.Registry
	ProjectRobot *harbor.Robot
	SystemRobot  *harbor.Robot
	Retention    *harbor.RetentionPolicy
	Immutability []harbor.ImmutabilityRule
	RobotSecrets map[string]map[string][]byte
}

func readHarborState(t *testing.T, cl client.Client, admin *harbor.Client, namespace, name string) harborState {
	t.Helper()

	ctx := t.Context()
	state := harborState{RobotSecrets: map[string]map[string][]byte{}}

	var err error

	state.Project, err = admin.GetProjectByName(ctx, name)
	require.NoError(t, err)

	state.Quota, err = admin.GetProjectStorageQuota(ctx, state.Project.ID)
	require.NoError(t, err)

	state.Registry, err = admin.GetRegistryByName(ctx, name)
	require.NoError(t, err)

	state.ProjectRobot, err = admin.FindProjectRobot(ctx, state.Project.ID, name)
	require.NoError(t, err)

	state.SystemRobot, err = admin.FindSystemRobot(ctx, name)
	require.NoError(t, err)

	retention := &harborv1alpha1.HarborRetentionPolicy{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "retention"}, retention))

	state.Retention, err = admin.GetRetention(ctx, retention.Status.RetentionID)
	require.NoError(t, err)

	state.Immutability, err = admin.ListImmutabilityRules(ctx, state.Project.ID)
	require.NoError(t, err)

	for _, secretName := range []string{"robot-dockerconfig", "system-robot-dockerconfig"} {
		secret := &corev1.Secret{}
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err == nil {
			state.RobotSecrets[secretName] = secret.Data
		}
	}

	return state
}

func waitAllReady(t *testing.T, cl client.Client, objects []*unstructured.Unstructured) {
	t.Helper()

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		for _, object := range objects {
			current := &unstructured.Unstructured{}
			current.SetGroupVersionKind(object.GroupVersionKind())
			require.NoError(collect, cl.Get(t.Context(), client.ObjectKeyFromObject(object), current))

			conditions, _, _ := unstructured.NestedSlice(current.Object, "status", "conditions")
			ready := false

			for _, raw := range conditions {
				condition, _ := raw.(map[string]any)
				if condition["type"] == harborv1alpha1.ConditionReady && condition["status"] == string(metav1.ConditionTrue) {
					generation, _, _ := unstructured.NestedInt64(condition, "observedGeneration")
					ready = generation == current.GetGeneration()
				}
			}

			assert.Truef(collect, ready, "%s %s is not Ready", current.GetKind(), current.GetName())
		}
	}, 5*time.Minute, 2*time.Second)
}

// Installing the previous release, creating one of every kind and upgrading to the local chart must leave every
// resource Ready and change nothing in Harbor.
func TestUpgradeFromThePreviousReleaseChangesNothingInHarbor(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := kubeClient(t)
	internalURL := os.Getenv("HARBOR_INTERNAL_URL")
	require.NotEmpty(t, internalURL, "HARBOR_INTERNAL_URL is set by test/e2e/up.sh")

	version, err := os.ReadFile("../upgrade/previous-version")
	require.NoError(t, err)

	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	name := harborfixture.UniqueName(t, "upg")
	namespace := name
	ctx := t.Context()

	// Whatever happens, leave the local chart installed for the other tests.
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborProjectRobot{}, client.InNamespace(namespace))
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborSystemRobot{}, client.InNamespace(namespace))
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborRetentionPolicy{}, client.InNamespace(namespace))
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborImmutabilityPolicy{}, client.InNamespace(namespace))
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborProject{}, client.InNamespace(namespace))
		_ = cl.DeleteAllOf(cleanup, &harborv1alpha1.HarborRegistry{}, client.InNamespace(namespace))
		_ = admin.DeleteProject(cleanup, name)
		_ = cl.Delete(cleanup, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})

		installLocalChart(t)
	})

	// Replacing the installed release in place keeps the CRDs, so nothing waits on their deletion.
	helm(t, "upgrade", "--install", "harbor-operator", previousChart, "--version", strings.TrimSpace(string(version)),
		"--create-namespace", "--wait", "--timeout", "5m")

	require.NoError(t, cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	require.NoError(t, cl.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "harbor-creds"},
		StringData: map[string]string{"username": instance.Username, "password": instance.Password},
	}))

	manifest := strings.NewReplacer("@NAME@", name, "@NAMESPACE@", namespace, "@HARBORURL@", internalURL).Replace(upgradeManifest)
	objects := applyManifest(t, cl, manifest)
	waitAllReady(t, cl, objects)

	before := readHarborState(t, cl, admin, namespace, name)

	installLocalChart(t)

	waitAllReady(t, cl, objects)

	after := readHarborState(t, cl, admin, namespace, name)
	assert.Equal(t, before, after, "the upgrade must not change Harbor objects or the robot Secrets")

	// A drift correction emits an Updated event.
	var events eventsv1.EventList
	require.NoError(t, cl.List(ctx, &events, client.InNamespace(namespace)))

	for _, event := range events.Items {
		assert.NotEqualf(t, harborv1alpha1.EventReasonUpdated, event.Reason, "unexpected drift correction: %s", event.Note)
	}

	var current harborv1alpha1.HarborProject
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "project"}, &current))
	assert.NotNil(t, meta.FindStatusCondition(current.Status.Conditions, harborv1alpha1.ConditionReady))
}

const upgradeManifest = `apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborConnection
metadata: {name: harbor, namespace: @NAMESPACE@}
spec:
  url: @HARBORURL@
  credentialsSecretRef: {name: harbor-creds}
  systemAllowedNamespaces: {names: [@NAMESPACE@]}
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborRegistry
metadata: {name: registry, namespace: @NAMESPACE@}
spec:
  connectionRef: {name: harbor}
  name: @NAME@
  type: docker-registry
  url: @HARBORURL@
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborProject
metadata: {name: project, namespace: @NAMESPACE@}
spec:
  connectionRef: {name: harbor}
  name: @NAME@
  public: true
  storageQuota: 5Gi
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborProjectRobot
metadata: {name: robot, namespace: @NAMESPACE@}
spec:
  projectRef: {name: project}
  name: @NAME@
  permissions:
    - {resource: repository, action: push}
    - {resource: repository, action: pull}
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborRetentionPolicy
metadata: {name: retention, namespace: @NAMESPACE@}
spec:
  projectRef: {name: project}
  rules:
    - template: latestPushedK
      count: 5
      repositories: {decoration: repoMatches, pattern: "**"}
      tags: {decoration: matches, pattern: "v*"}
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborImmutabilityPolicy
metadata: {name: immutability, namespace: @NAMESPACE@}
spec:
  projectRef: {name: project}
  rules:
    - repositories: {decoration: repoMatches, pattern: "**"}
      tags: {decoration: matches, pattern: "release-*"}
---
apiVersion: harbor.soliddowant.dev/v1alpha1
kind: HarborSystemRobot
metadata: {name: system-robot, namespace: @NAMESPACE@}
spec:
  connectionRef: {name: harbor}
  name: @NAME@
  systemPermissions:
    - {resource: project, action: list}
`
