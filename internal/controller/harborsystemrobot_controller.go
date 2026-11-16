package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
)

// SystemRobotConnectionIndex finds the system robots that use a connection. Its value is "namespace/name".
const SystemRobotConnectionIndex = "spec.connectionRef"

// allProjects is the project name that grants access in every project.
const allProjects = "*"

// HarborSystemRobotReconciler makes a Harbor system robot match its HarborSystemRobot and writes its credentials into
// Secrets.
type HarborSystemRobotReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
	// Recorder emits events on the resource.
	Recorder events.EventRecorder
	// Wakeups receives the connection when a Harbor call fails as unavailable. Nil sends nothing.
	Wakeups ConnectionWakeups
	// WatchNamespaces lists the namespaces the operator watches; empty means all. A reference into any other
	// namespace is not admitted.
	WatchNamespaces access.WatchedNamespaces
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborsystemrobots,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborsystemrobots/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborsystemrobots/finalizers,verbs=update
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborconnections,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager registers the reconciler with its watches: connections, newly created projects (a missing project
// may appear), namespaces, owned Secrets and the other claimants of the same Harbor robot.
func (r *HarborSystemRobotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	projectCreated := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborSystemRobot{}).
		Owns(&corev1.Secret{}).
		Watches(&harborv1alpha1.HarborConnection{}, handler.EnqueueRequestsFromMapFunc(r.robotsForConnection)).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(r.allRobots), builder.WithPredicates(projectCreated)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.allRobots)).
		Watches(&harborv1alpha1.HarborSystemRobot{}, handler.EnqueueRequestsFromMapFunc(r.otherClaimants)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4, RateLimiter: backoffLimiter(0)}).
		Named("harborsystemrobot").
		Complete(r)
}

func (r *HarborSystemRobotReconciler) robotsForConnection(ctx context.Context, object client.Object) []reconcile.Request {
	return r.robotRequests(ctx, client.MatchingFields{SystemRobotConnectionIndex: client.ObjectKeyFromObject(object).String()})
}

func (r *HarborSystemRobotReconciler) allRobots(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.robotRequests(ctx)
}

func (r *HarborSystemRobotReconciler) otherClaimants(ctx context.Context, object client.Object) []reconcile.Request {
	key := object.(*harborv1alpha1.HarborSystemRobot).Status.HarborKey
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

func (r *HarborSystemRobotReconciler) robotRequests(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	var robots harborv1alpha1.HarborSystemRobotList
	if err := r.List(ctx, &robots, options...); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(robots.Items))
	for index := range robots.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&robots.Items[index])})
	}

	return requests
}

// systemRobotConnectionKey is the connection the robot names; its namespace defaults to the robot's.
func systemRobotConnectionKey(robot *harborv1alpha1.HarborSystemRobot) types.NamespacedName {
	namespace := robot.Spec.ConnectionRef.Namespace
	if namespace == "" {
		namespace = robot.Namespace
	}

	return types.NamespacedName{Namespace: namespace, Name: robot.Spec.ConnectionRef.Name}
}

// systemRobotName is the robot's short name in Harbor, without Harbor's prefix.
func systemRobotName(robot *harborv1alpha1.HarborSystemRobot) string {
	if robot.Spec.Name != "" {
		return robot.Spec.Name
	}

	return robot.Name
}

// desiredSystemRobot is the Harbor robot the spec asks for: one system grant and one project grant per
// projectPermissions entry and project.
func desiredSystemRobot(robot *harborv1alpha1.HarborSystemRobot) harbor.RobotSpec {
	duration := robot.Spec.Duration
	if duration == 0 {
		duration = -1
	}

	grants := []harbor.RobotGrant{}

	if len(robot.Spec.SystemPermissions) > 0 {
		access := make([]harbor.RobotAccess, 0, len(robot.Spec.SystemPermissions))
		for _, permission := range robot.Spec.SystemPermissions {
			access = append(access, harbor.RobotAccess{Resource: permission.Resource, Action: permission.Action})
		}

		grants = append(grants, harbor.RobotGrant{Kind: harbor.LevelSystem, Namespace: "/", Access: access})
	}

	for _, entry := range robot.Spec.ProjectPermissions {
		access := make([]harbor.RobotAccess, 0, len(entry.Access))
		for _, permission := range entry.Access {
			access = append(access, harbor.RobotAccess{Resource: permission.Resource, Action: permission.Action})
		}

		for _, project := range entry.Projects {
			grants = append(grants, harbor.RobotGrant{Kind: harbor.LevelProject, Namespace: project, Access: access})
		}
	}

	return harbor.RobotSpec{
		Name:        systemRobotName(robot),
		Description: robot.Spec.Description,
		Duration:    duration,
		Disabled:    robot.Spec.Disabled,
		Grants:      grants,
	}
}

