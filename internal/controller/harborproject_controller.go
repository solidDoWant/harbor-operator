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
	// ProjectFinalizer keeps a HarborProject until its Harbor project has been dealt with.
	ProjectFinalizer = "harbor.soliddowant.dev/project"

	// claimantCacheDelay is how long to wait for the cache to show a status.harborKey just written.
	claimantCacheDelay = time.Second
)

// HarborProjectReconciler makes a Harbor project match its HarborProject.
type HarborProjectReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
	// Recorder emits events on the resource.
	Recorder events.EventRecorder
	// WatchNamespaces lists the namespaces the operator watches; empty means all. A reference into any other
	// namespace is not admitted.
	WatchNamespaces access.WatchedNamespaces
	// MaxBackoff caps the retry delay of a failing reconcile; tests lower it. Zero means 5 minutes.
	MaxBackoff time.Duration
	// Wakeups receives the connection when a Harbor call fails as unavailable. Nil sends nothing.
	Wakeups ConnectionWakeups
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects/finalizers,verbs=update
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojectrobots,verbs=get;list;watch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborregistries,verbs=get;list;watch

// SetupWithManager registers the reconciler with its watches: connections, namespaces, the other claimants of the
// same Harbor project, the robots and immutability policies that reference a project and the registries
// proxy-cache projects use.
func (r *HarborProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborProject{}).
		Watches(&harborv1alpha1.HarborConnection{}, handler.EnqueueRequestsFromMapFunc(r.projectsForConnection)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.projectsInNamespace)).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(r.otherClaimants)).
		Watches(&harborv1alpha1.HarborProjectRobot{}, handler.EnqueueRequestsFromMapFunc(r.projectForRobot)).
		Watches(&harborv1alpha1.HarborRetentionPolicy{}, handler.EnqueueRequestsFromMapFunc(r.projectForRetention)).
		Watches(&harborv1alpha1.HarborImmutabilityPolicy{}, handler.EnqueueRequestsFromMapFunc(r.projectForImmutabilityPolicy)).
		Watches(&harborv1alpha1.HarborRegistry{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			return r.projectRequests(ctx, client.MatchingFields{ProjectRegistryIndex: client.ObjectKeyFromObject(object).String()})
		})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4, RateLimiter: backoffLimiter(r.MaxBackoff)}).
		Named("harborproject").
		Complete(r)
}

func (r *HarborProjectReconciler) projectsForConnection(ctx context.Context, object client.Object) []reconcile.Request {
	return r.projectRequests(ctx, client.MatchingFields{ProjectConnectionIndex: client.ObjectKeyFromObject(object).String()})
}

func (r *HarborProjectReconciler) projectsInNamespace(ctx context.Context, object client.Object) []reconcile.Request {
	return r.projectRequests(ctx, client.InNamespace(object.GetName()))
}

func (r *HarborProjectReconciler) otherClaimants(ctx context.Context, object client.Object) []reconcile.Request {
	key := object.(*harborv1alpha1.HarborProject).Status.HarborKey
	if key == "" {
		return nil
	}

	requests := r.projectRequests(ctx, client.MatchingFields{ownership.HarborKeyField: key})

	self := client.ObjectKeyFromObject(object)

	others := requests[:0]
	for _, request := range requests {
		if request.NamespacedName != self {
			others = append(others, request)
		}
	}

	return others
}

func (r *HarborProjectReconciler) projectRequests(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	var projects harborv1alpha1.HarborProjectList
	if err := r.List(ctx, &projects, options...); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(projects.Items))
	for index := range projects.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&projects.Items[index])})
	}

	return requests
}

// projectName is the project's name in Harbor.
func projectName(project *harborv1alpha1.HarborProject) string {
	if project.Spec.Name != "" {
		return project.Spec.Name
	}

	return project.Name
}

// projectReconcile carries what one reconcile has resolved.
type projectReconcile struct {
	project    *harborv1alpha1.HarborProject
	connection *harborv1alpha1.HarborConnection
	harbor     *harbor.Client
	resync     time.Duration
	// registryID is the Harbor ID of a proxy-cache project's upstream registry, or 0 when the project is not a proxy
	// cache or its registry resource is not usable now.
	registryID int64
	// justCreated is set once this reconcile created the Harbor project, so the settings written right after
	// are not reported as drift.
	justCreated bool
}

