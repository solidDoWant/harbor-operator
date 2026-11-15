//go:build e2e

package e2e_test

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// The release that test/e2e/up.sh installs.
const (
	operatorRelease        = "harbor-operator"
	operatorNamespace      = "harbor-operator-system"
	operatorServiceAccount = "system:serviceaccount:" + operatorNamespace + ":harbor-operator"
)

func helm(t *testing.T, args ...string) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "helm", append(args,
		"--kube-context", os.Getenv("E2E_KUBE_CONTEXT"), "--namespace", operatorNamespace, "--wait", "--timeout", "5m")...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func operatorMay(t *testing.T, cl client.Client, verb, group, resource, namespace string) bool {
	t.Helper()

	review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:               operatorServiceAccount,
		ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: verb, Group: group, Resource: resource},
	}}
	require.NoError(t, cl.Create(t.Context(), review))

	return review.Status.Allowed
}

func newProject(namespace, connectionNamespace, name string) *harborv1alpha1.HarborProject {
	return &harborv1alpha1.HarborProject{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "project"},
		Spec: harborv1alpha1.HarborProjectSpec{
			ConnectionRef: harborv1alpha1.ObjectReference{Namespace: connectionNamespace, Name: "harbor"},
			Name:          name,
		},
	}
}

// Changes the shared operator release, so it must not run in parallel with other e2e tests.
func TestOperatorWithWatchNamespacesIgnoresAndCannotReadOtherNamespaces(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := kubeClient(t)
	internalURL := os.Getenv("HARBOR_INTERNAL_URL")
	require.NotEmpty(t, internalURL, "HARBOR_INTERNAL_URL is set by test/e2e/up.sh")

	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	ctx := t.Context()
	watched := harborfixture.UniqueName(t, "e2e-watched")
	unwatched := harborfixture.UniqueName(t, "e2e-unwatched")

	for _, namespace := range []string{watched, unwatched} {
		require.NoError(t, cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	}

	// Registered first so it runs last: the operator goes back to watching every namespace only after the
	// resources it never saw are gone, so it can't act on them.
	t.Cleanup(func() { helm(t, "rollback", operatorRelease) })
	t.Cleanup(func() {
		_ = cl.DeleteAllOf(t.Context(), &harborv1alpha1.HarborProject{}, client.InNamespace(unwatched))
		_ = cl.DeleteAllOf(t.Context(), &harborv1alpha1.HarborProject{}, client.InNamespace(watched))
		_ = admin.DeleteProject(t.Context(), watched)
		_ = admin.DeleteProject(t.Context(), unwatched)

		assert.Eventually(t, func() bool {
			list := &harborv1alpha1.HarborProjectList{}
			if err := cl.List(t.Context(), list); err != nil {
				return false
			}

			for _, project := range list.Items {
				if project.Namespace == watched || project.Namespace == unwatched {
					return false
				}
			}

			return true
		}, 2*time.Minute, time.Second, "waiting for the test's projects to be deleted")

		for _, namespace := range []string{watched, unwatched} {
			_ = cl.Delete(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
		}
	})

	helm(t, "upgrade", operatorRelease, "../../deploy/charts/harbor-operator", "--reuse-values", "--set", "watchNamespaces={"+watched+"}")

	// The operator's service account may read Secrets and custom resources only in the watched namespace.
	for _, resource := range []struct{ group, resource string }{{"", "secrets"}, {harborv1alpha1.GroupVersion.Group, "harborprojects"}} {
		for _, verb := range []string{"get", "list"} {
			assert.True(t, operatorMay(t, cl, verb, resource.group, resource.resource, watched), "%s %s in the watched namespace", verb, resource.resource)
			assert.False(t, operatorMay(t, cl, verb, resource.group, resource.resource, unwatched), "%s %s in another namespace", verb, resource.resource)
			assert.False(t, operatorMay(t, cl, verb, resource.group, resource.resource, ""), "%s %s cluster-wide", verb, resource.resource)
		}
	}

	assert.True(t, operatorMay(t, cl, "list", "", "namespaces", ""), "reading Namespaces stays cluster-wide")

	require.NoError(t, cl.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: watched, Name: "harbor-creds"},
		StringData: map[string]string{"username": instance.Username, "password": instance.Password},
	}))
	require.NoError(t, cl.Create(ctx, &harborv1alpha1.HarborConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: watched, Name: "harbor"},
		Spec: harborv1alpha1.HarborConnectionSpec{
			URL:                  internalURL,
			CredentialsSecretRef: harborv1alpha1.LocalSecretReference{Name: "harbor-creds"},
			AllowedNamespaces:    harborv1alpha1.AllowedNamespaces{Names: []string{unwatched}},
		},
	}))

	// The unwatched project is created first and the watched one after it, so once the watched one is Ready the
	// operator has had every chance to act on the other.
	ignored := newProject(unwatched, watched, unwatched)
	require.NoError(t, cl.Create(ctx, ignored))

	project := newProject(watched, watched, watched)
	require.NoError(t, cl.Create(ctx, project))

	ready := &harborv1alpha1.HarborProject{}
	require.Eventually(t, func() bool {
		if err := cl.Get(ctx, client.ObjectKeyFromObject(project), ready); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(ready.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == metav1.ConditionTrue
	}, 3*time.Minute, time.Second, "waiting for the project in the watched namespace to be Ready, have %+v", ready.Status.Conditions)

	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(ignored), ignored))
	assert.Empty(t, ignored.Status.Conditions, "the project in the unwatched namespace has no status")
	assert.Empty(t, ignored.Finalizers, "the operator never touched the project in the unwatched namespace")

	_, err = admin.GetProjectByName(ctx, unwatched)
	assert.True(t, harbor.IsNotFound(err), "Harbor has no project for the unwatched namespace, got %v", err)
}
