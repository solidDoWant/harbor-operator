package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

func TestDriftCorrectionCountsOnlyWhenTheSpecIsUnchanged(t *testing.T) {
	project := func(generation, observed int64) *harborv1alpha1.HarborProject {
		return &harborv1alpha1.HarborProject{
			ObjectMeta: metav1.ObjectMeta{Generation: generation},
			Status:     harborv1alpha1.HarborProjectStatus{ObservedGeneration: observed},
		}
	}

	before := testutil.ToFloat64(driftCorrections.WithLabelValues("HarborProject"))

	driftOf(project(3, 2)).count()
	assert.InDelta(t, before, testutil.ToFloat64(driftCorrections.WithLabelValues("HarborProject")), 0, "a spec change is not drift")

	driftOf(project(3, 3)).count()
	assert.InDelta(t, before+1, testutil.ToFloat64(driftCorrections.WithLabelValues("HarborProject")), 0, "an update at the observed generation is drift")
}
