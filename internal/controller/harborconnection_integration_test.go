//go:build integration

package controller_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// startOperator runs an API server and the connection controller in-process and returns a client for it. withProjects
// also runs the project controller.
func startOperator(t *testing.T, withProjects ...bool) client.Client {
	t.Helper()

	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}

	var (
		restConfig *rest.Config
		err        error
	)

	restConfig, err = environment.Start()
	if err != nil {
		t.Skipf("envtest is not available: %v", err)
	}

	t.Cleanup(func() { _ = environment.Stop() })

	runtimeScheme, err := scheme.New()
	require.NoError(t, err)

	manager, err := ctrl.NewManager(restConfig, ctrl.Options{Scheme: runtimeScheme, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: ptr(true)}})
	require.NoError(t, err)
	require.NoError(t, controller.IndexFields(t.Context(), manager.GetFieldIndexer()))
	clients := harbor.NewClientCache(manager.GetClient())
	require.NoError(t, (&controller.HarborConnectionReconciler{Client: manager.GetClient(), Clients: clients}).SetupWithManager(manager))

	if len(withProjects) > 0 && withProjects[0] {
		require.NoError(t, ownership.SetupIndexes(t.Context(), manager.GetFieldIndexer()))
		require.NoError(t, (&controller.HarborProjectReconciler{Client: manager.GetClient(), Clients: clients, Recorder: manager.GetEventRecorder("harbor-operator")}).SetupWithManager(manager))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = manager.Start(ctx)
	}()

	t.Cleanup(func() { cancel(); <-done })

	direct, err := client.New(restConfig, client.Options{Scheme: runtimeScheme})
	require.NoError(t, err)

	return direct
}

func ptr[T any](value T) *T { return &value }

func credentials(namespace, name, username, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		StringData: map[string]string{"username": username, "password": password},
	}
}

func newConnection(namespace, name, url, secret string) *harborv1alpha1.HarborConnection {
	return &harborv1alpha1.HarborConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       harborv1alpha1.HarborConnectionSpec{URL: url, CredentialsSecretRef: harborv1alpha1.LocalSecretReference{Name: secret}},
	}
}

func readyReason(connection *harborv1alpha1.HarborConnection) (metav1.ConditionStatus, string) {
	condition := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionReady)
	if condition == nil {
		return "", ""
	}

	return condition.Status, condition.Reason
}

func awaitReady(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborConnection {
	t.Helper()

	var connection harborv1alpha1.HarborConnection

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), key, &connection); err != nil {
			return false
		}

		gotStatus, gotReason := readyReason(&connection)

		return gotStatus == status && gotReason == reason
	}, 60*time.Second, 200*time.Millisecond, "waiting for Ready=%s %s, have %+v", status, reason, connection.Status.Conditions)

	return &connection
}

func TestConnectionBecomesReadyWithValidCredentials(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)

	require.NoError(t, cl.Create(t.Context(), credentials("default", "harbor-creds", instance.Username, instance.Password)))
	connection := newConnection("default", "harbor", instance.URL, "harbor-creds")
	require.NoError(t, cl.Create(t.Context(), connection))

	ready := awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	assert.NotEmpty(t, ready.Status.HarborVersion)
	assert.Equal(t, harbor.NormalizeURL(instance.URL), ready.Status.HarborIdentity)
	assert.Equal(t, harbor.HostOf(ready.Status.HarborIdentity), ready.Status.RegistryHost)
	assert.Equal(t, ready.Generation, ready.Status.ObservedGeneration)
}

func TestConnectionReportsBadCredentials(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)

	require.NoError(t, cl.Create(t.Context(), credentials("default", "wrong-password", instance.Username, "not-the-password")))
	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "no-password"}, StringData: map[string]string{"username": "admin"}}))

	for name, test := range map[string]struct {
		secret string
		reason string
	}{
		"wrong password": {secret: "wrong-password", reason: harborv1alpha1.ReasonUnauthorized},
		"missing key":    {secret: "no-password", reason: harborv1alpha1.ReasonCredentialsInvalid},
		"missing Secret": {secret: "absent", reason: harborv1alpha1.ReasonCredentialsInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			connection := newConnection("default", "conn-"+test.secret, instance.URL, test.secret)
			require.NoError(t, cl.Create(t.Context(), connection))
			awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, test.reason)
		})
	}
}

func TestConnectionRevalidatesWhenCredentialsSecretChanges(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)

	secret := credentials("default", "harbor-creds", instance.Username, "not-the-password")
	require.NoError(t, cl.Create(t.Context(), secret))

	connection := newConnection("default", "harbor", instance.URL, "harbor-creds")
	require.NoError(t, cl.Create(t.Context(), connection))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonUnauthorized)

	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
	secret.Data = nil
	secret.StringData = map[string]string{"username": instance.Username, "password": instance.Password}
	require.NoError(t, cl.Update(t.Context(), secret))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	// Breaking the Secret again is noticed too.
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
	delete(secret.Data, "password")
	require.NoError(t, cl.Update(t.Context(), secret))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonCredentialsInvalid)
}

func TestConnectionDeletionWaitsForAdmittedProjects(t *testing.T) {
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)

	for _, name := range []string{"admitted", "outsider"} {
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	}

	require.NoError(t, cl.Create(t.Context(), credentials("default", "harbor-creds", instance.Username, instance.Password)))

	connection := newConnection("default", "harbor", instance.URL, "harbor-creds")
	connection.Spec.AllowedNamespaces.Names = []string{"admitted"}
	require.NoError(t, cl.Create(t.Context(), connection))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	newProject := func(namespace string) *harborv1alpha1.HarborProject {
		return &harborv1alpha1.HarborProject{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "project", Finalizers: []string{"test/hold"}},
			Spec:       harborv1alpha1.HarborProjectSpec{ConnectionRef: harborv1alpha1.ObjectReference{Namespace: "default", Name: "harbor"}},
		}
	}

	admitted, outsider := newProject("admitted"), newProject("outsider")
	require.NoError(t, cl.Create(t.Context(), admitted))
	require.NoError(t, cl.Create(t.Context(), outsider))

	require.NoError(t, cl.Delete(t.Context(), connection))

	held := awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	assert.Contains(t, held.Status.Conditions[0].Message, "admitted/project")
	assert.NotContains(t, held.Status.Conditions[0].Message, "outsider/project", "references from namespaces the connection does not admit are ignored")
	assert.Contains(t, held.Finalizers, controller.ConnectionFinalizer)

	// Removing the project lets the connection go, with no other trigger.
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(admitted), admitted))
	admitted.Finalizers = nil
	require.NoError(t, cl.Update(t.Context(), admitted))
	require.NoError(t, cl.Delete(t.Context(), admitted))

	require.Eventually(t, func() bool {
		var current harborv1alpha1.HarborConnection
		return client.IgnoreNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current)) == nil && current.Name == ""
	}, 60*time.Second, 200*time.Millisecond, "connection should be gone")
}