// Reconcile makes the Harbor project match the resource and records the result in status.
func (r *HarborProjectReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	project := &harborv1alpha1.HarborProject{}
	if err := r.Get(ctx, request.NamespacedName, project); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !project.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, project)
	}

	if controllerutil.AddFinalizer(project, ProjectFinalizer) {
		if err := r.Update(ctx, project); err != nil {
			return reconcile.Result{}, err
		}
	}

	state, result, err := r.resolve(ctx, project)
	if state == nil {
		return result, err
	}

	return r.sync(ctx, state)
}

// resolve finds and admits the connection, gets a Harbor client and checks ownership. It returns nil state when the
// reconcile ends here, with the result to return.
func (r *HarborProjectReconciler) resolve(ctx context.Context, project *harborv1alpha1.HarborProject) (*projectReconcile, reconcile.Result, error) {
	resync := defaultResyncInterval
	if project.Spec.ResyncInterval != nil && project.Spec.ResyncInterval.Duration > 0 {
		resync = project.Spec.ResyncInterval.Duration
	}

	stop := func(reason, message string) (*projectReconcile, reconcile.Result, error) {
		return nil, reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, project, metav1.ConditionFalse, reason, message)
	}

	key := connectionKey(project)
	connection := &harborv1alpha1.HarborConnection{}

	if !r.WatchNamespaces.Watches(key.Namespace) {
		message := access.NotWatchedMessage(key.Namespace)
		r.setReferenceResolved(project, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	if err := r.Get(ctx, key, connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}

		r.setReferenceResolved(project, metav1.ConditionFalse, harborv1alpha1.ReasonNotFound, fmt.Sprintf("HarborConnection %s not found", key))

		return stop(harborv1alpha1.ReasonNotFound, fmt.Sprintf("HarborConnection %s not found", key))
	}

	if project.Spec.ResyncInterval == nil {
		resync = resyncInterval(connection)
	}

	admitted, err := access.Admits(ctx, r.Client, connection.Spec.AllowedNamespaces, connection.Namespace, project.Namespace)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if !admitted {
		message := fmt.Sprintf("HarborConnection %s does not admit namespace %q", key, project.Namespace)
		r.setReferenceResolved(project, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	r.setReferenceResolved(project, metav1.ConditionTrue, harborv1alpha1.ReasonReferenceResolved, "HarborConnection "+key.String()+" admits the namespace")

	identity := connection.Status.HarborIdentity
	if ready := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionTrue || identity == "" {
		reason := harborv1alpha1.ReasonHarborUnavailable
		if ready != nil && ready.Status == metav1.ConditionFalse {
			reason = ready.Reason
		}

		return stop(reason, fmt.Sprintf("HarborConnection %s is not ready", key))
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return stop(reason, message)
	}

	harborKey := ownership.ProjectKey(identity, projectName(project))
	if project.Status.HarborKey != harborKey {
		// An ID recorded for another Harbor identity names nothing in this one.
		project.Status.ProjectID = 0
		project.Status.HarborKey = harborKey

		if err := r.updateStatus(ctx, project); err != nil {
			return nil, reconcile.Result{}, err
		}
	}

	owner, err := ownership.ProjectOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if owner == nil {
		// The cache does not show this resource's claim yet.
		return nil, reconcile.Result{RequeueAfter: claimantCacheDelay}, nil
	}

	if owner.UID != project.UID {
		return stop(harborv1alpha1.ReasonConflict, fmt.Sprintf("Harbor project %q is owned by HarborProject %s/%s", projectName(project), owner.Namespace, owner.Name))
	}

	state := &projectReconcile{project: project, connection: connection, harbor: harborClient, resync: resync}

	if project.Spec.ProxyCache != nil {
		registryID, reason, message, err := r.resolveRegistry(ctx, project, identity)
		if err != nil {
			return nil, reconcile.Result{}, err
		}

		if reason != "" {
			r.setReferenceResolved(project, metav1.ConditionFalse, reason, message)

			return stop(reason, message)
		}

		state.registryID = registryID
	}

	return state, reconcile.Result{}, nil
}

// resolveRegistry checks a proxy-cache project's registry on every reconcile. It returns a reason and message when the
// reconcile must stop: the registry does not admit the project's namespace, or it is in another Harbor. Otherwise it
// returns the registry's Harbor ID, or 0 when the registry resource is missing, has no ID yet, or is in Conflict or
// NotAllowed; creating or adopting the project then waits for it.
func (r *HarborProjectReconciler) resolveRegistry(ctx context.Context, project *harborv1alpha1.HarborProject, identity string) (int64, string, string, error) {
	key := projectRegistryKey(project)
	registry := &harborv1alpha1.HarborRegistry{}

	if !r.WatchNamespaces.Watches(key.Namespace) {
		return 0, harborv1alpha1.ReasonNotAllowed, access.NotWatchedMessage(key.Namespace), nil
	}

	if err := r.Get(ctx, key, registry); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, "", "", nil
		}

		return 0, "", "", err
	}

	admitted, err := access.Admits(ctx, r.Client, registry.Spec.AllowedNamespaces, registry.Namespace, project.Namespace)
	if err != nil {
		return 0, "", "", err
	}

	if !admitted {
		return 0, harborv1alpha1.ReasonNotAllowed, fmt.Sprintf("HarborRegistry %s does not admit namespace %q", key, project.Namespace), nil
	}

	if registry.Status.HarborKey != "" && registry.Status.HarborKey != ownership.RegistryKey(identity, registryName(registry)) {
		return 0, harborv1alpha1.ReasonRegistryOnOtherHarbor, fmt.Sprintf("HarborRegistry %s is in another Harbor than the project's connection", key), nil
	}

	if ready := meta.FindStatusCondition(registry.Status.Conditions, harborv1alpha1.ConditionReady); ready != nil &&
		(ready.Reason == harborv1alpha1.ReasonConflict || ready.Reason == harborv1alpha1.ReasonNotAllowed) {
		return 0, "", "", nil
	}

	if registry.Status.HarborKey == "" {
		return 0, "", "", nil
	}

	return registry.Status.RegistryID, "", "", nil
}

