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
	// MaxBackoff caps the retry delay of a failing reconcile. Zero means 10 minutes.
	MaxBackoff time.Duration
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects/finalizers,verbs=update
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojectrobots,verbs=get;list;watch

// SetupWithManager registers the reconciler with its watches: connections, namespaces, the other claimants of the
// same Harbor project and the robots that reference a project.
func (r *HarborProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborProject{}).
		Watches(&harborv1alpha1.HarborConnection{}, handler.EnqueueRequestsFromMapFunc(r.projectsForConnection)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.projectsInNamespace)).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(r.otherClaimants)).
		Watches(&harborv1alpha1.HarborProjectRobot{}, handler.EnqueueRequestsFromMapFunc(r.projectForRobot)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4, RateLimiter: r.rateLimiter()}).
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

	return &projectReconcile{project: project, connection: connection, harbor: harborClient, resync: resync}, reconcile.Result{}, nil
}

// admitted reports whether a claimant's connection admits the claimant's namespace.
func (r *HarborProjectReconciler) admitted(ctx context.Context, claimant *harborv1alpha1.HarborProject) (bool, error) {
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

	found, err := state.harbor.GetProjectByName(ctx, name)

	switch {
	case harbor.IsNotFound(err):
		return r.create(ctx, state, recreate)
	case err != nil:
		return r.harborFailed(ctx, state, err)
	}

	_, ownCreate := project.Annotations[harborv1alpha1.AnnotationCreatePending]

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

	if _, ok := project.Annotations[harborv1alpha1.AnnotationCreatePending]; !ok {
		metav1.SetMetaDataAnnotation(&project.ObjectMeta, harborv1alpha1.AnnotationCreatePending, time.Now().UTC().Format(time.RFC3339))

		if err := r.Update(ctx, project); err != nil {
			return reconcile.Result{}, err
		}
	}

	var storageLimit *int64
	if limit, ok := desiredStorageQuota(&project.Spec); ok {
		storageLimit = &limit
	}

	created, err := state.harbor.CreateProject(ctx, name, desiredProjectSettings(&project.Spec), storageLimit)
	if err != nil {
		return r.harborFailed(ctx, state, err)
	}

	if err := r.recordID(ctx, project, created.ID); err != nil {
		return reconcile.Result{}, err
	}

	if recreate {
		r.Recorder.Eventf(project, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonRecreated, "Create", "Created Harbor project %q again after it was deleted in Harbor", name)
	}

	return r.update(ctx, state, created)
}

// recordID writes the project ID to status, then clears the create-pending annotation.
func (r *HarborProjectReconciler) recordID(ctx context.Context, project *harborv1alpha1.HarborProject, id int64) error {
	project.Status.ProjectID = int32(id) //nolint:gosec // Harbor project IDs are 32-bit
	project.Status.HarborName = projectName(project)

	if err := r.updateStatus(ctx, project); err != nil {
		return err
	}

	if _, ok := project.Annotations[harborv1alpha1.AnnotationCreatePending]; !ok {
		return nil
	}

	delete(project.Annotations, harborv1alpha1.AnnotationCreatePending)

	return r.Update(ctx, project)
}

// update writes the spec's fields that differ in Harbor, and only those.
func (r *HarborProjectReconciler) update(ctx context.Context, state *projectReconcile, current *harbor.Project) (reconcile.Result, error) {
	project := state.project

	if diff, changed := projectSettingsDiff(desiredProjectSettings(&project.Spec), current.Settings); changed {
		if err := state.harbor.UpdateProjectMetadata(ctx, current.Name, diff); err != nil {
			return r.harborFailed(ctx, state, err)
		}
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
		}
	}

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, project, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor project matches the spec")
}

// harborFailed reports a Harbor error. A spec Harbor rejects waits for a spec change; anything else retries with
// backoff.
func (r *HarborProjectReconciler) harborFailed(ctx context.Context, state *projectReconcile, err error) (reconcile.Result, error) {
	reason, retry := harbor.MapError(err)
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