// namedProjects lists the project names the spec grants access in, without "*", sorted and without repeats.
func namedProjects(robot *harborv1alpha1.HarborSystemRobot) []string {
	var names []string

	for _, entry := range robot.Spec.ProjectPermissions {
		for _, project := range entry.Projects {
			if project != allProjects && !slices.Contains(names, project) {
				names = append(names, project)
			}
		}
	}

	slices.Sort(names)

	return names
}

// systemRobot is a HarborSystemRobot as the shared robot lifecycle sees it.
type systemRobot struct {
	robot *harborv1alpha1.HarborSystemRobot
}

func (s systemRobot) object() client.Object                            { return s.robot }
func (s systemRobot) status() *harborv1alpha1.HarborProjectRobotStatus { return &s.robot.Status }
func (s systemRobot) secretSpec() harborv1alpha1.RobotSecretSpec       { return s.robot.Spec.Secret }
func (s systemRobot) adopt() bool                                      { return s.robot.Spec.Adopt }
func (s systemRobot) deletionPolicy() harborv1alpha1.DeletionPolicy {
	return s.robot.Spec.DeletionPolicy
}
func (s systemRobot) resyncInterval() *metav1.Duration { return s.robot.Spec.ResyncInterval }
func (s systemRobot) shortName() string                { return systemRobotName(s.robot) }
func (s systemRobot) desired() harbor.RobotSpec        { return desiredSystemRobot(s.robot) }
func (s systemRobot) empty() robotResource {
	return systemRobot{robot: &harborv1alpha1.HarborSystemRobot{}}
}

// systemRobotTarget is Harbor's system level, where system robots live. projectIDs maps the projects the spec names to
// their Harbor IDs.
type systemRobotTarget struct {
	projectIDs map[string]int64
}

func (t systemRobotTarget) find(ctx context.Context, harborClient *harbor.Client, name string) (*harbor.Robot, error) {
	return harborClient.FindSystemRobot(ctx, name)
}

func (t systemRobotTarget) create(ctx context.Context, harborClient *harbor.Client, spec harbor.RobotSpec) (*harbor.Robot, error) {
	return harborClient.CreateSystemRobot(ctx, spec)
}

func (t systemRobotTarget) update(ctx context.Context, harborClient *harbor.Client, id int64, spec harbor.RobotSpec) error {
	return harborClient.UpdateRobot(ctx, id, "", spec)
}

// isOwn reports whether the Harbor robot is a system robot with the short name.
func (t systemRobotTarget) isOwn(found *harbor.Robot, name string) bool {
	return found.Level == harbor.LevelSystem && harbor.IsSystemRobotName(found.Name, name)
}

// missingPermissions checks system grants at system scope and project grants in each named project. Harbor cannot
// report what the connection holds in every project at once, so a grant on "*" always counts as missing: the robot is
// then never deleted on the chance that it can be created again.
func (t systemRobotTarget) missingPermissions(ctx context.Context, harborClient *harbor.Client, spec harbor.RobotSpec) ([]string, error) {
	ids := make([]int64, 0, len(t.projectIDs))
	for _, id := range t.projectIDs {
		ids = append(ids, id)
	}

	held, err := harborClient.ReadHeldPermissions(ctx, ids)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, grant := range spec.Grants {
		switch {
		case grant.Kind == harbor.LevelSystem:
			names = append(names, accessNames(held.MissingSystem(grant.Access), "system: ")...)
		case grant.Namespace == allProjects:
			names = append(names, accessNames(grant.Access, "every project: ")...)
		default:
			names = append(names, accessNames(held.MissingInProject(t.projectIDs[grant.Namespace], grant.Access), "project "+grant.Namespace+": ")...)
		}
	}

	return names, nil
}

func (t systemRobotTarget) location() string { return "at system level" }

func (r *HarborSystemRobotReconciler) lifecycle() *robotLifecycle {
	return &robotLifecycle{Client: r.Client, Recorder: r.Recorder, Wakeups: r.Wakeups}
}