// registryNotReady stops a proxy-cache project's create or adoption while its registry is not usable.
func (r *HarborProjectReconciler) registryNotReady(ctx context.Context, state *projectReconcile) (reconcile.Result, error) {
	project := state.project
	message := fmt.Sprintf("HarborRegistry %s does not exist, has no Harbor registry yet, or is in Conflict or NotAllowed", projectRegistryKey(project))
	r.setReferenceResolved(project, metav1.ConditionFalse, harborv1alpha1.ReasonRegistryNotReady, message)

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, project, metav1.ConditionFalse, harborv1alpha1.ReasonRegistryNotReady, message)
}

// admitted reports whether a claimant's connection admits the claimant's namespace.
func (r *HarborProjectReconciler) admitted(ctx context.Context, claimant *harborv1alpha1.HarborProject) (bool, error) {
	if !r.WatchNamespaces.Watches(connectionKey(claimant).Namespace) {
		return false, nil
	}

	connection := &harborv1alpha1.HarborConnection{}
	if err := r.Get(ctx, connectionKey(claimant), connection); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return access.Admits(ctx, r.Client, connection.Spec.AllowedNamespaces, connection.Namespace, claimant.Namespace)
}

// sync finds or creates the Harbor project, then makes its settings match the spec.
func (r *HarborProjectReconciler) sync(ctx context.Context, state *projectReconcile) (reconcile.Result, error) {
	project := state.project
	name := projectName(project)
	recreate := false

	if err := removeLegacyCreatePending(ctx, r.Client, project); err != nil {
		return reconcile.Result{}, err
	}

	if project.Status.ProjectID != 0 {
		found, err := state.harbor.GetProject(ctx, int64(project.Status.ProjectID))

		switch {
		case harbor.IsNotFound(err):
			recreate = true
		case err != nil:
			return r.harborFailed(ctx, state, err)
		case found.Name != name:
			// The ID now names another project; it is not this resource's.
		default:
			return r.update(ctx, state, found)
		}

		project.Status.ProjectID = 0
		if err := r.updateStatus(ctx, project); err != nil {
			return reconcile.Result{}, err
		}
	}

	if project.Spec.ProxyCache != nil && state.registryID == 0 {
		return r.registryNotReady(ctx, state)
	}

	found, err := state.harbor.GetProjectByName(ctx, name)

	switch {
	case harbor.IsNotFound(err):
		return r.create(ctx, state, recreate)
	case err != nil:
		return r.harborFailed(ctx, state, err)
	}

	ownCreate := isOwnCreate(project.Status.CreatePending, found.CreationTime)

	switch {
	case ownCreate:
		// The project was created by this resource just before the reconcile was interrupted.
	case !project.Spec.Adopt:
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, project, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists,
			fmt.Sprintf("Harbor project %q already exists; set adopt: true to take it over", name))
	case !state.connection.Spec.AllowAdoption:
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, project, metav1.ConditionFalse, harborv1alpha1.ReasonAdoptionNotAllowed,
			fmt.Sprintf("HarborConnection %s does not allow adoption", client.ObjectKeyFromObject(state.connection)))
	}

	// Harbor cannot turn a project into a proxy cache or back, so a project in the other mode is never taken over.
	if (found.RegistryID != 0) != (project.Spec.ProxyCache != nil) {
		return reconcile.Result{}, r.setReady(ctx, project, metav1.ConditionFalse, harborv1alpha1.ReasonInvalidSpec,
			fmt.Sprintf("Harbor project %q: proxy-cache mode can't be changed", name))
	}

	if err := r.recordID(ctx, project, found.ID); err != nil {
		return reconcile.Result{}, err
	}

	if !ownCreate {
		r.Recorder.Eventf(project, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonAdopted, "Adopt", "Adopted the existing Harbor project %q", name)
	}

	return r.update(ctx, state, found)
}

