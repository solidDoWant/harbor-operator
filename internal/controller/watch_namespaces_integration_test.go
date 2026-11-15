//go:build integration

package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

const notWatchedWait = 30 * time.Second

// countingHarbor stands in for Harbor and counts every request it gets. It answers 503, so a request that slips
// through also fails the resource instead of making it look healthy.
func countingHarbor(t *testing.T) (string, *atomic.Int64) {
	t.Helper()

	requests := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	return server.URL, requests
}

// awaitReadyCondition waits until the object's Ready condition has the given reason and message.
func awaitReadyCondition(t *testing.T, cl client.Client, object client.Object, conditions func() []metav1.Condition, reason, message string) {
	t.Helper()

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		require.NoError(collect, cl.Get(t.Context(), client.ObjectKeyFromObject(object), object))

		ready := meta.FindStatusCondition(conditions(), harborv1alpha1.ConditionReady)
		require.NotNil(collect, ready, "no Ready condition yet")
		assert.Equal(collect, metav1.ConditionFalse, ready.Status)
		assert.Equal(collect, reason, ready.Reason)
		assert.Equal(collect, message, ready.Message)
	}, notWatchedWait, 100*time.Millisecond, "waiting for Ready=False %s", reason)
}

// TestReferenceIntoAnUnwatchedNamespaceIsNotAllowed runs the operator with a watch list and points every kind of
// reference at a namespace outside it. The referenced objects exist there and admit the watched namespace, so only the
// watch list keeps them out of reach.
func TestReferenceIntoAnUnwatchedNamespaceIsNotAllowed(t *testing.T) {
	t.Parallel()

	const watched, unwatched = "watched", "unwatched"

	cl := startIsolatedOperator(t, operatorOptions{watchNamespaces: access.WatchedNamespaces{watched}})
	harborURL, requests := countingHarbor(t)

	for _, namespace := range []string{watched, unwatched} {
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	}

	admitWatched := harborv1alpha1.AllowedNamespaces{Names: []string{watched}}

	require.NoError(t, cl.Create(t.Context(), credentials(unwatched, "harbor-creds", "admin", "unused")))
	connection := newConnection(unwatched, "harbor", harborURL, "harbor-creds")
	connection.Spec.AllowedNamespaces = admitWatched
	connection.Spec.SystemAllowedNamespaces = admitWatched
	require.NoError(t, cl.Create(t.Context(), connection))

	project := newProject(unwatched, "unwatched-project", connection)
	project.Spec.AllowedNamespaces = admitWatched
	require.NoError(t, cl.Create(t.Context(), project))

	projectKey := client.ObjectKeyFromObject(project)
	message := access.NotWatchedMessage(unwatched)
	resync := &metav1.Duration{Duration: 2 * time.Second}

	tests := []struct {
		name string
		// object is the resource in the watched namespace with its reference into the unwatched one.
		object client.Object
		// conditions returns the object's status conditions.
		conditions func(client.Object) []metav1.Condition
		// claim records a Harbor ID with deletionPolicy Delete, so deletion takes the path that would delete in Harbor.
		claim func(client.Object)
	}{
		{
			name: "project connectionRef",
			object: func() client.Object {
				referrer := newProject(watched, "watched-project", connection)
				referrer.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete

				return referrer
			}(),
			conditions: func(object client.Object) []metav1.Condition {
				return object.(*harborv1alpha1.HarborProject).Status.Conditions
			},
			claim: func(object client.Object) { object.(*harborv1alpha1.HarborProject).Status.ProjectID = 7 },
		},
		{
			name: "registry connectionRef",
			object: &harborv1alpha1.HarborRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: watched, Name: "registry"},
				Spec: harborv1alpha1.HarborRegistrySpec{
					ConnectionRef:  harborv1alpha1.ObjectReference{Namespace: unwatched, Name: connection.Name},
					Name:           "watched-registry",
					Type:           "docker-registry",
					URL:            "https://registry.example.com",
					DeletionPolicy: harborv1alpha1.DeletionPolicyDelete,
					ResyncInterval: resync,
				},
			},
			conditions: func(object client.Object) []metav1.Condition {
				return object.(*harborv1alpha1.HarborRegistry).Status.Conditions
			},
			claim: func(object client.Object) { object.(*harborv1alpha1.HarborRegistry).Status.RegistryID = 7 },
		},
		{
			name: "system robot connectionRef",
			object: &harborv1alpha1.HarborSystemRobot{
				ObjectMeta: metav1.ObjectMeta{Namespace: watched, Name: "system-robot"},
				Spec: harborv1alpha1.HarborSystemRobotSpec{
					ConnectionRef:      harborv1alpha1.ObjectReference{Namespace: unwatched, Name: connection.Name},
					Name:               "watched-system-robot",
					Duration:           -1,
					ProjectPermissions: []harborv1alpha1.ProjectPermission{{Projects: []string{"*"}, Access: pullOnly}},
					DeletionPolicy:     harborv1alpha1.DeletionPolicyDelete,
					ResyncInterval:     resync,
				},
			},
			conditions: func(object client.Object) []metav1.Condition {
				return object.(*harborv1alpha1.HarborSystemRobot).Status.Conditions
			},
			claim: func(object client.Object) { object.(*harborv1alpha1.HarborSystemRobot).Status.RobotID = 7 },
		},
		{
			name: "project robot projectRef",
			object: func() client.Object {
				robot := newRobot(watched, "robot", projectKey)
				robot.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
				robot.Spec.ResyncInterval = resync

				return robot
			}(),
			conditions: func(object client.Object) []metav1.Condition {
				return object.(*harborv1alpha1.HarborProjectRobot).Status.Conditions
			},
			claim: func(object client.Object) { object.(*harborv1alpha1.HarborProjectRobot).Status.RobotID = 7 },
		},
		{
			name: "retention policy projectRef",
			object: func() client.Object {
				policy := keepVersionsAndRecentPulls(watched, "retention", project)
				policy.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete

				return policy
			}(),
			conditions: func(object client.Object) []metav1.Condition {
				return object.(*harborv1alpha1.HarborRetentionPolicy).Status.Conditions
			},
			claim: func(object client.Object) {
				policy := object.(*harborv1alpha1.HarborRetentionPolicy)
				policy.Status.ProjectID = 7
				policy.Status.RetentionID = 7
			},
		},
		{
			name: "immutability policy projectRef",
			object: func() client.Object {
				policy := immutabilityPolicy(watched, project, immutabilityRule("v*", false))
				policy.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete

				return policy
			}(),
			conditions: func(object client.Object) []metav1.Condition {
				return object.(*harborv1alpha1.HarborImmutabilityPolicy).Status.Conditions
			},
			claim: func(object client.Object) { object.(*harborv1alpha1.HarborImmutabilityPolicy).Status.ProjectID = 7 },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := test.object
			conditions := func() []metav1.Condition { return test.conditions(object) }

			require.NoError(t, cl.Create(t.Context(), object))
			awaitReadyCondition(t, cl, object, conditions, harborv1alpha1.ReasonNotAllowed, message)

			// A resource that claimed a Harbor object and asks for it to be deleted is still only orphaned.
			require.Eventually(t, func() bool {
				if cl.Get(t.Context(), client.ObjectKeyFromObject(object), object) != nil {
					return false
				}

				test.claim(object)

				return cl.Status().Update(t.Context(), object) == nil
			}, notWatchedWait, 100*time.Millisecond, "recording a Harbor ID in the status")

			require.NoError(t, cl.Delete(t.Context(), object))
			require.Eventually(t, func() bool {
				return apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(object), object))
			}, notWatchedWait, 100*time.Millisecond, "waiting for the resource to be released")
		})
	}

	assert.Zero(t, requests.Load(), "no request reaches Harbor for a reference into an unwatched namespace")
}

