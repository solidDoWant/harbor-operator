package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
)

// ImmutabilityFinalizer keeps a HarborImmutabilityPolicy until its Harbor rules have been dealt with.
const ImmutabilityFinalizer = "harbor.soliddowant.dev/immutability"

// HarborImmutabilityPolicyReconciler makes a Harbor project's tag immutability rules match its
// HarborImmutabilityPolicy.
type HarborImmutabilityPolicyReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
	// Recorder emits events on the resource.
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborimmutabilitypolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborimmutabilitypolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborimmutabilitypolicies/finalizers,verbs=update

// SetupWithManager registers the reconciler with its watches: projects, namespaces and the other claimants of the
// same rule set.
func (r *HarborImmutabilityPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborImmutabilityPolicy{}).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(r.policiesForProject)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.policiesInNamespace)).
		Watches(&harborv1alpha1.HarborImmutabilityPolicy{}, handler.EnqueueRequestsFromMapFunc(r.otherClaimants)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Named("harborimmutabilitypolicy").
		Complete(r)
}

// policyProjectKey is the project an immutability policy references.
func policyProjectKey(policy *harborv1alpha1.HarborImmutabilityPolicy) types.NamespacedName {
	namespace := policy.Spec.ProjectRef.Namespace
	if namespace == "" {
		namespace = policy.Namespace
	}

	return types.NamespacedName{Namespace: namespace, Name: policy.Spec.ProjectRef.Name}
}

func (r *HarborImmutabilityPolicyReconciler) policiesForProject(ctx context.Context, object client.Object) []reconcile.Request {
	return r.policyRequests(ctx, client.MatchingFields{ImmutabilityProjectIndex: client.ObjectKeyFromObject(object).String()})
}

// policiesInNamespace re-queues every policy when a namespace's labels may change which projects admit which policies.
func (r *HarborImmutabilityPolicyReconciler) policiesInNamespace(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.policyRequests(ctx)
}

func (r *HarborImmutabilityPolicyReconciler) otherClaimants(ctx context.Context, object client.Object) []reconcile.Request {
	key := object.(*harborv1alpha1.HarborImmutabilityPolicy).Status.HarborKey
	if key == "" {
		return nil
	}

	self := client.ObjectKeyFromObject(object)
	requests := r.policyRequests(ctx, client.MatchingFields{ownership.HarborKeyField: key})

	others := requests[:0]
	for _, request := range requests {
		if request.NamespacedName != self {
			others = append(others, request)
		}
	}

	return others
}

func (r *HarborImmutabilityPolicyReconciler) policyRequests(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	var policies harborv1alpha1.HarborImmutabilityPolicyList
	if err := r.List(ctx, &policies, options...); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(policies.Items))
	for index := range policies.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[index])})
	}

	return requests
}

// immutabilityResync is the policy's resync interval: its own, else the connection's default.
func immutabilityResync(policy *harborv1alpha1.HarborImmutabilityPolicy, connection *harborv1alpha1.HarborConnection) time.Duration {
	if policy.Spec.ResyncInterval != nil && policy.Spec.ResyncInterval.Duration > 0 {
		return policy.Spec.ResyncInterval.Duration
	}

	if connection != nil {
		return resyncInterval(connection)
	}

	return defaultResyncInterval
}

// immutabilityReconcile carries what one reconcile has resolved.
type immutabilityReconcile struct {
	policy  *harborv1alpha1.HarborImmutabilityPolicy
	project *harborv1alpha1.HarborProject
	harbor  *harbor.Client
	resync  time.Duration
}

// Reconcile makes the project's immutability rules match the resource and records the result in status.
func (r *HarborImmutabilityPolicyReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	policy := &harborv1alpha1.HarborImmutabilityPolicy{}
	if err := r.Get(ctx, request.NamespacedName, policy); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, policy)
	}

	if controllerutil.AddFinalizer(policy, ImmutabilityFinalizer) {
		if err := r.Update(ctx, policy); err != nil {
			return reconcile.Result{}, err
		}
	}

	state, result, err := r.resolve(ctx, policy)
	if state == nil {
		return result, err
	}

	return r.sync(ctx, state)
}

