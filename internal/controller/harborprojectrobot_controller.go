package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
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

// HarborProjectRobotReconciler makes a Harbor project robot match its HarborProjectRobot and writes its credentials
// into Secrets.
type HarborProjectRobotReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
	// Recorder emits events on the resource.
	Recorder events.EventRecorder
	// Wakeups receives the connection when a Harbor call fails as unavailable. Nil sends nothing.
	Wakeups ConnectionWakeups
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojectrobots,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojectrobots/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojectrobots/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager registers the reconciler with its watches: projects, namespaces, owned Secrets and the other
// claimants of the same Harbor robot.
func (r *HarborProjectRobotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborProjectRobot{}).
		Owns(&corev1.Secret{}).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(r.robotsForProject)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.robotsInNamespace)).
		Watches(&harborv1alpha1.HarborProjectRobot{}, handler.EnqueueRequestsFromMapFunc(r.otherClaimants)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4, RateLimiter: backoffLimiter(0)}).
		Named("harborprojectrobot").
		Complete(r)
}

func (r *HarborProjectRobotReconciler) robotsForProject(ctx context.Context, object client.Object) []reconcile.Request {
	return r.robotRequests(ctx, client.MatchingFields{RobotProjectIndex: client.ObjectKeyFromObject(object).String()})
}

// robotsInNamespace re-queues every robot when a namespace's labels may change which projects admit which robots.
func (r *HarborProjectRobotReconciler) robotsInNamespace(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.robotRequests(ctx)
}

func (r *HarborProjectRobotReconciler) otherClaimants(ctx context.Context, object client.Object) []reconcile.Request {
	key := object.(*harborv1alpha1.HarborProjectRobot).Status.HarborKey
	if key == "" {
		return nil
	}

	self := client.ObjectKeyFromObject(object)
	requests := r.robotRequests(ctx, client.MatchingFields{ownership.HarborKeyField: key})

	others := requests[:0]
	for _, request := range requests {
		if request.NamespacedName != self {
			others = append(others, request)
		}
	}

	return others
}

func (r *HarborProjectRobotReconciler) robotRequests(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	var robots harborv1alpha1.HarborProjectRobotList
	if err := r.List(ctx, &robots, options...); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(robots.Items))
	for index := range robots.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&robots.Items[index])})
	}

	return requests
}

// robotName is the robot's short name in Harbor, without Harbor's prefix.
func robotName(robot *harborv1alpha1.HarborProjectRobot) string {
	if robot.Spec.Name != "" {
		return robot.Spec.Name
	}

	return robot.Name
}

// desiredRobot is the Harbor robot the spec asks for.
func desiredRobot(robot *harborv1alpha1.HarborProjectRobot) harbor.RobotSpec {
	duration := robot.Spec.Duration
	if duration == 0 {
		duration = -1
	}

	permissions := make([]harbor.RobotAccess, 0, len(robot.Spec.Permissions))
	for _, permission := range robot.Spec.Permissions {
		permissions = append(permissions, harbor.RobotAccess{Resource: permission.Resource, Action: permission.Action})
	}

	return harbor.RobotSpec{
		Name:        robotName(robot),
		Description: robot.Spec.Description,
		Duration:    duration,
		Disabled:    robot.Spec.Disabled,
		Permissions: permissions,
	}
}

// projectRobot is a HarborProjectRobot as the shared robot lifecycle sees it.
type projectRobot struct {
	robot *harborv1alpha1.HarborProjectRobot
}

func (p projectRobot) object() client.Object                            { return p.robot }
func (p projectRobot) status() *harborv1alpha1.HarborProjectRobotStatus { return &p.robot.Status }
func (p projectRobot) secretSpec() harborv1alpha1.RobotSecretSpec       { return p.robot.Spec.Secret }
func (p projectRobot) adopt() bool                                      { return p.robot.Spec.Adopt }
func (p projectRobot) deletionPolicy() harborv1alpha1.DeletionPolicy {
	return p.robot.Spec.DeletionPolicy
}
func (p projectRobot) resyncInterval() *metav1.Duration { return p.robot.Spec.ResyncInterval }
func (p projectRobot) shortName() string                { return robotName(p.robot) }
func (p projectRobot) desired() harbor.RobotSpec        { return desiredRobot(p.robot) }
func (p projectRobot) empty() robotResource {
	return projectRobot{robot: &harborv1alpha1.HarborProjectRobot{}}
}

// projectRobotTarget is the Harbor project a project robot lives in.
type projectRobotTarget struct {
	projectID   int64
	projectName string
}

