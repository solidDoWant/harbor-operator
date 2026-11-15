package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
)

const (
	// RobotProjectIndex finds the robots that reference a project. Its value is "namespace/name", the namespace
	// defaulting to the robot's own.
	RobotProjectIndex = "spec.projectRef"
	// ImmutabilityProjectIndex finds the immutability policies that reference a project. Its value is
	// "namespace/name", the namespace defaulting to the policy's own.
	ImmutabilityProjectIndex = "spec.projectRef"
)

// robotProjectKey is the project a robot references.
func robotProjectKey(robot *harborv1alpha1.HarborProjectRobot) types.NamespacedName {
	namespace := robot.Spec.ProjectRef.Namespace
	if namespace == "" {
		namespace = robot.Namespace
	}

	return types.NamespacedName{Namespace: namespace, Name: robot.Spec.ProjectRef.Name}
}

func (r *HarborProjectReconciler) projectForRetention(_ context.Context, object client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: retentionProjectKey(object.(*harborv1alpha1.HarborRetentionPolicy))}}
}

func (r *HarborProjectReconciler) projectForRobot(_ context.Context, object client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: robotProjectKey(object.(*harborv1alpha1.HarborProjectRobot))}}
}

// projectForImmutabilityPolicy re-queues the project an immutability policy references, so a project waiting for its
// dependents sees the policy go.
func (r *HarborProjectReconciler) projectForImmutabilityPolicy(_ context.Context, object client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: policyProjectKey(object.(*harborv1alpha1.HarborImmutabilityPolicy))}}
}

// finalize runs instead of the normal reconcile once the resource is being deleted. It deletes the Harbor project only
// when the resource still owns it and its deletion policy is Delete; every other case releases the resource without
// touching Harbor.
func (r *HarborProjectReconciler) finalize(ctx context.Context, project *harborv1alpha1.HarborProject) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(project, ProjectFinalizer) {
		return reconcile.Result{}, nil
	}

	// Without an ID the resource never had a Harbor project to deal with.
	if project.Status.ProjectID == 0 {
		return reconcile.Result{}, r.release(ctx, project)
	}

	dependents, err := r.dependents(ctx, project)
	if err != nil {
		return reconcile.Result{}, err
	}

	if len(dependents) > 0 {
		listed, suffix := dependents, ""
		if len(listed) > maxListedDependents {
			listed, suffix = listed[:maxListedDependents], fmt.Sprintf(" and %d more", len(dependents)-maxListedDependents)
		}

		// A deleted robot or policy triggers a new reconcile through its watch.
		return reconcile.Result{}, r.setReady(ctx, project, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist,
			"still used by "+strings.Join(listed, ", ")+suffix)
	}

	key := connectionKey(project)
	connection := &harborv1alpha1.HarborConnection{}

	if !r.WatchNamespaces.Watches(key.Namespace) {
		r.Recorder.Eventf(project, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"%s; the Harbor project is left in place", access.NotWatchedMessage(key.Namespace))

		return reconcile.Result{}, r.release(ctx, project)
	}

	if err := r.Get(ctx, key, connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		connection = nil
	}

	if deletionPolicy(project, connection) == harborv1alpha1.DeletionPolicyOrphan {
		recordOrphaned(r.Recorder, project, "project", projectName(project))

		return reconcile.Result{}, r.release(ctx, project)
	}

	// A connection that no longer exists admits nothing. It only goes while it admits none of its projects, so this
	// project's reference is no longer admitted and the project is orphaned like any other.
	admitted := false

	if connection != nil {
		var err error
		if admitted, err = access.Admits(ctx, r.Client, connection.Spec.AllowedNamespaces, connection.Namespace, project.Namespace); err != nil {
			return reconcile.Result{}, err
		}
	}

	if !admitted {
		r.Recorder.Eventf(project, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"HarborConnection %s does not admit namespace %q; the Harbor project is left in place", key, project.Namespace)

		return reconcile.Result{}, r.release(ctx, project)
	}

	identity := connection.Status.HarborIdentity
	if reason, message := connectionNotReadyForDeletion(connection); reason != "" {
		return r.deleteFailed(ctx, project, reason, message)
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return r.deleteFailed(ctx, project, reason, message)
	}

	return r.deleteOwned(ctx, project, harborClient, identity)
}