// resolve finds the project and checks that it admits the policy, gets a Harbor client, checks ownership and records
// the claim. It returns nil state when the reconcile ends here, with the result to return.
func (r *HarborImmutabilityPolicyReconciler) resolve(ctx context.Context, policy *harborv1alpha1.HarborImmutabilityPolicy) (*immutabilityReconcile, reconcile.Result, error) {
	resync := immutabilityResync(policy, nil)

	stop := func(reason, message string) (*immutabilityReconcile, reconcile.Result, error) {
		return nil, reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, policy, metav1.ConditionFalse, reason, message)
	}

	key := policyProjectKey(policy)
	project := &harborv1alpha1.HarborProject{}

	if err := r.Get(ctx, key, project); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}

		message := fmt.Sprintf("HarborProject %s not found", key)
		r.setReferenceResolved(policy, metav1.ConditionFalse, harborv1alpha1.ReasonNotFound, message)

		return stop(harborv1alpha1.ReasonProjectNotReady, message)
	}

	admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, policy.Namespace)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if !admitted {
		message := fmt.Sprintf("HarborProject %s does not admit namespace %q", key, policy.Namespace)
		r.setReferenceResolved(policy, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	r.setReferenceResolved(policy, metav1.ConditionTrue, harborv1alpha1.ReasonReferenceResolved, "HarborProject "+key.String()+" admits the namespace")

	if !projectUsable(project) {
		return stop(harborv1alpha1.ReasonProjectNotReady, fmt.Sprintf("HarborProject %s has no Harbor project it owns yet", key))
	}

	connection := &harborv1alpha1.HarborConnection{}
	if err := r.Get(ctx, connectionKey(project), connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}

		return stop(harborv1alpha1.ReasonProjectNotReady, fmt.Sprintf("HarborConnection of HarborProject %s not found", key))
	}

	resync = immutabilityResync(policy, connection)

	identity := connection.Status.HarborIdentity
	if ready := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionTrue || identity == "" {
		reason := harborv1alpha1.ReasonHarborUnavailable
		if ready != nil && ready.Status == metav1.ConditionFalse {
			reason = ready.Reason
		}

		return stop(reason, fmt.Sprintf("HarborConnection %s is not ready", client.ObjectKeyFromObject(connection)))
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return stop(reason, message)
	}

	harborKey := ownership.ImmutabilityKey(identity, projectName(project))
	if policy.Status.HarborKey != harborKey || (policy.Status.ProjectID != 0 && policy.Status.ProjectID != project.Status.ProjectID) {
		// A claim recorded for another Harbor identity, project name or a project since re-created names nothing here:
		// start over.
		policy.Status.ProjectID = 0
		policy.Status.HarborKey = harborKey

		if err := r.updateStatus(ctx, policy); err != nil {
			return nil, reconcile.Result{}, err
		}
	}

	owner, err := ownership.ImmutabilityOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if owner == nil {
		// The cache does not show this resource's claim yet.
		return nil, reconcile.Result{RequeueAfter: claimantCacheDelay}, nil
	}

	if owner.UID != policy.UID {
		return stop(harborv1alpha1.ReasonConflict, fmt.Sprintf("the immutability rules of Harbor project %q are owned by HarborImmutabilityPolicy %s/%s",
			projectName(project), owner.Namespace, owner.Name))
	}

	if policy.Status.ProjectID == 0 {
		policy.Status.ProjectID = project.Status.ProjectID
		if err := r.updateStatus(ctx, policy); err != nil {
			return nil, reconcile.Result{}, err
		}
	}

	return &immutabilityReconcile{policy: policy, project: project, harbor: harborClient, resync: resync}, reconcile.Result{}, nil
}

// admitted reports whether a claimant's project admits the claimant's namespace.
func (r *HarborImmutabilityPolicyReconciler) admitted(ctx context.Context, claimant *harborv1alpha1.HarborImmutabilityPolicy) (bool, error) {
	project := &harborv1alpha1.HarborProject{}
	if err := r.Get(ctx, policyProjectKey(claimant), project); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, claimant.Namespace)
}

