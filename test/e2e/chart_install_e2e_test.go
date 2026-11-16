//go:build e2e

// Package e2e runs the operator from its Helm chart in a kind cluster against a real Harbor.
package e2e_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

func ptr[T any](value T) *T { return &value }

func kubeClient(t *testing.T) client.Client {
	t.Helper()

	cl, _ := kubeClients(t)

	return cl
}

func kubeClients(t *testing.T) (client.Client, *kubernetes.Clientset) {
	t.Helper()

	kubeContext := os.Getenv("E2E_KUBE_CONTEXT")
	if kubeContext == "" {
		t.Skip("E2E_KUBE_CONTEXT is not set: run `make test-e2e` (needs Docker)")
	}

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	require.NoError(t, err)

	runtimeScheme, err := scheme.New()
	require.NoError(t, err)

	cl, err := client.New(restConfig, client.Options{Scheme: runtimeScheme})
	require.NoError(t, err)

	clientset, err := kubernetes.NewForConfig(restConfig)
	require.NoError(t, err)

	return cl, clientset
}

func TestProjectIsCreatedByTheOperatorInstalledFromTheChart(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := kubeClient(t)
	internalURL := os.Getenv("HARBOR_INTERNAL_URL")
	require.NotEmpty(t, internalURL, "HARBOR_INTERNAL_URL is set by test/e2e/up.sh")

	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	name := harborfixture.UniqueName(t, "e2e")
	namespace := name
	ctx := t.Context()

	require.NoError(t, cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	t.Cleanup(func() {
		// Delete the resources first so the operator removes the Harbor project, then the namespace.
		_ = cl.DeleteAllOf(t.Context(), &harborv1alpha1.HarborProject{}, client.InNamespace(namespace))
		_ = admin.DeleteProject(t.Context(), name)
		_ = cl.Delete(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	require.NoError(t, cl.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "harbor-creds"},
		StringData: map[string]string{"username": instance.Username, "password": instance.Password},
	}))

	connection := &harborv1alpha1.HarborConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "harbor"},
		Spec:       harborv1alpha1.HarborConnectionSpec{URL: internalURL, CredentialsSecretRef: harborv1alpha1.CredentialsSecretReference{Name: "harbor-creds"}},
	}
	require.NoError(t, cl.Create(ctx, connection))

	expires := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	project := &harborv1alpha1.HarborProject{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "project"},
		Spec: harborv1alpha1.HarborProjectSpec{
			ConnectionRef: harborv1alpha1.ObjectReference{Namespace: namespace, Name: "harbor"},
			Name:          name,
			Public:        ptr(true),
			StorageQuota:  ptr(resource.MustParse("5Gi")),
			Vulnerability: &harborv1alpha1.VulnerabilitySpec{
				PreventVulnerableImages: ptr(true),
				Severity:                "high",
				ScanOnPush:              ptr(true),
				CVEAllowlist:            &harborv1alpha1.CVEAllowlist{Items: []string{"CVE-2024-0001", "CVE-2024-0002"}, ExpiresAt: &metav1.Time{Time: expires}},
			},
		},
	}
	require.NoError(t, cl.Create(ctx, project))

	ready := &harborv1alpha1.HarborProject{}

	require.Eventually(t, func() bool {
		if err := cl.Get(ctx, client.ObjectKeyFromObject(project), ready); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(ready.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == ready.Generation
	}, 3*time.Minute, time.Second, "waiting for the project to be Ready, have %+v", ready.Status.Conditions)

	found, err := admin.GetProjectByName(ctx, name)
	require.NoError(t, err)

	quota, err := admin.GetProjectStorageQuota(ctx, found.ID)
	require.NoError(t, err)

	assert.Equal(t, int32(found.ID), ready.Status.ProjectID) //nolint:gosec // test IDs are small
	assert.Equal(t, int64(5<<30), quota)
	assert.Equal(t, harbor.ProjectSettings{
		Public:                  ptr(true),
		PreventVulnerableImages: ptr(true),
		Severity:                ptr("high"),
		ScanOnPush:              ptr(true),
		UseSystemAllowlist:      ptr(false),
		CVEAllowlist:            &harbor.CVEAllowlist{Items: []string{"CVE-2024-0001", "CVE-2024-0002"}, ExpiresAt: &expires},
	}, found.Settings)
}
