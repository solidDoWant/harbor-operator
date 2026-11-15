package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

func TestConnectionNotReadyForDeletion(t *testing.T) {
	terminating := metav1.Now()

	connectionWith := func(deleted *metav1.Time, identity string, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborConnection {
		connection := &harborv1alpha1.HarborConnection{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "harbor", DeletionTimestamp: deleted},
			Status:     harborv1alpha1.HarborConnectionStatus{HarborIdentity: identity},
		}
		if status != "" {
			connection.Status.Conditions = []metav1.Condition{{Type: harborv1alpha1.ConditionReady, Status: status, Reason: reason}}
		}

		return connection
	}

	for _, test := range []struct {
		name       string
		connection *harborv1alpha1.HarborConnection
		wantReason string
	}{
		{
			name:       "ready connection is usable",
			connection: connectionWith(nil, "https://harbor.example.com", metav1.ConditionTrue, harborv1alpha1.ReasonReady),
		},
		{
			name:       "terminating connection waiting for its dependents is usable",
			connection: connectionWith(&terminating, "https://harbor.example.com", metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist),
		},
		{
			name:       "terminating connection that last failed to reach Harbor is still tried",
			connection: connectionWith(&terminating, "https://harbor.example.com", metav1.ConditionFalse, harborv1alpha1.ReasonHarborUnavailable),
		},
		{
			name:       "terminating connection that never reached Harbor waits",
			connection: connectionWith(&terminating, "", metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist),
			wantReason: harborv1alpha1.ReasonDependentsExist,
		},
		{
			name:       "connection that is not terminating and not ready waits with its reason",
			connection: connectionWith(nil, "https://harbor.example.com", metav1.ConditionFalse, harborv1alpha1.ReasonCredentialsInvalid),
			wantReason: harborv1alpha1.ReasonCredentialsInvalid,
		},
		{
			name:       "connection without a Ready condition waits as HarborUnavailable",
			connection: connectionWith(nil, "", "", ""),
			wantReason: harborv1alpha1.ReasonHarborUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reason, message := connectionNotReadyForDeletion(test.connection)
			assert.Equal(t, test.wantReason, reason)

			if test.wantReason == "" {
				assert.Empty(t, message)
			} else {
				assert.Equal(t, "HarborConnection default/harbor is not ready", message)
			}
		})
	}
}