// sync makes Harbor's rule list match the spec: new rules first, then changed ones, then removals, so a tag both lists
// protect is never briefly unprotected.
func (r *HarborImmutabilityPolicyReconciler) sync(ctx context.Context, state *immutabilityReconcile) (reconcile.Result, error) {
	projectID := int64(state.policy.Status.ProjectID)

	have, err := state.harbor.ListImmutabilityRules(ctx, projectID)
	if err != nil {
		return r.harborFailed(ctx, state.policy, err)
	}

	want := desiredImmutabilityRules(state.policy)
	plan := planImmutabilityRules(want, have)

	if len(have) == 0 && len(plan.create) > 0 {
		defer recordCreated(r.Recorder, state.policy, "immutability rules of project", projectName(state.project))
	} else if len(plan.create)+len(plan.update)+len(plan.remove) > 0 {
		defer recordUpdated(r.Recorder, state.policy, "immutability rules of project", projectName(state.project), "rules")
	}

	for _, rule := range plan.create {
		if err := state.harbor.CreateImmutabilityRule(ctx, projectID, rule); err != nil {
			return r.harborFailed(ctx, state.policy, err)
		}
	}

	if len(plan.create) > 0 {
		// Harbor creates every rule enabled, so a rule wanted disabled needs an update with the ID Harbor gave it.
		if have, err = state.harbor.ListImmutabilityRules(ctx, projectID); err != nil {
			return r.harborFailed(ctx, state.policy, err)
		}

		plan = planImmutabilityRules(want, have)
	}

	for _, rule := range plan.update {
		if err := state.harbor.UpdateImmutabilityRule(ctx, projectID, rule); err != nil {
			return r.harborFailed(ctx, state.policy, err)
		}
	}

	for _, rule := range plan.remove {
		if err := state.harbor.DeleteImmutabilityRule(ctx, projectID, rule.ID); err != nil && !harbor.IsNotFound(err) {
			return r.harborFailed(ctx, state.policy, err)
		}
	}

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, state.policy, metav1.ConditionTrue, harborv1alpha1.ReasonReady,
		fmt.Sprintf("Harbor project %q has the %d immutability rules of the spec", projectName(state.project), len(state.policy.Spec.Rules)))
}