// TestProxyCacheProjectWithARegistryInAnUnwatchedNamespaceIsNotAllowed checks the registryRef of a proxy-cache project.
// The project's connection is in the watched namespace and ready, so only the registry reference stops it.
func TestProxyCacheProjectWithARegistryInAnUnwatchedNamespaceIsNotAllowed(t *testing.T) {
	t.Parallel()

	const watched, unwatched = "watched", "unwatched"

	instance := harborfixture.FromEnv(t)
	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	cl := startIsolatedOperator(t, operatorOptions{watchNamespaces: access.WatchedNamespaces{watched}})

	for _, namespace := range []string{watched, unwatched} {
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	}

	connection := readyConnection(t, cl, instance, watched, nil)

	harborName := harborfixture.UniqueName(t, "op")
	t.Cleanup(func() { _ = admin.DeleteProject(context.Background(), harborName) })

	project := newProject(watched, harborName, connection)
	project.Spec.ProxyCache = &harborv1alpha1.ProxyCacheSpec{
		RegistryRef: harborv1alpha1.ObjectReference{Namespace: unwatched, Name: "registry"},
	}
	require.NoError(t, cl.Create(t.Context(), project))

	awaitReadyCondition(t, cl, project, func() []metav1.Condition { return project.Status.Conditions },
		harborv1alpha1.ReasonNotAllowed, access.NotWatchedMessage(unwatched))

	_, err = admin.GetProjectByName(t.Context(), harborName)
	assert.True(t, harbor.IsNotFound(err), "no Harbor project is created behind a registry in an unwatched namespace: %v", err)
}
