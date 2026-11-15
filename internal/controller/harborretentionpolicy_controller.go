package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/robfig/cron/v3"
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

const (
	// RetentionFinalizer keeps a HarborRetentionPolicy until its Harbor retention policy has been dealt with.
	RetentionFinalizer = "harbor.soliddowant.dev/retention"

	// RetentionProjectIndex finds the retention policies that reference a project. Its value is "namespace/name", the
	// namespace defaulting to the policy's own.
	RetentionProjectIndex = "spec.projectRef"
)

// retentionScheduleParser parses Harbor's six-field cron expressions, seconds first.
var retentionScheduleParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// HarborRetentionPolicyReconciler makes a Harbor project's tag retention policy match its HarborRetentionPolicy.
type HarborRetentionPolicyReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
	// Recorder emits events on the resource.
	Recorder events.EventRecorder
	// Wakeups receives the connection when a Harbor call fails as unavailable. Nil sends nothing.
	Wakeups ConnectionWakeups
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborretentionpolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborretentionpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborretentionpolicies/finalizers,verbs=update

// SetupWithManager registers the reconciler with its watches: projects, namespaces and the other claimants of the same
// retention policy.
func (r *HarborRetentionPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborRetentionPolicy{}).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(r.policiesForProject)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.policiesInNamespace)).
		Watches(&harborv1alpha1.HarborRetentionPolicy{}, handler.EnqueueRequestsFromMapFunc(r.otherClaimants)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4, RateLimiter: backoffLimiter(0)}).
		Named("harborretentionpolicy").
		Complete(r)
}

// retentionProjectKey is the project a retention policy references.
func retentionProjectKey(policy *harborv1alpha1.HarborRetentionPolicy) types.NamespacedName {
	namespace := policy.Spec.ProjectRef.Namespace
	if namespace == "" {
		namespace = policy.Namespace
	}

	return types.NamespacedName{Namespace: namespace, Name: policy.Spec.ProjectRef.Name}
}

func (r *HarborRetentionPolicyReconciler) policiesForProject(ctx context.Context, object client.Object) []reconcile.Request {
	return r.policyRequests(ctx, client.MatchingFields{RetentionProjectIndex: client.ObjectKeyFromObject(object).String()})
}

// policiesInNamespace re-queues every policy when a namespace's labels may change which projects admit which policies.
func (r *HarborRetentionPolicyReconciler) policiesInNamespace(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.policyRequests(ctx)
}

func (r *HarborRetentionPolicyReconciler) otherClaimants(ctx context.Context, object client.Object) []reconcile.Request {
	key := object.(*harborv1alpha1.HarborRetentionPolicy).Status.HarborKey
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

func (r *HarborRetentionPolicyReconciler) policyRequests(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	var policies harborv1alpha1.HarborRetentionPolicyList
	if err := r.List(ctx, &policies, options...); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(policies.Items))
	for index := range policies.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[index])})
	}

	return requests
}

// desiredRetention is the Harbor retention policy the spec asks for.
func desiredRetention(policy *harborv1alpha1.HarborRetentionPolicy) harbor.RetentionPolicy {
	rules := make([]harbor.RetentionRule, 0, len(policy.Spec.Rules))

	for _, rule := range policy.Spec.Rules {
		var count int64
		if rule.Count != nil {
			count = int64(*rule.Count)
		}

		rules = append(rules, harbor.RetentionRule{
			Disabled:             rule.Disabled,
			Template:             rule.Template,
			Count:                count,
			RepositoryDecoration: rule.Repositories.Decoration,
			RepositoryPattern:    rule.Repositories.Pattern,
			TagDecoration:        rule.Tags.Decoration,
			TagPattern:           rule.Tags.Pattern,
			Untagged:             rule.Tags.Untagged,
		})
	}

	return harbor.RetentionPolicy{Rules: rules, Schedule: policy.Spec.Schedule}
}