// Reconcile makes the Harbor robot match the resource, writes its Secrets and records the result in status.
func (r *HarborSystemRobotReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	robot := &harborv1alpha1.HarborSystemRobot{}
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

// resolve finds the connection and checks that it admits the robot, gets a Harbor client, refuses the operator's own
// credential, checks that the named projects exist and checks ownership. It returns nil state when the reconcile ends
// here, with the result to return.
func (r *HarborSystemRobotReconciler) resolve(ctx context.Context, robot *harborv1alpha1.HarborSystemRobot) (*robotReconcile, reconcile.Result, error) {
	resource := systemRobot{robot: robot}
	lifecycle := r.lifecycle()
	resync := robotResync(resource, nil)

	stop := func(reason, message string) (*robotReconcile, reconcile.Result, error) {
		return nil, reconcile.Result{RequeueAfter: resync}, lifecycle.setReady(ctx, resource, metav1.ConditionFalse, reason, message)
	}

	key := systemRobotConnectionKey(robot)
	connection := &harborv1alpha1.HarborConnection{}

	if !r.WatchNamespaces.Watches(key.Namespace) {
		message := access.NotWatchedMessage(key.Namespace)
		setReferenceResolved(resource, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	if err := r.Get(ctx, key, connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}

		message := fmt.Sprintf("HarborConnection %s not found", key)
		setReferenceResolved(resource, metav1.ConditionFalse, harborv1alpha1.ReasonNotFound, message)

		return stop(harborv1alpha1.ReasonNotFound, message)
	}

	resync = robotResync(resource, connection)

	admitted, err := r.admitted(ctx, robot)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if !admitted {
		message := fmt.Sprintf("HarborConnection %s does not admit namespace %q for system-wide resources", key, robot.Namespace)
		setReferenceResolved(resource, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	setReferenceResolved(resource, metav1.ConditionTrue, harborv1alpha1.ReasonReferenceResolved, "HarborConnection "+key.String()+" admits the namespace")

	if reason, message := connectionNotReady(connection); reason != "" {
		return stop(reason, message)
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return stop(reason, message)
	}

	if user, err := r.operatorCredential(ctx, robot, connection.Status.HarborIdentity); err != nil || user != "" {
		if err != nil {
			return nil, reconcile.Result{}, err
		}

		return stop(harborv1alpha1.ReasonOperatorCredential, fmt.Sprintf("Harbor robot %q is the credential of HarborConnection %s; the operator never manages it", systemRobotName(robot), user))
	}

	harborKey := ownership.SystemRobotKey(connection.Status.HarborIdentity, systemRobotName(robot))
	if robot.Status.HarborKey != harborKey {
		// An ID recorded for another Harbor identity names nothing here.
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
		return stop(harborv1alpha1.ReasonConflict, fmt.Sprintf("Harbor robot %q is owned by HarborSystemRobot %s/%s", systemRobotName(robot), owner.Namespace, owner.Name))
	}

	state := &robotReconcile{robot: resource, connection: connection, harbor: harborClient, resync: resync}

	ids, missing, err := projectIDs(ctx, harborClient, namedProjects(robot))
	if err != nil {
		result, err := lifecycle.harborFailed(ctx, state, err)

		return nil, result, err
	}

	if len(missing) > 0 {
		// The projects may be created later; the project watch and the resync try again.
		return stop(harborv1alpha1.ReasonProjectNotFound, "Harbor projects not found: "+strings.Join(missing, ", "))
	}

	state.target = systemRobotTarget{projectIDs: ids}

	return state, reconcile.Result{}, nil
}

// projectIDs looks up the Harbor ID of each project and lists the projects Harbor does not have.
func projectIDs(ctx context.Context, harborClient *harbor.Client, names []string) (map[string]int64, []string, error) {
	ids := make(map[string]int64, len(names))

	var missing []string

	for _, name := range names {
		project, err := harborClient.GetProjectByName(ctx, name)

		switch {
		case harbor.IsNotFound(err):
			missing = append(missing, name)
		case err != nil:
			return nil, nil, err
		default:
			ids[name] = project.ID
		}
	}

	return ids, missing, nil
}

// admitted reports whether a robot's connection admits the robot's namespace for system-wide kinds.
func (r *HarborSystemRobotReconciler) admitted(ctx context.Context, robot *harborv1alpha1.HarborSystemRobot) (bool, error) {
	if !r.WatchNamespaces.Watches(systemRobotConnectionKey(robot).Namespace) {
		return false, nil
	}

	connection := &harborv1alpha1.HarborConnection{}
	if err := r.Get(ctx, systemRobotConnectionKey(robot), connection); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return access.Admits(ctx, r.Client, connection.Spec.SystemAllowedNamespaces, connection.Namespace, robot.Namespace)
}

// operatorCredential returns "namespace/name" of a connection to the same Harbor whose credentials log in as this
// robot, or "" when there is none. Managing that robot could lock the operator out of Harbor.
func (r *HarborSystemRobotReconciler) operatorCredential(ctx context.Context, robot *harborv1alpha1.HarborSystemRobot, identity string) (string, error) {
	var connections harborv1alpha1.HarborConnectionList
	if err := r.List(ctx, &connections); err != nil {
		return "", err
	}

	name := systemRobotName(robot)

	for index := range connections.Items {
		connection := &connections.Items[index]
		if connection.Status.HarborIdentity != identity {
			continue
		}

		secret := &corev1.Secret{}

		err := r.Get(ctx, client.ObjectKey{Namespace: connection.Namespace, Name: connection.Spec.CredentialsSecretRef.Name}, secret)

		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return "", err
		}

		username := harbor.MappedUsername(credentialKeys(connection), secret)
		if username != "" && (harbor.IsSystemRobotName(username, name) || username == robot.Status.HarborName) {
			return client.ObjectKeyFromObject(connection).String(), nil
		}
	}

	return "", nil
}

// finalize runs the deletion preamble: it deletes the Harbor robot when the resource owns it, the connection still
// admits it and its policy is Delete, deletes the robot's Secrets either way and then removes the finalizer.
func (r *HarborSystemRobotReconciler) finalize(ctx context.Context, robot *harborv1alpha1.HarborSystemRobot) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(robot, RobotFinalizer) {
		return reconcile.Result{}, nil
	}

	resource := systemRobot{robot: robot}
	lifecycle := r.lifecycle()

	if robot.Status.RobotID == 0 {
		return lifecycle.release(ctx, resource)
	}

	key := systemRobotConnectionKey(robot)
	connection := &harborv1alpha1.HarborConnection{}

	if !r.WatchNamespaces.Watches(key.Namespace) {
		r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"%s; the Harbor robot is left in place", access.NotWatchedMessage(key.Namespace))

		return lifecycle.release(ctx, resource)
	}

	if err := r.Get(ctx, key, connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		// A connection that no longer exists admits nothing, so the robot is left in place.
		if robotDeletionPolicy(resource, nil) != harborv1alpha1.DeletionPolicyOrphan {
			r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
				"HarborConnection %s no longer exists; the Harbor robot is left in place", key)
		}

		return lifecycle.release(ctx, resource)
	}

	if robotDeletionPolicy(resource, connection) == harborv1alpha1.DeletionPolicyOrphan {
		recordOrphaned(lifecycle.Recorder, robot, "robot", resource.shortName())

		return lifecycle.release(ctx, resource)
	}

	admitted, err := r.admitted(ctx, robot)
	if err != nil {
		return reconcile.Result{}, err
	}

	if !admitted {
		r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"HarborConnection %s does not admit namespace %q; the Harbor robot is left in place", key, robot.Namespace)

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

	harborKey := ownership.SystemRobotKey(connection.Status.HarborIdentity, systemRobotName(robot))
	if robot.Status.HarborKey != harborKey {
		// The ID was recorded for another Harbor identity; it names nothing of this resource's here.
		return lifecycle.release(ctx, resource)
	}

	if user, err := r.operatorCredential(ctx, robot, connection.Status.HarborIdentity); err != nil || user != "" {
		if err != nil {
			return reconcile.Result{}, err
		}

		r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonOperatorCredential, "Delete",
			"Harbor robot is the credential of HarborConnection %s; it is left in place", user)

		return lifecycle.release(ctx, resource)
	}

	// Only the owner deletes the robot the ID names.
	owner, err := ownership.RobotOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return reconcile.Result{}, err
	}

	if owner == nil || owner.UID != robot.UID {
		return lifecycle.release(ctx, resource)
	}

	return lifecycle.deleteHarborRobot(ctx, &robotReconcile{
		robot: resource, target: systemRobotTarget{}, connection: connection, harbor: harborClient, resync: resync,
	})
}
