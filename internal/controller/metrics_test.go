package controller_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

func ready(reason string) []metav1.Condition {
	return []metav1.Condition{{Type: harborv1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: reason}}
}

func gather(t *testing.T, collector prometheus.Collector) map[string]float64 {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(collector))

	families, err := registry.Gather()
	require.NoError(t, err)

	values := map[string]float64{}

	for _, family := range families {
		for _, metric := range family.GetMetric() {
			key := family.GetName()
			for _, label := range metric.GetLabel() {
				key += "|" + label.GetName() + "=" + label.GetValue()
			}

			values[key] = metric.GetGauge().GetValue()
		}
	}

	return values
}

func TestResourceCollectorCountsByKindReasonAndHealth(t *testing.T) {
	runtimeScheme, err := scheme.New()
	require.NoError(t, err)

	object := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: "secret-team"} }

	reader := fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(
		&harborv1alpha1.HarborProject{ObjectMeta: object("a"), Status: harborv1alpha1.HarborProjectStatus{Conditions: ready(harborv1alpha1.ReasonReady)}},
		&harborv1alpha1.HarborProject{ObjectMeta: object("b"), Status: harborv1alpha1.HarborProjectStatus{Conditions: ready(harborv1alpha1.ReasonReady)}},
		&harborv1alpha1.HarborProject{ObjectMeta: object("c"), Status: harborv1alpha1.HarborProjectStatus{Conditions: ready(harborv1alpha1.ReasonNotAllowed)}},
		&harborv1alpha1.HarborProject{ObjectMeta: object("d")},
		&harborv1alpha1.HarborRegistry{ObjectMeta: object("r1"), Status: harborv1alpha1.HarborRegistryStatus{Health: harborv1alpha1.RegistryHealthHealthy, Conditions: ready(harborv1alpha1.ReasonReady)}},
		&harborv1alpha1.HarborRegistry{ObjectMeta: object("r2"), Status: harborv1alpha1.HarborRegistryStatus{Health: harborv1alpha1.RegistryHealthUnhealthy}},
		&harborv1alpha1.HarborRegistry{ObjectMeta: object("r3")},
	).Build()

	elected := make(chan struct{})
	collector := controller.NewResourceCollector(reader, elected)

	assert.Empty(t, gather(t, collector), "a replica that is not the leader exports nothing")

	close(elected)

	values := gather(t, collector)

	assert.InDelta(t, 2, values["harbor_operator_resources|kind=HarborProject|reason=Ready"], 0)
	assert.InDelta(t, 1, values["harbor_operator_resources|kind=HarborProject|reason=NotAllowed"], 0)
	assert.InDelta(t, 1, values["harbor_operator_resources|kind=HarborProject|reason=Unknown"], 0)
	assert.InDelta(t, 1, values["harbor_operator_resources|kind=HarborRegistry|reason=Ready"], 0)
	assert.InDelta(t, 1, values["harbor_operator_registry_health|health=healthy"], 0)
	assert.InDelta(t, 1, values["harbor_operator_registry_health|health=unhealthy"], 0)
	assert.InDelta(t, 1, values["harbor_operator_registry_health|health=unknown"], 0)

	for key := range values {
		assert.NotContains(t, key, "secret-team")
		assert.NotContains(t, key, "namespace=")
	}
}