// validateSchedule reports why a non-empty schedule is not a six-field cron expression, or "" when it is.
func validateSchedule(schedule string) string {
	if schedule == "" {
		return ""
	}

	if _, err := retentionScheduleParser.Parse(schedule); err != nil {
		return fmt.Sprintf("schedule %q is not a six-field cron expression: %v", schedule, err)
	}

	return ""
}

// retentionResync is the policy's resync interval: its own, else the connection's default.
func retentionResync(policy *harborv1alpha1.HarborRetentionPolicy, connection *harborv1alpha1.HarborConnection) time.Duration {
	if policy.Spec.ResyncInterval != nil && policy.Spec.ResyncInterval.Duration > 0 {
		return policy.Spec.ResyncInterval.Duration
	}

	if connection != nil {
		return resyncInterval(connection)
	}

	return defaultResyncInterval
}

// retentionReconcile carries what one reconcile has resolved.
type retentionReconcile struct {
	// connection is the HarborConnection the policy reaches Harbor through.
	connection types.NamespacedName
	policy     *harborv1alpha1.HarborRetentionPolicy
	projectID  int64
	harbor     *harbor.Client
	resync     time.Duration
}

// Reconcile makes the project's retention policy in Harbor match the resource and records the result in status.
func (r *HarborRetentionPolicyReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	policy := &harborv1alpha1.HarborRetentionPolicy{}
	if err := r.Get(ctx, request.NamespacedName, policy); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, policy)
	}

	if controllerutil.AddFinalizer(policy, RetentionFinalizer) {
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

// resolve finds the project and checks that it admits the policy, gets a Harbor client and checks ownership. It returns
// nil state when the reconcile ends here, with the result to return.
func (r *HarborRetentionPolicyReconciler) resolve(ctx context.Context, policy *harborv1alpha1.HarborRetentionPolicy) (*retentionReconcile, reconcile.Result, error) {
	resync := retentionResync(policy, nil)

	stop := func(reason, message string) (*retentionReconcile, reconcile.Result, error) {
		return nil, reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, policy, metav1.ConditionFalse, reason, message)
	}

	key := retentionProjectKey(policy)
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

	resync = retentionResync(policy, connection)

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

	harborKey := ownership.RetentionKey(identity, projectName(project))
	if policy.Status.HarborKey != harborKey || policy.Status.ProjectID != project.Status.ProjectID {
		// An ID recorded for another Harbor identity, project name or a project since re-created names nothing here,
		// so the claim starts over.
		policy.Status.RetentionID = 0
		policy.Status.HarborKey = harborKey
		policy.Status.ProjectID = project.Status.ProjectID

		if err := r.updateStatus(ctx, policy); err != nil {
			return nil, reconcile.Result{}, err
		}
	}

	owner, err := ownership.RetentionOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if owner == nil {
		// The cache does not show this resource's claim yet.
		return nil, reconcile.Result{RequeueAfter: claimantCacheDelay}, nil
	}

	if owner.UID != policy.UID {
		return stop(harborv1alpha1.ReasonConflict, fmt.Sprintf("the retention policy of Harbor project %q is owned by HarborRetentionPolicy %s/%s",
			projectName(project), owner.Namespace, owner.Name))
	}

	return &retentionReconcile{connection: client.ObjectKeyFromObject(connection), policy: policy, projectID: int64(project.Status.ProjectID), harbor: harborClient, resync: resync}, reconcile.Result{}, nil
}

// admitted reports whether a claimant's project admits the claimant's namespace.
func (r *HarborRetentionPolicyReconciler) admitted(ctx context.Context, claimant *harborv1alpha1.HarborRetentionPolicy) (bool, error) {
	project := &harborv1alpha1.HarborProject{}
	if err := r.Get(ctx, retentionProjectKey(claimant), project); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, claimant.Namespace)
}