// create creates the Harbor project, marking the resource first so that an interrupted create is recognised as its own.
func (r *HarborProjectReconciler) create(ctx context.Context, state *projectReconcile, recreate bool) (reconcile.Result, error) {
	project := state.project
	name := projectName(project)

	if project.Status.CreatePending == nil {
		project.Status.CreatePending = &metav1.Time{Time: time.Now().UTC().Truncate(time.Second)}

		if err := r.updateStatus(ctx, project); err != nil {
			return reconcile.Result{}, err
		}
	}

	var storageLimit *int64
	if limit, ok := desiredStorageQuota(&project.Spec); ok {
		storageLimit = &limit
	}

	created, err := state.harbor.CreateProxyCacheProject(ctx, name, desiredProjectSettings(&project.Spec), storageLimit, state.registryID)
	if err != nil {
		// Harbor refused, so nothing was created: a project made later by someone else must not pass for this one.
		if harbor.IsRefused(err) {
			project.Status.CreatePending = nil
		}

		return r.harborFailed(ctx, state, err)
	}

	if err := r.recordID(ctx, project, created.ID); err != nil {
		return reconcile.Result{}, err
	}

	if recreate {
		r.Recorder.Eventf(project, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonRecreated, "Create", "Created Harbor project %q again after it was deleted in Harbor", name)
	} else {
		recordCreated(r.Recorder, project, "project", name)
	}

	state.justCreated = true

	return r.update(ctx, state, created)
}

// recordID writes the project ID to status and clears the create marker.
func (r *HarborProjectReconciler) recordID(ctx context.Context, project *harborv1alpha1.HarborProject, id int64) error {
	project.Status.ProjectID = int32(id) //nolint:gosec // Harbor project IDs are 32-bit
	project.Status.HarborName = projectName(project)
	project.Status.CreatePending = nil

	return r.updateStatus(ctx, project)
}

