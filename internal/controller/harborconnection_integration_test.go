//go:build integration

package controller_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/go-logr/logr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// startOperator returns a client for the operator that TestMain started once for the whole package. The operator runs
// every controller. Tests must keep to their own namespaces (see newNamespace) and never assert on global counts.
// The withProjects argument is accepted for older call sites and ignored: every controller always runs.
func startOperator(t *testing.T, _ ...bool) client.Client {
	t.Helper()

	if sharedClient == nil {
		t.Skipf("envtest is not available: %v", sharedStartErr)
	}

	return sharedClient
}

// newNamespace creates a namespace with a random name starting with prefix and returns the name. The suffix is random,
// not derived from the test name, so repeated runs (-count) on the same API server never collide.
func newNamespace(t *testing.T, cl client.Client, prefix string) string {
	t.Helper()

	name := strings.ToLower(prefix) + "-" + randomHex(t, 5)
	require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	// Cleanups run last-in first-out, so this runs before cleanups registered earlier that delete Harbor objects.
	t.Cleanup(func() { abandonNamespace(cl, name) })

	return name
}

// abandonNamespace removes every operator resource in the namespace without running their finalizers, so the shared
// operator stops reconciling them and cannot recreate the Harbor objects a test cleanup is about to delete.
// Envtest has no namespace controller, so deleting the Namespace itself would not do this.
func abandonNamespace(cl client.Client, namespace string) {
	ctx := context.Background()
	lists := []client.ObjectList{
		&harborv1alpha1.HarborImmutabilityPolicyList{}, &harborv1alpha1.HarborRetentionPolicyList{},
		&harborv1alpha1.HarborProjectRobotList{}, &harborv1alpha1.HarborSystemRobotList{},
		&harborv1alpha1.HarborProjectList{}, &harborv1alpha1.HarborRegistryList{}, &harborv1alpha1.HarborConnectionList{},
	}

	for _, list := range lists {
		if cl.List(ctx, list, client.InNamespace(namespace)) != nil {
			continue
		}

		items, err := meta.ExtractList(list)
		if err != nil {
			continue
		}

		for _, item := range items {
			object, ok := item.(client.Object)
			if !ok {
				continue
			}

			_ = cl.Patch(ctx, object, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`)))
			_ = cl.Delete(ctx, object)
		}
	}
}

// randomHex returns 2*length random hex characters.
func randomHex(t *testing.T, length int) string {
	t.Helper()

	raw := make([]byte, length)
	_, err := rand.Read(raw)
	require.NoError(t, err)

	return hex.EncodeToString(raw)
}

// operatorOptions change how startIsolatedOperator runs the operator.
type operatorOptions struct {
	// logger receives the operator's log; the zero logger keeps the default.
	logger logr.Logger
	// wrapRobotClient, when set, wraps the robot controller's client so a test can change what the controller reads.
	wrapRobotClient func(client.Client) client.Client
	// watchNamespaces limits the cache and the reconcilers to these namespaces, as the --watch-namespaces flag does.
	watchNamespaces access.WatchedNamespaces
}

// startIsolatedOperator starts a private API server and operator for one test. It is the documented exception for
// tests that need operator-wide settings the shared operator does not have: a different logger, a wrapped client, or
// a watch list.
func startIsolatedOperator(t *testing.T, options operatorOptions) client.Client {
	t.Helper()

	environment, restConfig, err := startEnvironment()
	if err != nil {
		t.Skipf("envtest is not available: %v", err)
	}

	t.Cleanup(func() { _ = environment.Stop() })

	stop, direct, err := runOperator(restConfig, options)
	require.NoError(t, err)
	t.Cleanup(stop)

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
		Spec:       harborv1alpha1.HarborConnectionSpec{URL: url, CredentialsSecretRef: harborv1alpha1.CredentialsSecretReference{Name: secret}},
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

// unknownUser is a Harbor user that does not exist. Failed sign-ins for a real account such as the admin lock it out,
// and the shared operator keeps retrying a bad credential for as long as the connection exists, which would lock the
// admin out for every other test.
const unknownUser = "no-such-user"

func TestConnectionBecomesReadyWithValidCredentials(t *testing.T) {
	t.Parallel()
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)
	namespace := newNamespace(t, cl, "conn")

	require.NoError(t, cl.Create(t.Context(), credentials(namespace, "harbor-creds", instance.Username, instance.Password)))
	connection := newConnection(namespace, "harbor", instance.URL, "harbor-creds")
	require.NoError(t, cl.Create(t.Context(), connection))

	ready := awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	assert.NotEmpty(t, ready.Status.HarborVersion)
	assert.Equal(t, harbor.NormalizeURL(instance.URL), ready.Status.HarborIdentity)
	assert.Equal(t, harbor.HostOf(ready.Status.HarborIdentity), ready.Status.RegistryHost)
	assert.Equal(t, ready.Generation, ready.Status.ObservedGeneration)
}

func TestConnectionMapsCredentialKeys(t *testing.T) {
	t.Parallel()
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)
	namespace := newNamespace(t, cl, "conn")

	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "admin-secret"},
		StringData: map[string]string{"HARBOR_ADMIN_PASSWORD": instance.Password},
	}))

	connection := newConnection(namespace, "mapped", instance.URL, "admin-secret")
	connection.Spec.CredentialsSecretRef.Username = instance.Username
	connection.Spec.CredentialsSecretRef.PasswordKey = "HARBOR_ADMIN_PASSWORD"
	require.NoError(t, cl.Create(t.Context(), connection))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	missing := newConnection(namespace, "missing-key", instance.URL, "admin-secret")
	missing.Spec.CredentialsSecretRef.Username = instance.Username
	missing.Spec.CredentialsSecretRef.PasswordKey = "NO_SUCH_KEY"
	require.NoError(t, cl.Create(t.Context(), missing))
	awaitReady(t, cl, client.ObjectKeyFromObject(missing), metav1.ConditionFalse, harborv1alpha1.ReasonCredentialsInvalid)

	var current harborv1alpha1.HarborConnection
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(missing), &current))
	condition := meta.FindStatusCondition(current.Status.Conditions, harborv1alpha1.ConditionReady)
	require.NotNil(t, condition)
	assert.Contains(t, condition.Message, "NO_SUCH_KEY")

	both := newConnection(namespace, "both", instance.URL, "admin-secret")
	both.Spec.CredentialsSecretRef.Username = instance.Username
	both.Spec.CredentialsSecretRef.UsernameKey = "USER"
	require.Error(t, cl.Create(t.Context(), both), "username with usernameKey is refused")
}

func TestConnectionReportsBadCredentials(t *testing.T) {
	t.Parallel()
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)
	namespace := newNamespace(t, cl, "conn")

	require.NoError(t, cl.Create(t.Context(), credentials(namespace, "wrong-password", unknownUser, "not-the-password")))
	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "no-password"}, StringData: map[string]string{"username": "admin"}}))

	for name, test := range map[string]struct {
		secret string
		reason string
	}{
		"wrong password": {secret: "wrong-password", reason: harborv1alpha1.ReasonUnauthorized},
		"missing key":    {secret: "no-password", reason: harborv1alpha1.ReasonCredentialsInvalid},
		"missing Secret": {secret: "absent", reason: harborv1alpha1.ReasonCredentialsInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			connection := newConnection(namespace, "conn-"+test.secret, instance.URL, test.secret)
			require.NoError(t, cl.Create(t.Context(), connection))
			awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, test.reason)
		})
	}
}

func TestConnectionRevalidatesWhenCredentialsSecretChanges(t *testing.T) {
	t.Parallel()
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)
	namespace := newNamespace(t, cl, "conn")

	secret := credentials(namespace, "harbor-creds", unknownUser, "not-the-password")
	require.NoError(t, cl.Create(t.Context(), secret))

	connection := newConnection(namespace, "harbor", instance.URL, "harbor-creds")
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
	t.Parallel()
	instance := harborfixture.FromEnv(t)
	cl := startOperator(t)
	namespace := newNamespace(t, cl, "conn")

	admittedNamespace, outsiderNamespace := newNamespace(t, cl, "admitted"), newNamespace(t, cl, "outsider")

	require.NoError(t, cl.Create(t.Context(), credentials(namespace, "harbor-creds", instance.Username, instance.Password)))

	connection := newConnection(namespace, "harbor", instance.URL, "harbor-creds")
	connection.Spec.AllowedNamespaces.Names = []string{admittedNamespace}
	require.NoError(t, cl.Create(t.Context(), connection))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	newProject := func(projectNamespace string) *harborv1alpha1.HarborProject {
		return &harborv1alpha1.HarborProject{
			ObjectMeta: metav1.ObjectMeta{Namespace: projectNamespace, Name: "project", Finalizers: []string{"test/hold"}},
			Spec:       harborv1alpha1.HarborProjectSpec{ConnectionRef: harborv1alpha1.ObjectReference{Namespace: namespace, Name: "harbor"}},
		}
	}

	admitted, outsider := newProject(admittedNamespace), newProject(outsiderNamespace)
	require.NoError(t, cl.Create(t.Context(), admitted))
	require.NoError(t, cl.Create(t.Context(), outsider))

	require.NoError(t, cl.Delete(t.Context(), connection))

	held := awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	assert.Contains(t, held.Status.Conditions[0].Message, admittedNamespace+"/project")
	assert.NotContains(t, held.Status.Conditions[0].Message, outsiderNamespace+"/project", "references from namespaces the connection does not admit are ignored")
	assert.Contains(t, held.Finalizers, controller.ConnectionFinalizer)

	// Removing the project lets the connection go, with no other trigger.
	// A patch carries no resourceVersion, so it cannot conflict with the controller writing status.
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(admitted), admitted))
	original := admitted.DeepCopy()
	admitted.Finalizers = nil
	require.NoError(t, cl.Patch(t.Context(), admitted, client.MergeFrom(original)))
	require.NoError(t, cl.Delete(t.Context(), admitted))

	require.Eventually(t, func() bool {
		var current harborv1alpha1.HarborConnection
		return client.IgnoreNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current)) == nil && current.Name == ""
	}, 60*time.Second, 200*time.Millisecond, "connection should be gone")
}