// sync creates the project's retention policy, or checks that the existing one belongs to the project and makes it
// match the spec.
func (r *HarborRetentionPolicyReconciler) sync(ctx context.Context, state *retentionReconcile) (reconcile.Result, error) {
	policy := state.policy

	if message := validateSchedule(policy.Spec.Schedule); message != "" {
		return reconcile.Result{}, r.setReady(ctx, policy, metav1.ConditionFalse, harborv1alpha1.ReasonInvalidSpec, message)
	}

	project, err := state.harbor.GetProject(ctx, state.projectID)
	if err != nil {
		return r.syncFailed(ctx, state, err)
	}

	want := desiredRetention(policy)

	if project.RetentionID == 0 {
		id, err := state.harbor.CreateRetention(ctx, state.projectID, want)
		if err != nil {
			return r.syncFailed(ctx, state, err)
		}

		recordCreated(r.Recorder, policy, "retention policy", policy.Name)

		return r.synced(ctx, state, id)
	}

	have, err := state.harbor.GetRetention(ctx, project.RetentionID)
	if err != nil {
		return r.syncFailed(ctx, state, err)
	}

	if have.ProjectID != state.projectID {
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, policy, metav1.ConditionFalse, harborv1alpha1.ReasonConflict,
			fmt.Sprintf("Harbor retention policy %d of the project is not scoped to project %d", project.RetentionID, state.projectID))
	}

	if !harbor.RetentionPolicyEqual(want, *have) {
		if err := state.harbor.UpdateRetention(ctx, have.ID, state.projectID, want); err != nil {
			return r.syncFailed(ctx, state, err)
		}

		recordUpdated(r.Recorder, policy, "retention policy", policy.Name,
			fieldIf(want.Schedule != have.Schedule, "schedule"), fieldIf(!slices.Equal(want.Rules, have.Rules), "rules"))
	}

	return r.synced(ctx, state, have.ID)
}

// synced records the retention policy's ID and marks the resource ready.
func (r *HarborRetentionPolicyReconciler) synced(ctx context.Context, state *retentionReconcile, id int64) (reconcile.Result, error) {
	state.policy.Status.RetentionID = id

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, state.policy, metav1.ConditionTrue, harborv1alpha1.ReasonReady,
		"Harbor retention policy matches the spec")
}

// finalize runs the deletion preamble: when the resource owns the project's retention policy and its deletion policy is
// Delete, it clears the policy's rules and schedule, then removes the finalizer.
func (r *HarborRetentionPolicyReconciler) finalize(ctx context.Context, policy *harborv1alpha1.HarborRetentionPolicy) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, RetentionFinalizer) {
		return reconcile.Result{}, nil
	}

	release := func() (reconcile.Result, error) {
		controllerutil.RemoveFinalizer(policy, RetentionFinalizer)

		return reconcile.Result{}, client.IgnoreNotFound(r.Update(ctx, policy))
	}

	if policy.Status.RetentionID == 0 {
		return release()
	}

	key := retentionProjectKey(policy)
	project := &harborv1alpha1.HarborProject{}

	if err := r.Get(ctx, key, project); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		// Without its project the policy cannot be reached through an admitted reference.
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
		recordOrphaned(r.Recorder, policy, "retention policy", policy.Name)

		return release()
	}

	admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, policy.Namespace)
	if err != nil {
		return reconcile.Result{}, err
	}

	if !admitted {
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"HarborProject %s does not admit namespace %q; the Harbor retention policy is left in place", key, policy.Namespace)

		return release()
	}

	if connectionErr != nil {
		// A connection that no longer exists admits nothing, so the project reference is no longer admitted.
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"the HarborConnection of HarborProject %s no longer exists; the Harbor retention policy is left in place", key)

		return release()
	}

	resync := retentionResync(policy, connection)

	identity := connection.Status.HarborIdentity
	if reason, message := connectionNotReadyForDeletion(connection); reason != "" {
		return reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, policy, metav1.ConditionFalse, reason, message)
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, policy, metav1.ConditionFalse, reason, message)
	}

	if policy.Status.HarborKey != ownership.RetentionKey(identity, projectName(project)) || policy.Status.ProjectID != project.Status.ProjectID {
		// The ID was recorded for another Harbor identity or project; it names nothing of this resource's here.
		return release()
	}

	// Another claimant may own the policy, for example when this resource is in Conflict; only the owner clears it.
	owner, err := ownership.RetentionOwner(ctx, r.Client, policy.Status.HarborKey, r.admitted)
	if err != nil {
		return reconcile.Result{}, err
	}

	if owner == nil || owner.UID != policy.UID {
		return release()
	}

	return r.clearRetention(ctx, policy, harborClient, release)
}

