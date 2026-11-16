package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

func TestOperatorCredentialCountsOtherConnectionsOnlyWhileHarborAcceptsThem(t *testing.T) {
	t.Parallel()

	const identity = "https://harbor.example.com"

	tests := []struct {
		name            string
		generation      int64
		readyStatus     metav1.ConditionStatus
		readyReason     string
		readyGeneration int64
		want            string
	}{
		{
			name:       "accepted for its current generation",
			generation: 2, readyStatus: metav1.ConditionTrue, readyReason: harborv1alpha1.ReasonReady, readyGeneration: 2,
			want: "tenant/evil",
		},
		{
			name:       "rejected by Harbor",
			generation: 2, readyStatus: metav1.ConditionFalse, readyReason: harborv1alpha1.ReasonUnauthorized, readyGeneration: 2,
		},
		{
			name:       "accepted only for an older generation",
			generation: 2, readyStatus: metav1.ConditionTrue, readyReason: harborv1alpha1.ReasonReady, readyGeneration: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runtimeScheme, err := scheme.New()
			require.NoError(t, err)

			robot := &harborv1alpha1.HarborSystemRobot{
				ObjectMeta: metav1.ObjectMeta{Namespace: "root", Name: "operator"},
				Spec: harborv1alpha1.HarborSystemRobotSpec{
					ConnectionRef: harborv1alpha1.ObjectReference{Namespace: "root", Name: "admin"},
					Name:          "operator",
				},
			}

			// The other namespace's connection names the robot through a literal username; its Secret holds a
			// credential of its own.
			connection := &harborv1alpha1.HarborConnection{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "evil", Generation: test.generation},
				Spec: harborv1alpha1.HarborConnectionSpec{
					URL:                  identity,
					CredentialsSecretRef: harborv1alpha1.CredentialsSecretReference{Name: "own-creds", Username: "robot$operator"},
				},
				Status: harborv1alpha1.HarborConnectionStatus{
					HarborIdentity: identity,
					Conditions: []metav1.Condition{{
						Type: harborv1alpha1.ConditionReady, Status: test.readyStatus, Reason: test.readyReason,
						ObservedGeneration: test.readyGeneration, LastTransitionTime: metav1.Now(),
					}},
				},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "own-creds"},
				Data:       map[string][]byte{"username": []byte("robot$tenant-ci"), "password": []byte("secret")},
			}

			reconciler := &HarborSystemRobotReconciler{
				Client: fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(robot, connection, secret).Build(),
			}

			got, err := reconciler.operatorCredential(t.Context(), robot, identity)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}