func (t projectRobotTarget) find(ctx context.Context, harborClient *harbor.Client, name string) (*harbor.Robot, error) {
	return harborClient.FindProjectRobot(ctx, t.projectID, name)
}

func (t projectRobotTarget) create(ctx context.Context, harborClient *harbor.Client, spec harbor.RobotSpec) (*harbor.Robot, error) {
	return harborClient.CreateRobot(ctx, t.projectName, spec)
}

func (t projectRobotTarget) update(ctx context.Context, harborClient *harbor.Client, id int64, spec harbor.RobotSpec) error {
	return harborClient.UpdateRobot(ctx, id, t.projectName, spec)
}

// isOwn reports whether the Harbor robot has the short name in the same project.
func (t projectRobotTarget) isOwn(found *harbor.Robot, name string) bool {
	return strings.HasSuffix(found.Name, "+"+name) && found.ProjectName == t.projectName
}

func (t projectRobotTarget) missingPermissions(ctx context.Context, harborClient *harbor.Client, spec harbor.RobotSpec) ([]string, error) {
	missing, err := harborClient.MissingPermissions(ctx, harbor.ProjectScope(t.projectID), spec.Permissions)
	if err != nil {
		return nil, err
	}

	return accessNames(missing, ""), nil
}

// accessNames lists permissions as "resource action", each after prefix.
func accessNames(permissions []harbor.RobotAccess, prefix string) []string {
	names := make([]string, 0, len(permissions))
	for _, access := range permissions {
		names = append(names, prefix+access.String())
	}

	return names
}

func (t projectRobotTarget) location() string { return fmt.Sprintf("in project %q", t.projectName) }

func newProjectRobotTarget(project *harborv1alpha1.HarborProject) projectRobotTarget {
	return projectRobotTarget{projectID: int64(project.Status.ProjectID), projectName: projectName(project)}
}

func (r *HarborProjectRobotReconciler) lifecycle() *robotLifecycle {
	return &robotLifecycle{Client: r.Client, Recorder: r.Recorder, Wakeups: r.Wakeups}
}

// Reconcile makes the Harbor robot match the resource, writes its Secrets and records the result in status.
func (r *HarborProjectRobotReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	robot := &harborv1alpha1.HarborProjectRobot{}
	if err := r.Get(ctx, request.NamespacedName, robot); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !robot.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, robot)
	}

	if controllerutil.AddFinalizer(robot, RobotFinalizer) {
		if err := r.Update(ctx, robot); err != nil {
			return reconcile.Result{}, err
		}
	}

	state, result, err := r.resolve(ctx, robot)
	if state == nil {
		return result, err
	}

	return r.lifecycle().sync(ctx, state)
}

// projectUsable reports whether the project has a Harbor project robots can use. The project's Ready condition is not
// required: an unrelated rejected setting on the project must not stop its robots.
func projectUsable(project *harborv1alpha1.HarborProject) bool {
	if project.Status.ProjectID == 0 {
		return false
	}

	ready := meta.FindStatusCondition(project.Status.Conditions, harborv1alpha1.ConditionReady)

	return ready == nil || (ready.Reason != harborv1alpha1.ReasonConflict && ready.Reason != harborv1alpha1.ReasonNotAllowed)
}