// clearRetention empties the retention policy this resource owns. Harbor keeps one retention policy per project, so
// the policy is emptied rather than deleted; a policy Harbor no longer has, or that now belongs to another scope, is
// left alone.
func (r *HarborRetentionPolicyReconciler) clearRetention(ctx context.Context, policy *harborv1alpha1.HarborRetentionPolicy, harborClient *harbor.Client,
	release func() (reconcile.Result, error),
) (reconcile.Result, error) {
	projectID := int64(policy.Status.ProjectID)

	found, err := harborClient.GetRetention(ctx, policy.Status.RetentionID)

	switch {
	case harbor.IsNotFound(err):
		return release()
	case err != nil:
		return r.harborFailed(ctx, policy, err)
	case found.ProjectID != projectID:
		// The ID now names a policy of another project, which this resource never managed.
		return release()
	}

	err = harborClient.UpdateRetention(ctx, found.ID, projectID, harbor.RetentionPolicy{Rules: []harbor.RetentionRule{}})
	if err != nil && !harbor.IsNotFound(err) {
		return r.harborFailed(ctx, policy, err)
	}

	recordDeleted(r.Recorder, policy, "retention policy", policy.Name)

	return release()
}

// syncFailed is harborFailed for a reconcile that reached Harbor through a ready connection: when Harbor is
// unavailable, it also asks for the connection to be checked again.
func (r *HarborRetentionPolicyReconciler) syncFailed(ctx context.Context, state *retentionReconcile, err error) (reconcile.Result, error) {
	reason, _ := harbor.MapError(err)
	r.Wakeups.wakeIfUnavailable(state.connection, reason)

	return r.harborFailed(ctx, state.policy, err)
}

// harborFailed reports a Harbor error. A spec Harbor rejects waits for a spec change; anything else retries with
// backoff.
func (r *HarborRetentionPolicyReconciler) harborFailed(ctx context.Context, policy *harborv1alpha1.HarborRetentionPolicy, err error) (reconcile.Result, error) {
	reason, retry := harbor.MapError(err)
	if statusErr := r.setReady(ctx, policy, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	if !retry {
		return reconcile.Result{}, nil
	}

	return reconcile.Result{}, err
}

func (r *HarborRetentionPolicyReconciler) setReferenceResolved(policy *harborv1alpha1.HarborRetentionPolicy, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReferenceResolved, Status: status, Reason: reason, Message: message, ObservedGeneration: policy.Generation,
	})
}

// setReady records the Ready condition and emits an event when it changes.
func (r *HarborRetentionPolicyReconciler) setReady(ctx context.Context, policy *harborv1alpha1.HarborRetentionPolicy, status metav1.ConditionStatus, reason, message string) error {
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
func (r *HarborRetentionPolicyReconciler) updateStatus(ctx context.Context, policy *harborv1alpha1.HarborRetentionPolicy) error {
	stored := &harborv1alpha1.HarborRetentionPolicy{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(policy), stored); err == nil &&
		stored.ResourceVersion == policy.ResourceVersion && apiequality.Semantic.DeepEqual(stored.Status, policy.Status) {
		return nil
	}

	return r.Status().Update(ctx, policy)
}
