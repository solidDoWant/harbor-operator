package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
)

// finalize runs instead of the normal reconcile once the resource is being deleted. It waits while proxy-cache
// projects the registry admits still use it, under either policy, then deletes the Harbor registry only when the
// resource still owns it and its deletion policy is Delete.
func (r *HarborRegistryReconciler) finalize(ctx context.Context, registry *harborv1alpha1.HarborRegistry) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(registry, RegistryFinalizer) {
		return reconcile.Result{}, nil
	}

	dependents, err := r.dependents(ctx, registry)
	if err != nil {
		return reconcile.Result{}, err
	}

	if len(dependents) > 0 {
		listed, suffix := dependents, ""
		if len(listed) > maxListedDependents {
			listed, suffix = listed[:maxListedDependents], fmt.Sprintf(" and %d more", len(dependents)-maxListedDependents)
		}

		// A deleted project triggers a new reconcile through the project watch.
		return reconcile.Result{}, r.setReady(ctx, registry, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist,
			"still used by HarborProject "+strings.Join(listed, ", ")+suffix)
	}

	// Without an ID the resource never had a Harbor registry to deal with.
	if registry.Status.RegistryID == 0 {
		return reconcile.Result{}, r.release(ctx, registry)
	}

	key := registryConnectionKey(registry)
	connection := &harborv1alpha1.HarborConnection{}

	if !r.WatchNamespaces.Watches(key.Namespace) {
		r.Recorder.Eventf(registry, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"%s; the Harbor registry is left in place", access.NotWatchedMessage(key.Namespace))

		return reconcile.Result{}, r.release(ctx, registry)
	}

	if err := r.Get(ctx, key, connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		connection = nil
	}

	if registryDeletionPolicy(registry, connection) == harborv1alpha1.DeletionPolicyOrphan {
		recordOrphaned(r.Recorder, registry, "registry", registryName(registry))

		return reconcile.Result{}, r.release(ctx, registry)
	}

	// A connection that no longer exists admits nothing, so the registry is left in place like any other that is not
	// admitted.
	admitted := false
	if connection != nil {
		if admitted, err = r.systemAdmitted(ctx, connection, registry.Namespace); err != nil {
			return reconcile.Result{}, err
		}
	}

	if !admitted {
		r.Recorder.Eventf(registry, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"HarborConnection %s does not admit namespace %q; the Harbor registry is left in place", key, registry.Namespace)

		return reconcile.Result{}, r.release(ctx, registry)
	}

	identity := connection.Status.HarborIdentity
	if reason, message := connectionNotReadyForDeletion(connection); reason != "" {
		return r.deleteFailed(ctx, registry, reason, message)
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return r.deleteFailed(ctx, registry, reason, message)
	}

	return r.deleteOwned(ctx, registry, harborClient, identity)
}

// deleteOwned deletes the Harbor registry by ID when this resource owns it, and releases the resource once it is gone.
func (r *HarborRegistryReconciler) deleteOwned(ctx context.Context, registry *harborv1alpha1.HarborRegistry, harborClient *harbor.Client, identity string) (reconcile.Result, error) {
	harborKey := ownership.RegistryKey(identity, registryName(registry))
	if registry.Status.HarborKey != harborKey {
		// The ID was recorded in another Harbor; it names nothing of this resource's here.
		return reconcile.Result{}, r.release(ctx, registry)
	}

	owner, err := ownership.RegistryOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return reconcile.Result{}, err
	}

	if owner == nil || owner.UID != registry.UID {
		return reconcile.Result{}, r.release(ctx, registry)
	}

	other, err := r.otherIDOwner(ctx, registry, identity, registry.Status.RegistryID)
	if err != nil {
		return reconcile.Result{}, err
	}

	if other != nil {
		return reconcile.Result{}, r.release(ctx, registry)
	}

	if err := harborClient.DeleteRegistry(ctx, registry.Status.RegistryID); err != nil && !harbor.IsNotFound(err) {
		// Harbor answers 412 while a project or replication rule uses the registry: DeleteBlocked with its message.
		reason, _ := harbor.MapError(err)
		return r.deleteFailed(ctx, registry, reason, err.Error())
	}

	recordDeleted(r.Recorder, registry, "registry", registryName(registry))

	return reconcile.Result{}, r.release(ctx, registry)
}

// deleteFailed keeps the finalizer, records why the deletion cannot finish yet and retries with backoff.
func (r *HarborRegistryReconciler) deleteFailed(ctx context.Context, registry *harborv1alpha1.HarborRegistry, reason, message string) (reconcile.Result, error) {
	if err := r.setReady(ctx, registry, metav1.ConditionFalse, reason, message); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, fmt.Errorf("deleting Harbor registry %q: %s", registryName(registry), reason)
}

// release removes the finalizer, leaving Harbor as it is.
func (r *HarborRegistryReconciler) release(ctx context.Context, registry *harborv1alpha1.HarborRegistry) error {
	r.lastWrite.Delete(client.ObjectKeyFromObject(registry))
	controllerutil.RemoveFinalizer(registry, RegistryFinalizer)

	return client.IgnoreNotFound(r.Update(ctx, registry))
}

// registryDeletionPolicy is the resource's policy, else the connection's default, else Delete.
func registryDeletionPolicy(registry *harborv1alpha1.HarborRegistry, connection *harborv1alpha1.HarborConnection) harborv1alpha1.DeletionPolicy {
	if registry.Spec.DeletionPolicy != "" {
		return registry.Spec.DeletionPolicy
	}

	if connection != nil && connection.Spec.Defaults.DeletionPolicy != "" {
		return connection.Spec.Defaults.DeletionPolicy
	}

	return harborv1alpha1.DeletionPolicyDelete
}

// dependents lists "namespace/name" of the proxy-cache projects that reference the registry from a namespace it
// admits.
func (r *HarborRegistryReconciler) dependents(ctx context.Context, registry *harborv1alpha1.HarborRegistry) ([]string, error) {
	var projects harborv1alpha1.HarborProjectList
	if err := r.List(ctx, &projects, client.MatchingFields{ProjectRegistryIndex: client.ObjectKeyFromObject(registry).String()}); err != nil {
		return nil, err
	}

	var names []string

	for index := range projects.Items {
		project := &projects.Items[index]

		admitted, err := access.Admits(ctx, r.Client, registry.Spec.AllowedNamespaces, registry.Namespace, project.Namespace)
		if err != nil {
			return nil, err
		}

		if admitted {
			names = append(names, client.ObjectKeyFromObject(project).String())
		}
	}

	return names, nil
}
