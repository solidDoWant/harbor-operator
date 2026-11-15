package leakscan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/solidDoWant/harbor-operator/test/leakscan"
)

func TestScanFindsASecretInTheLogAndNamesItsLabelOnly(t *testing.T) {
	log := &leakscan.Log{}
	log.Logger().Info("created robot", "password", "hunter2-secret")

	found, err := leakscan.Scan(t.Context(), fake.NewClientBuilder().Build(), log, nil, map[string]string{"robot secret": "hunter2-secret"})
	require.NoError(t, err)
	assert.Equal(t, []string{"operator log contains robot secret"}, found)
}

func TestScanFindsASecretInAnEvent(t *testing.T) {
	cl := fake.NewClientBuilder().WithObjects(&corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "e"}, Message: "bad password hunter2-secret",
	}).Build()

	found, err := leakscan.Scan(t.Context(), cl, &leakscan.Log{}, nil, map[string]string{"connection password": "hunter2-secret"})
	require.NoError(t, err)
	assert.Equal(t, []string{"event default/e contains connection password"}, found)
}

func TestScanFindsASecretOnlyInStatus(t *testing.T) {
	cl := fake.NewClientBuilder().WithObjects(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "p"},
		Spec:       corev1.PodSpec{Hostname: "hunter2-secret"},
		Status:     corev1.PodStatus{Message: "hunter2-secret"},
	}).Build()

	found, err := leakscan.Scan(t.Context(), cl, &leakscan.Log{}, []client.ObjectList{&corev1.PodList{}}, map[string]string{"s": "hunter2-secret"})
	require.NoError(t, err)
	assert.Equal(t, []string{"status of *v1.Pod default/p contains s"}, found)
}

func TestScanIsCleanWhenNoSecretAppears(t *testing.T) {
	log := &leakscan.Log{}
	log.Logger().Info("reconciled", "name", "ci")

	found, err := leakscan.Scan(t.Context(), fake.NewClientBuilder().Build(), log, nil, map[string]string{"s": "hunter2-secret", "empty": ""})
	require.NoError(t, err)
	assert.Empty(t, found)
}