// finalize runs the deletion preamble: it deletes the project's immutability rules when the resource owns them and its
// policy is Delete, then removes the finalizer.
func (r *HarborImmutabilityPolicyReconciler) finalize(ctx context.Context, policy *harborv1alpha1.HarborImmutabilityPolicy) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, ImmutabilityFinalizer) {
		return reconcile.Result{}, nil
	}

	release := func() (reconcile.Result, error) {
		controllerutil.RemoveFinalizer(policy, ImmutabilityFinalizer)

		return reconcile.Result{}, client.IgnoreNotFound(r.Update(ctx, policy))
	}

	// Without a claimed ID the resource never owned the project's rules.
	if policy.Status.ProjectID == 0 {
		return release()
	}

	key := policyProjectKey(policy)
	project := &harborv1alpha1.HarborProject{}

	if err := r.Get(ctx, key, project); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		// Without its project the rules cannot be reached through an admitted reference.
		return release()
	}

	connection := &harborv1alpha1.HarborConnection{}
	connectionErr := r.Get(ctx, connectionKey(project), connection)

	if connectionErr != nil && !apierrors.IsNotFound(connectionErr) {
		return reconcile.Result{}, connectionErr
	}

	deletion := policy.Spec.DeletionPolicy
	if deletion == "" && connectionErr == nil {
		deletion = connection.Spec.Defaults.DeletionPolicy
	}

	if deletion == harborv1alpha1.DeletionPolicyOrphan {
		recordOrphaned(r.Recorder, policy, "immutability rules of project", policy.Name)

		return release()
	}

	admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, policy.Namespace)
	if err != nil {
		return reconcile.Result{}, err
	}

	if !admitted {
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"HarborProject %s does not admit namespace %q; the immutability rules are left in place", key, policy.Namespace)

		return release()
	}

	if connectionErr != nil {
		// A connection that no longer exists admits nothing, so the project reference is no longer admitted.
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"the HarborConnection of HarborProject %s no longer exists; the immutability rules are left in place", key)

		return release()
	}

	resync := immutabilityResync(policy, connection)

	identity := connection.Status.HarborIdentity
	if reason, message := connectionNotReadyForDeletion(connection); reason != "" {
		return reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, policy, metav1.ConditionFalse, reason, message)
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, policy, metav1.ConditionFalse, reason, message)
	}

	harborKey := ownership.ImmutabilityKey(identity, projectName(project))
	if policy.Status.HarborKey != harborKey || policy.Status.ProjectID != project.Status.ProjectID {
		// The claim was recorded for another Harbor identity or project; it names nothing of this resource's here.
		return release()
	}

	// Only the owner deletes the rules.
	owner, err := ownership.ImmutabilityOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return reconcile.Result{}, err
	}

	if owner == nil || owner.UID != policy.UID {
		return release()
	}

	projectID := int64(policy.Status.ProjectID)

	rules, err := harborClient.ListImmutabilityRules(ctx, projectID)

	switch {
	case harbor.IsNotFound(err):
		return release()
	case err != nil:
		return r.harborFailed(ctx, policy, err)
	}

	for _, rule := range rules {
		if err := harborClient.DeleteImmutabilityRule(ctx, projectID, rule.ID); err != nil && !harbor.IsNotFound(err) {
			return r.harborFailed(ctx, policy, err)
		}
	}

	recordDeleted(r.Recorder, policy, "immutability rules of project", policy.Name)

	return release()
}

// harborFailed reports a Harbor error. A spec Harbor rejects waits for a spec change; anything else retries with
// backoff.
func (r *HarborImmutabilityPolicyReconciler) harborFailed(ctx context.Context, policy *harborv1alpha1.HarborImmutabilityPolicy, err error) (reconcile.Result, error) {
	reason, retry := harbor.MapError(err)
	if statusErr := r.setReady(ctx, policy, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	if !retry {
		return reconcile.Result{}, nil
	}

	return reconcile.Result{}, err
}

func (r *HarborImmutabilityPolicyReconciler) setReferenceResolved(policy *harborv1alpha1.HarborImmutabilityPolicy, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReferenceResolved, Status: status, Reason: reason, Message: message, ObservedGeneration: policy.Generation,
	})
}

// setReady records the Ready condition and emits an event when it changes.
func (r *HarborImmutabilityPolicyReconciler) setReady(ctx context.Context, policy *harborv1alpha1.HarborImmutabilityPolicy, status metav1.ConditionStatus, reason, message string) error {
	previous := meta.FindStatusCondition(policy.Status.Conditions, harborv1alpha1.ConditionReady)
	changed := previous == nil || previous.Status != status || previous.Reason != reason || previous.Message != message

	policy.Status.ObservedGeneration = policy.Generation
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: policy.Generation,
	})

	if err := r.updateStatus(ctx, policy); err != nil {
		return err
	}

	if changed {
		eventType := corev1.EventTypeNormal
		if status != metav1.ConditionTrue {
			eventType = corev1.EventTypeWarning
		}

		r.Recorder.Eventf(policy, nil, eventType, reason, "Reconcile", "%s", message)
	}

	return nil
}

// updateStatus writes status only when it differs from what the API server has.
func (r *HarborImmutabilityPolicyReconciler) updateStatus(ctx context.Context, policy *harborv1alpha1.HarborImmutabilityPolicy) error {
	stored := &harborv1alpha1.HarborImmutabilityPolicy{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(policy), stored); err == nil &&
		stored.ResourceVersion == policy.ResourceVersion && apiequality.Semantic.DeepEqual(stored.Status, policy.Status) {
		return nil
	}

	return r.Status().Update(ctx, policy)
}