// resolve finds the project and checks that it admits the robot, gets a Harbor client and checks ownership. It returns
// nil state when the reconcile ends here, with the result to return.
func (r *HarborProjectRobotReconciler) resolve(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) (*robotReconcile, reconcile.Result, error) {
	resource := projectRobot{robot: robot}
	lifecycle := r.lifecycle()
	resync := robotResync(resource, nil)

	stop := func(reason, message string) (*robotReconcile, reconcile.Result, error) {
		return nil, reconcile.Result{RequeueAfter: resync}, lifecycle.setReady(ctx, resource, metav1.ConditionFalse, reason, message)
	}

	key := robotProjectKey(robot)
	project := &harborv1alpha1.HarborProject{}

	if err := r.Get(ctx, key, project); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}

		message := fmt.Sprintf("HarborProject %s not found", key)
		setReferenceResolved(resource, metav1.ConditionFalse, harborv1alpha1.ReasonNotFound, message)

		return stop(harborv1alpha1.ReasonProjectNotReady, message)
	}

	admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, robot.Namespace)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if !admitted {
		message := fmt.Sprintf("HarborProject %s does not admit namespace %q", key, robot.Namespace)
		setReferenceResolved(resource, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	setReferenceResolved(resource, metav1.ConditionTrue, harborv1alpha1.ReasonReferenceResolved, "HarborProject "+key.String()+" admits the namespace")

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

	resync = robotResync(resource, connection)

	if reason, message := connectionNotReady(connection); reason != "" {
		return stop(reason, message)
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return stop(reason, message)
	}

	harborKey := ownership.RobotKey(connection.Status.HarborIdentity, projectName(project), robotName(robot))
	if robot.Status.HarborKey != harborKey {
		// An ID recorded for another Harbor identity or project names nothing here.
		robot.Status.RobotID = 0
		robot.Status.HarborKey = harborKey

		if err := lifecycle.updateStatus(ctx, resource); err != nil {
			return nil, reconcile.Result{}, err
		}
	}

	owner, err := ownership.RobotOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if owner == nil {
		// The cache does not show this resource's claim yet.
		return nil, reconcile.Result{RequeueAfter: claimantCacheDelay}, nil
	}

	if owner.UID != robot.UID {
		return stop(harborv1alpha1.ReasonConflict, fmt.Sprintf("Harbor robot %q is owned by HarborProjectRobot %s/%s", robotName(robot), owner.Namespace, owner.Name))
	}

	return &robotReconcile{
		robot: resource, target: newProjectRobotTarget(project), connection: connection, harbor: harborClient, resync: resync,
	}, reconcile.Result{}, nil
}

// admitted reports whether a claimant's project admits the claimant's namespace.
func (r *HarborProjectRobotReconciler) admitted(ctx context.Context, claimant *harborv1alpha1.HarborProjectRobot) (bool, error) {
	project := &harborv1alpha1.HarborProject{}
	if err := r.Get(ctx, robotProjectKey(claimant), project); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, claimant.Namespace)
}

// finalize runs the deletion preamble: it deletes the Harbor robot when the resource owns it and its policy is Delete,
// deletes the robot's Secrets either way and then removes the finalizer.
func (r *HarborProjectRobotReconciler) finalize(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(robot, RobotFinalizer) {
		return reconcile.Result{}, nil
	}

	resource := projectRobot{robot: robot}
	lifecycle := r.lifecycle()

	if robot.Status.RobotID == 0 {
		return lifecycle.release(ctx, resource)
	}

	key := robotProjectKey(robot)
	project := &harborv1alpha1.HarborProject{}

	if err := r.Get(ctx, key, project); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		// Without its project the robot cannot be reached through an admitted reference.
		return lifecycle.release(ctx, resource)
	}

	connection := &harborv1alpha1.HarborConnection{}
	connectionErr := r.Get(ctx, connectionKey(project), connection)

	if connectionErr != nil && !apierrors.IsNotFound(connectionErr) {
		return reconcile.Result{}, connectionErr
	}

	knownConnection := connection
	if connectionErr != nil {
		knownConnection = nil
	}

	if robotDeletionPolicy(resource, knownConnection) == harborv1alpha1.DeletionPolicyOrphan {
		recordOrphaned(lifecycle.Recorder, robot, "robot", resource.shortName())

		return lifecycle.release(ctx, resource)
	}

	admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, robot.Namespace)
	if err != nil {
		return reconcile.Result{}, err
	}

	if !admitted {
		r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"HarborProject %s does not admit namespace %q; the Harbor robot is left in place", key, robot.Namespace)

		return lifecycle.release(ctx, resource)
	}

	if connectionErr != nil {
		// A connection that no longer exists admits nothing, so the project reference is no longer admitted.
		r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"the HarborConnection of HarborProject %s no longer exists; the Harbor robot is left in place", key)

		return lifecycle.release(ctx, resource)
	}

	resync := robotResync(resource, connection)

	if reason, message := connectionNotReadyForDeletion(connection); reason != "" {
		return reconcile.Result{RequeueAfter: resync}, lifecycle.setReady(ctx, resource, metav1.ConditionFalse, reason, message)
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return reconcile.Result{RequeueAfter: resync}, lifecycle.setReady(ctx, resource, metav1.ConditionFalse, reason, message)
	}

	harborKey := ownership.RobotKey(connection.Status.HarborIdentity, projectName(project), robotName(robot))
	if robot.Status.HarborKey != harborKey {
		// The ID was recorded for another Harbor identity or project; it names nothing of this resource's here.
		return lifecycle.release(ctx, resource)
	}

	// Another claimant may own the robot this ID names, for example when this resource is in Conflict after adopting
	// it; only the owner deletes it.
	owner, err := ownership.RobotOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return reconcile.Result{}, err
	}

	if owner == nil || owner.UID != robot.UID {
		return lifecycle.release(ctx, resource)
	}

	return lifecycle.deleteHarborRobot(ctx, &robotReconcile{
		robot: resource, target: newProjectRobotTarget(project), connection: connection, harbor: harborClient, resync: resync,
	})
}