// deleteOwned deletes the Harbor project by ID when the ID still names this resource's own project, and releases the
// resource once it is gone.
func (r *HarborProjectReconciler) deleteOwned(ctx context.Context, project *harborv1alpha1.HarborProject, harborClient *harbor.Client, identity string) (reconcile.Result, error) {
	name := projectName(project)

	harborKey := ownership.ProjectKey(identity, name)
	if project.Status.HarborKey != harborKey {
		// The ID was recorded in another Harbor; it names nothing of this resource's here.
		return reconcile.Result{}, r.release(ctx, project)
	}

	owner, err := ownership.ProjectOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return reconcile.Result{}, err
	}

	if owner == nil || owner.UID != project.UID {
		return reconcile.Result{}, r.release(ctx, project)
	}

	id := int64(project.Status.ProjectID)

	found, err := harborClient.GetProject(ctx, id)

	switch {
	case harbor.IsNotFound(err):
		return reconcile.Result{}, r.release(ctx, project)
	case err != nil:
		reason, _ := harbor.MapError(err)
		return r.deleteFailed(ctx, project, reason, err.Error())
	case found.Name != name:
		// The ID now names another project, which this resource never created or adopted.
		return reconcile.Result{}, r.release(ctx, project)
	}

	if err := harborClient.DeleteProjectByID(ctx, id); err != nil && !harbor.IsNotFound(err) {
		reason, _ := harbor.MapError(err)
		return r.deleteFailed(ctx, project, reason, err.Error())
	}

	recordDeleted(r.Recorder, project, "project", name)

	return reconcile.Result{}, r.release(ctx, project)
}

// deleteFailed keeps the finalizer, records why the deletion cannot finish yet and retries with backoff.
func (r *HarborProjectReconciler) deleteFailed(ctx context.Context, project *harborv1alpha1.HarborProject, reason, message string) (reconcile.Result, error) {
	if err := r.setReady(ctx, project, metav1.ConditionFalse, reason, message); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, fmt.Errorf("deleting Harbor project %q: %s", projectName(project), reason)
}

// release removes the finalizer, leaving Harbor as it is.
func (r *HarborProjectReconciler) release(ctx context.Context, project *harborv1alpha1.HarborProject) error {
	controllerutil.RemoveFinalizer(project, ProjectFinalizer)

	return client.IgnoreNotFound(r.Update(ctx, project))
}

// deletionPolicy is the resource's policy, else the connection's default, else Delete.
func deletionPolicy(project *harborv1alpha1.HarborProject, connection *harborv1alpha1.HarborConnection) harborv1alpha1.DeletionPolicy {
	if project.Spec.DeletionPolicy != "" {
		return project.Spec.DeletionPolicy
	}

	if connection != nil && connection.Spec.Defaults.DeletionPolicy != "" {
		return connection.Spec.Defaults.DeletionPolicy
	}

	return harborv1alpha1.DeletionPolicyDelete
}

// dependents lists the robots, retention policies and immutability policies that reference the project from a
// namespace it admits, as "Kind namespace/name".
func (r *HarborProjectReconciler) dependents(ctx context.Context, project *harborv1alpha1.HarborProject) ([]string, error) {
	projectKey := client.ObjectKeyFromObject(project).String()

	var robots harborv1alpha1.HarborProjectRobotList
	if err := r.List(ctx, &robots, client.MatchingFields{RobotProjectIndex: projectKey}); err != nil {
		return nil, err
	}

	var retentions harborv1alpha1.HarborRetentionPolicyList
	if err := r.List(ctx, &retentions, client.MatchingFields{RetentionProjectIndex: projectKey}); err != nil {
		return nil, err
	}

	var policies harborv1alpha1.HarborImmutabilityPolicyList
	if err := r.List(ctx, &policies, client.MatchingFields{ImmutabilityProjectIndex: projectKey}); err != nil {
		return nil, err
	}

	referrers := make([]client.Object, 0, len(robots.Items)+len(retentions.Items)+len(policies.Items))
	for index := range robots.Items {
		referrers = append(referrers, &robots.Items[index])
	}

	for index := range retentions.Items {
		referrers = append(referrers, &retentions.Items[index])
	}

	for index := range policies.Items {
		referrers = append(referrers, &policies.Items[index])
	}

	var names []string

	for _, referrer := range referrers {
		admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, referrer.GetNamespace())
		if err != nil {
			return nil, err
		}

		if admitted {
			kind := "HarborProjectRobot"

			switch referrer.(type) {
			case *harborv1alpha1.HarborRetentionPolicy:
				kind = "HarborRetentionPolicy"
			case *harborv1alpha1.HarborImmutabilityPolicy:
				kind = "HarborImmutabilityPolicy"
			}

			names = append(names, kind+" "+client.ObjectKeyFromObject(referrer).String())
		}
	}

	return names, nil
}
