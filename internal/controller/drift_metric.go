package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

var driftCorrections = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "harbor_operator_drift_corrections_total",
	Help: "Updates to a Harbor object while the resource's generation equals its observed generation, by kind.",
}, []string{"kind"})

func init() {
	ctrlmetrics.Registry.MustRegister(driftCorrections)
}

// driftCheck says whether an update of a Harbor object counts as a drift correction. It is taken before the
// reconcile writes status, because writing status moves the observed generation to the current one.
type driftCheck struct {
	kind  string
	drift bool
}

// driftOf captures the kind and whether the resource's spec was already reconciled at its current generation.
func driftOf(object client.Object) driftCheck {
	var (
		kind     string
		observed int64
	)

	switch typed := object.(type) {
	case *harborv1alpha1.HarborProject:
		kind, observed = "HarborProject", typed.Status.ObservedGeneration
	case *harborv1alpha1.HarborRegistry:
		kind, observed = "HarborRegistry", typed.Status.ObservedGeneration
	case *harborv1alpha1.HarborRetentionPolicy:
		kind, observed = "HarborRetentionPolicy", typed.Status.ObservedGeneration
	case *harborv1alpha1.HarborImmutabilityPolicy:
		kind, observed = "HarborImmutabilityPolicy", typed.Status.ObservedGeneration
	case *harborv1alpha1.HarborProjectRobot:
		kind, observed = "HarborProjectRobot", typed.Status.ObservedGeneration
	case *harborv1alpha1.HarborSystemRobot:
		kind, observed = "HarborSystemRobot", typed.Status.ObservedGeneration
	default:
		return driftCheck{}
	}

	return driftCheck{kind: kind, drift: observed == object.GetGeneration()}
}

// count adds one drift correction when the check says the update was not caused by a spec change.
func (c driftCheck) count() {
	if c.drift {
		driftCorrections.WithLabelValues(c.kind).Inc()
	}
}
