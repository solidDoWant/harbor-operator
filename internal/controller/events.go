package controller

import (
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// recordCreated emits the event for a Harbor object this resource just created.
func recordCreated(recorder events.EventRecorder, object runtime.Object, what, name string) {
	recorder.Eventf(object, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonCreated, "Create", "Created Harbor %s %q", what, name)
}

// recordUpdated emits the event for a drift correction. It names the fields that changed, never their values.
func recordUpdated(recorder events.EventRecorder, object runtime.Object, what, name string, fields ...string) {
	fields = slices.DeleteFunc(slices.Clone(fields), func(field string) bool { return field == "" })
	if len(fields) == 0 {
		return
	}

	recorder.Eventf(object, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonUpdated, "Update",
		"Updated Harbor %s %q to match the spec: %s", what, name, strings.Join(fields, ", "))
}

// recordDeleted emits the event for a Harbor object this resource deleted.
func recordDeleted(recorder events.EventRecorder, object runtime.Object, what, name string) {
	recorder.Eventf(object, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonDeleted, "Delete", "Deleted Harbor %s %q", what, name)
}

// recordOrphaned emits the event for a Harbor object left in place when its resource is deleted.
func recordOrphaned(recorder events.EventRecorder, object runtime.Object, what, name string) {
	recorder.Eventf(object, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonOrphaned, "Delete", "Left Harbor %s %q in place (deletion policy Orphan)", what, name)
}

// fieldIf returns name when set is true, else the empty string; recordUpdated drops empty names.
func fieldIf(set bool, name string) string {
	if set {
		return name
	}

	return ""
}
