//go:build integration

package controller_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// eventFor returns the first event with reason about object, waiting for it.
func eventFor(t *testing.T, cl client.Client, object client.Object, reason string) eventsv1.Event {
	t.Helper()

	var found eventsv1.Event

	require.Eventually(t, func() bool {
		var list eventsv1.EventList
		if err := cl.List(t.Context(), &list, client.InNamespace(object.GetNamespace())); err != nil {
			return false
		}

		for _, event := range list.Items {
			if event.Regarding.UID == object.GetUID() && event.Reason == reason {
				found = event

				return true
			}
		}

		return false
	}, projectWait, 200*time.Millisecond, "waiting for a %s event", reason)

	return found
}

func TestProjectStateChangesEmitEvents(t *testing.T) {
	t.Parallel()

	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "project")
	connection := readyConnection(t, cl, instance, ns, nil)

	project := newProject(ns, name, connection)
	project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	project.Spec.Public = ptr(true)
	require.NoError(t, cl.Create(t.Context(), project))
	ready := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	created := eventFor(t, cl, ready, harborv1alpha1.EventReasonCreated)
	assert.Equal(t, "Normal", created.Type)

	require.NoError(t, admin.UpdateProjectMetadata(t.Context(), name, harbor.ProjectSettings{Public: ptr(false)}))

	updated := eventFor(t, cl, ready, harborv1alpha1.EventReasonUpdated)
	assert.Equal(t, "Normal", updated.Type)
	assert.Contains(t, updated.Note, "public")
	assert.NotContains(t, updated.Note, "true", "an event names the changed fields, never their values")

	require.NoError(t, cl.Delete(t.Context(), ready))
	deleted := eventFor(t, cl, ready, harborv1alpha1.EventReasonDeleted)
	assert.Equal(t, "Normal", deleted.Type)
}

func TestProjectLeftInHarborEmitsAnOrphanedEvent(t *testing.T) {
	t.Parallel()

	instance, _, name := projectHarbor(t)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "project")
	connection := readyConnection(t, cl, instance, ns, nil)

	project := newProject(ns, name, connection)
	project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyOrphan
	require.NoError(t, cl.Create(t.Context(), project))
	ready := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	require.NoError(t, cl.Delete(t.Context(), ready))
	eventFor(t, cl, ready, harborv1alpha1.EventReasonOrphaned)
}

func TestProjectErrorReasonEmitsAWarningEvent(t *testing.T) {
	t.Parallel()

	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "project")
	connection := readyConnection(t, cl, instance, ns, nil)

	_, err := admin.CreateProxyCacheProject(t.Context(), name, harbor.ProjectSettings{}, nil, 0)
	require.NoError(t, err)

	project := newProject(ns, name, connection)
	require.NoError(t, cl.Create(t.Context(), project))
	conflicted := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)

	warning := eventFor(t, cl, conflicted, harborv1alpha1.ReasonAlreadyExists)
	assert.Equal(t, "Warning", warning.Type)
}

func TestConnectionReadinessEmitsAnEvent(t *testing.T) {
	t.Parallel()

	instance, _, _ := projectHarbor(t)
	cl := startOperator(t, true)
	ns := newNamespace(t, cl, "connection")
	connection := readyConnection(t, cl, instance, ns, nil)

	event := eventFor(t, cl, connection, harborv1alpha1.ReasonReady)
	assert.Equal(t, "Normal", event.Type)
}
