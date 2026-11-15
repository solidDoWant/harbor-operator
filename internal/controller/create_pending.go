package controller

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// createClockSkew allows for Harbor's clock running behind the operator's when a create marker is compared with the
// creation time Harbor reports.
const createClockSkew = 2 * time.Minute

// isOwnCreate reports whether a Harbor object created at created is the resource's own interrupted create. The proof is
// the marker the operator writes to status before a create call, which namespace users cannot write: the object counts
// as the resource's own only if it was created no earlier than the marker, less the clock-skew allowance.
func isOwnCreate(pending *metav1.Time, created time.Time) bool {
	return pending != nil && !created.IsZero() && !created.Before(pending.Add(-createClockSkew))
}

// removeLegacyCreatePending removes the create-pending annotation of an earlier design. Anyone who can write the
// resource can set it, so it proves nothing and is never read.
func removeLegacyCreatePending(ctx context.Context, cl client.Client, object client.Object) error {
	annotations := object.GetAnnotations()
	if _, ok := annotations[harborv1alpha1.AnnotationCreatePending]; !ok {
		return nil
	}

	delete(annotations, harborv1alpha1.AnnotationCreatePending)
	object.SetAnnotations(annotations)

	return cl.Update(ctx, object)
}