// update writes the spec's fields that differ in Harbor, and only those.
func (r *HarborProjectReconciler) update(ctx context.Context, state *projectReconcile, current *harbor.Project) (reconcile.Result, error) {
	project := state.project

	var fields []string

	defer func() {
		if !state.justCreated {
			recordUpdated(r.Recorder, project, "project", current.Name, fields...)
		}
	}()

	// A proxy-cache project follows its registry resource, which may have been re-created with a new ID.
	if project.Spec.ProxyCache != nil && state.registryID != 0 && current.RegistryID != state.registryID {
		if err := state.harbor.SetProjectRegistry(ctx, current.Name, state.registryID); err != nil {
			return r.harborFailed(ctx, state, err)
		}

		// Harbor answers this PUT with success but may keep the old registry. Read it back, and write nothing else
		// while the project still points elsewhere.
		after, err := state.harbor.GetProject(ctx, current.ID)
		if err != nil {
			return r.harborFailed(ctx, state, err)
		}

		if after.RegistryID != state.registryID {
			message := fmt.Sprintf("Harbor can't change the proxy-cache registry of an existing project (it keeps registry ID %d; the registry resource has ID %d)", after.RegistryID, state.registryID)

			return reconcile.Result{}, r.setReady(ctx, project, metav1.ConditionFalse, harborv1alpha1.ReasonInvalidSpec, message)
		}

		fields = append(fields, "proxyCache.registryRef")
		current = after
	}

	if diff, changed := projectSettingsDiff(desiredProjectSettings(&project.Spec), current.Settings); changed {
		if err := state.harbor.UpdateProjectMetadata(ctx, current.Name, diff); err != nil {
			return r.harborFailed(ctx, state, err)
		}

		fields = append(fields, projectSettingsFields(diff)...)
	}

	if limit, ok := desiredStorageQuota(&project.Spec); ok {
		have, err := state.harbor.GetProjectStorageQuota(ctx, current.ID)
		if err != nil {
			return r.harborFailed(ctx, state, err)
		}

		if have != limit {
			if err := state.harbor.SetProjectStorageQuota(ctx, current.ID, limit); err != nil {
				return r.harborFailed(ctx, state, err)
			}

			fields = append(fields, "storageQuota")
		}
	}

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, project, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor project matches the spec")
}

// harborFailed reports a Harbor error. A spec Harbor rejects waits for a spec change; anything else retries with
// backoff.
func (r *HarborProjectReconciler) harborFailed(ctx context.Context, state *projectReconcile, err error) (reconcile.Result, error) {
	reason, retry := harbor.MapError(err)
	if state.connection != nil {
		r.Wakeups.wakeIfUnavailable(client.ObjectKeyFromObject(state.connection), reason)
	}

	if statusErr := r.setReady(ctx, state.project, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	if !retry {
		return reconcile.Result{}, nil
	}

	return reconcile.Result{}, err
}

func (r *HarborProjectReconciler) setReferenceResolved(project *harborv1alpha1.HarborProject, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReferenceResolved, Status: status, Reason: reason, Message: message, ObservedGeneration: project.Generation,
	})
}

// setReady records the Ready condition and emits an event when it changes.
func (r *HarborProjectReconciler) setReady(ctx context.Context, project *harborv1alpha1.HarborProject, status metav1.ConditionStatus, reason, message string) error {
	previous := meta.FindStatusCondition(project.Status.Conditions, harborv1alpha1.ConditionReady)
	changed := previous == nil || previous.Status != status || previous.Reason != reason || previous.Message != message

	project.Status.ObservedGeneration = project.Generation
	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: project.Generation,
	})

	if err := r.updateStatus(ctx, project); err != nil {
		return err
	}

	if changed {
		eventType := corev1.EventTypeNormal
		if status != metav1.ConditionTrue {
			eventType = corev1.EventTypeWarning
		}

		r.Recorder.Eventf(project, nil, eventType, reason, "Reconcile", "%s", message)
	}

	return nil
}

// updateStatus writes status only when it differs from what the API server has.
func (r *HarborProjectReconciler) updateStatus(ctx context.Context, project *harborv1alpha1.HarborProject) error {
	stored := &harborv1alpha1.HarborProject{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(project), stored); err == nil &&
		stored.ResourceVersion == project.ResourceVersion && apiequality.Semantic.DeepEqual(stored.Status, project.Status) {
		return nil
	}

	return r.Status().Update(ctx, project)
}
