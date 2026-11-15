package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	"github.com/solidDoWant/harbor-operator/internal/secrets"
)

// RobotFinalizer keeps a HarborProjectRobot until its Harbor robot has been dealt with.
const RobotFinalizer = "harbor.soliddowant.dev/robot"

// HarborProjectRobotReconciler makes a Harbor project robot match its HarborProjectRobot and writes its credentials
// into Secrets.
type HarborProjectRobotReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
	// Recorder emits events on the resource.
	Recorder events.EventRecorder
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
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
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

// robotDiffers reports whether the Harbor robot's managed fields differ from the spec. Permissions are compared as a
// set of resource and action pairs.
func robotDiffers(want harbor.RobotSpec, have *harbor.Robot) bool {
	if want.Description != have.Description || want.Duration != have.Duration || want.Disabled != have.Disabled {
		return true
	}

	wantSet := make(map[harbor.RobotAccess]struct{}, len(want.Permissions))
	for _, permission := range want.Permissions {
		wantSet[permission] = struct{}{}
	}

	haveSet := make(map[harbor.RobotAccess]struct{}, len(have.Permissions))
	for _, permission := range have.Permissions {
		haveSet[permission] = struct{}{}
	}

	if len(wantSet) != len(haveSet) {
		return true
	}

	for permission := range wantSet {
		if _, ok := haveSet[permission]; !ok {
			return true
		}
	}

	return false
}

// robotReconcile carries what one reconcile has resolved.
type robotReconcile struct {
	robot       *harborv1alpha1.HarborProjectRobot
	project     *harborv1alpha1.HarborProject
	connection  *harborv1alpha1.HarborConnection
	harbor      *harbor.Client
	projectName string
	resync      time.Duration
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

	return r.sync(ctx, state)
}

// robotResync is the robot's resync interval: its own, else the connection's default.
func robotResync(robot *harborv1alpha1.HarborProjectRobot, connection *harborv1alpha1.HarborConnection) time.Duration {
	if robot.Spec.ResyncInterval != nil && robot.Spec.ResyncInterval.Duration > 0 {
		return robot.Spec.ResyncInterval.Duration
	}

	if connection != nil {
		return resyncInterval(connection)
	}

	return defaultResyncInterval
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
	resync := robotResync(robot, nil)

	stop := func(reason, message string) (*robotReconcile, reconcile.Result, error) {
		return nil, reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, robot, metav1.ConditionFalse, reason, message)
	}

	key := robotProjectKey(robot)
	project := &harborv1alpha1.HarborProject{}

	if err := r.Get(ctx, key, project); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}

		message := fmt.Sprintf("HarborProject %s not found", key)
		r.setReferenceResolved(robot, metav1.ConditionFalse, harborv1alpha1.ReasonNotFound, message)

		return stop(harborv1alpha1.ReasonProjectNotReady, message)
	}

	admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, robot.Namespace)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if !admitted {
		message := fmt.Sprintf("HarborProject %s does not admit namespace %q", key, robot.Namespace)
		r.setReferenceResolved(robot, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	r.setReferenceResolved(robot, metav1.ConditionTrue, harborv1alpha1.ReasonReferenceResolved, "HarborProject "+key.String()+" admits the namespace")

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

	resync = robotResync(robot, connection)

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

	harborKey := ownership.RobotKey(identity, projectName(project), robotName(robot))
	if robot.Status.HarborKey != harborKey {
		// An ID recorded for another Harbor identity or project names nothing here.
		robot.Status.RobotID = 0
		robot.Status.HarborKey = harborKey

		if err := r.updateStatus(ctx, robot); err != nil {
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
		robot: robot, project: project, connection: connection, harbor: harborClient,
		projectName: projectName(project), resync: resync,
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

// isOwnRobot reports whether the Harbor robot is this resource's: the same short name in the same project.
func isOwnRobot(state *robotReconcile, found *harbor.Robot) bool {
	return strings.HasSuffix(found.Name, "+"+robotName(state.robot)) && found.ProjectName == state.projectName
}

// sync finds, creates or adopts the Harbor robot, then makes it match the spec.
func (r *HarborProjectRobotReconciler) sync(ctx context.Context, state *robotReconcile) (reconcile.Result, error) {
	robot := state.robot
	name := robotName(robot)
	recreate := false

	if err := removeLegacyCreatePending(ctx, r.Client, robot); err != nil {
		return reconcile.Result{}, err
	}

	if robot.Status.RobotID != 0 {
		found, err := state.harbor.GetRobot(ctx, robot.Status.RobotID)

		switch {
		case harbor.IsNotFound(err):
			recreate = true
		case err != nil:
			return r.harborFailed(ctx, state, err)
		case !isOwnRobot(state, found):
			// The ID now names another robot; it is not this resource's.
		default:
			return r.update(ctx, state, found)
		}

		robot.Status.RobotID = 0
		if err := r.updateStatus(ctx, robot); err != nil {
			return reconcile.Result{}, err
		}
	}

	found, err := state.harbor.FindProjectRobot(ctx, int64(state.project.Status.ProjectID), name)

	switch {
	case harbor.IsNotFound(err):
		return r.create(ctx, state, recreate)
	case err != nil:
		return r.harborFailed(ctx, state, err)
	}

	ownCreate := isOwnCreate(robot.Status.CreatePending, found.CreationTime)

	switch {
	case ownCreate:
		// The robot was created by this resource just before the reconcile was interrupted; its secret was lost.
	case !robot.Spec.Adopt:
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists,
			fmt.Sprintf("Harbor robot %q already exists in project %q; set adopt: true to take it over", name, state.projectName))
	case !state.connection.Spec.AllowAdoption:
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonAdoptionNotAllowed,
			fmt.Sprintf("HarborConnection %s does not allow adoption", client.ObjectKeyFromObject(state.connection)))
	}

	// The secret of a robot found this way is unknown, so it is refreshed before the robot is used.
	if err := r.markPending(ctx, robot, harborv1alpha1.AnnotationSecretRefreshPending); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.recordID(ctx, robot, found); err != nil {
		return reconcile.Result{}, err
	}

	if !ownCreate {
		r.Recorder.Eventf(robot, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonAdopted, "Adopt", "Adopted the existing Harbor robot %q", found.Name)
	}

	return r.update(ctx, state, found)
}

// create creates the Harbor robot, marking the resource first so that an interrupted create is recognised as its own,
// and writes its Secrets.
func (r *HarborProjectRobotReconciler) create(ctx context.Context, state *robotReconcile, recreate bool) (reconcile.Result, error) {
	robot := state.robot

	created, done, result, err := r.createRobot(ctx, state)
	if done {
		return result, err
	}

	if recreate {
		r.Recorder.Eventf(robot, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonRecreated, "Create", "Created Harbor robot %q again after it was deleted in Harbor", created.Name)
	}

	// The robot was created from the spec and its Secrets were just written with its secret, which the cache may not
	// show yet; reading them back now would refresh the new secret for nothing.
	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor robot matches the spec")
}

// createRobot creates the robot, records it and writes its Secrets. done is true when the reconcile ends here.
func (r *HarborProjectRobotReconciler) createRobot(ctx context.Context, state *robotReconcile) (*harbor.Robot, bool, reconcile.Result, error) {
	robot := state.robot

	if err := r.markCreatePending(ctx, robot); err != nil {
		return nil, true, reconcile.Result{}, err
	}

	created, err := state.harbor.CreateRobot(ctx, state.projectName, desiredRobot(robot))
	if err != nil {
		result, err := r.harborFailed(ctx, state, err)
		return nil, true, result, err
	}

	robot.Status.ObservedRotate = robot.Spec.Secret.Rotate

	if err := r.recordID(ctx, robot, created); err != nil {
		return nil, true, reconcile.Result{}, err
	}

	// Until the Secrets hold the new secret the create marker stays, so an interrupted write refreshes it.
	if conflict, err := r.writeSecrets(ctx, state, created.Name, created.Secret, ""); conflict || err != nil {
		return nil, true, reconcile.Result{RequeueAfter: state.resync}, err
	}

	if err := r.clearPending(ctx, robot); err != nil {
		return nil, true, reconcile.Result{}, err
	}

	return created, false, reconcile.Result{}, nil
}

// update makes the Harbor robot match the spec and finishes an interrupted secret write.
func (r *HarborProjectRobotReconciler) update(ctx context.Context, state *robotReconcile, current *harbor.Robot) (reconcile.Result, error) {
	robot := state.robot

	if current.Duration > 0 && current.ExpiresAt != nil && current.ExpiresAt.Before(time.Now()) {
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonExpired,
			fmt.Sprintf("Harbor robot %q expired at %s", current.Name, current.ExpiresAt.Format(time.RFC3339)))
	}

	if want := desiredRobot(robot); robotDiffers(want, current) {
		err := state.harbor.UpdateRobot(ctx, current.ID, state.projectName, want)

		switch {
		case harbor.IsForbidden(err):
			return r.replace(ctx, state, current)
		case err != nil:
			return r.harborFailed(ctx, state, err)
		}

		updated, err := state.harbor.GetRobot(ctx, current.ID)
		if err != nil {
			return r.harborFailed(ctx, state, err)
		}

		current = updated
		if err := r.recordID(ctx, robot, current); err != nil {
			return reconcile.Result{}, err
		}
	}

	// A foreign Secret on a target name stops the reconcile before a refresh would leave no Secret with the new secret.
	if conflict, err := r.secretConflict(ctx, robot); conflict || err != nil {
		return reconcile.Result{RequeueAfter: state.resync}, err
	}

	secret, source, found, err := r.currentSecret(ctx, robot)
	if err != nil {
		return reconcile.Result{}, err
	}

	if !found {
		return r.refreshSecret(ctx, state, current)
	}

	conflict, err := r.writeSecrets(ctx, state, current.Name, secret, source)

	switch {
	case errors.Is(err, errSourceSecretGone):
		return r.refreshSecret(ctx, state, current)
	case conflict || err != nil:
		return reconcile.Result{RequeueAfter: state.resync}, err
	}

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor robot matches the spec")
}

// currentSecret returns the robot secret held by a Secret this robot owns, preferring the Secrets the spec names, and
// the name of that Secret. found is false when the secret must be refreshed: an interrupted create or refresh, a
// changed secret.rotate, or no owned Secret whose data matches its hash annotation.
func (r *HarborProjectRobotReconciler) currentSecret(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) (string, string, bool, error) {
	createPending := robot.Status.CreatePending != nil
	_, refreshPending := robot.Annotations[harborv1alpha1.AnnotationSecretRefreshPending]

	if createPending || refreshPending || robot.Spec.Secret.Rotate != robot.Status.ObservedRotate {
		return "", "", false, nil
	}

	owned, err := r.ownedSecrets(ctx, robot)
	if err != nil {
		return "", "", false, err
	}

	wanted := make(map[string]bool)
	for _, name := range secrets.Names(robot.Name, robot.Spec.Secret) {
		wanted[name] = true
	}

	// Two passes: the Secrets the spec names first, then any other owned one, such as one just disabled or renamed.
	for _, preferWanted := range []bool{true, false} {
		for _, secret := range owned {
			if wanted[secret.Name] != preferWanted {
				continue
			}

			if value, ok := secrets.Recover(secret); ok {
				return value, secret.Name, true, nil
			}
		}
	}

	return "", "", false, nil
}

// refreshSecret has Harbor generate a new secret for the robot, writes every enabled Secret with it and records the
// rotation.
func (r *HarborProjectRobotReconciler) refreshSecret(ctx context.Context, state *robotReconcile, current *harbor.Robot) (reconcile.Result, error) {
	robot := state.robot

	if err := r.markPending(ctx, robot, harborv1alpha1.AnnotationSecretRefreshPending); err != nil {
		return reconcile.Result{}, err
	}

	secret, err := state.harbor.RefreshRobotSecret(ctx, current.ID)

	switch {
	case harbor.IsForbidden(err):
		return r.replace(ctx, state, current)
	case err != nil:
		return r.harborFailed(ctx, state, err)
	}

	if conflict, err := r.writeSecrets(ctx, state, current.Name, secret, ""); conflict || err != nil {
		return reconcile.Result{RequeueAfter: state.resync}, err
	}

	now := metav1.Now()
	robot.Status.LastRotated = &now
	robot.Status.ObservedRotate = robot.Spec.Secret.Rotate

	if err := r.updateStatus(ctx, robot); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.clearPending(ctx, robot); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor robot matches the spec")
}

// secretConflict reports, and records as SecretConflict, a target Secret name taken by a Secret this robot does not
// own. That Secret is never changed.
func (r *HarborProjectRobotReconciler) secretConflict(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) (bool, error) {
	for _, name := range secrets.Names(robot.Name, robot.Spec.Secret) {
		have := &corev1.Secret{}

		err := r.Get(ctx, client.ObjectKey{Namespace: robot.Namespace, Name: name}, have)

		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return false, err
		case !secrets.IsOwnedBy(have, robot):
			return true, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonSecretConflict,
				fmt.Sprintf("Secret %q exists and is not owned by this robot", name))
		}
	}

	return false, nil
}

// replace deletes the robot and creates it again with the spec's fields, for connections whose credentials may create
// and delete robots but not update them.
func (r *HarborProjectRobotReconciler) replace(ctx context.Context, state *robotReconcile, current *harbor.Robot) (reconcile.Result, error) {
	robot := state.robot

	if err := r.markCreatePending(ctx, robot); err != nil {
		return reconcile.Result{}, err
	}

	if err := state.harbor.DeleteRobot(ctx, current.ID); err != nil && !harbor.IsNotFound(err) {
		return r.harborFailed(ctx, state, err)
	}

	created, done, result, err := r.createRobot(ctx, state)
	if done {
		return result, err
	}

	r.Recorder.Eventf(robot, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonReplaced, "Replace",
		"Replaced Harbor robot %q because the connection's credentials cannot update robots", created.Name)

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor robot matches the spec")
}

// errSourceSecretGone reports that the Secret a robot secret was read from was deleted before it was written back. The
// secret is then lost and must be refreshed: writing it again would bring back the secret the user threw away.
var errSourceSecretGone = errors.New("the Secret holding the robot secret was deleted")

// writeSecrets writes every enabled Secret with the robot's credentials, then deletes the Secrets this robot owns that
// the spec no longer names. conflict is true, and nothing is written, when a target name is taken by a Secret this
// robot does not own. source names the Secret the secret was read from, if any: that Secret is updated but never
// created again, and errSourceSecretGone is returned when it no longer exists.
func (r *HarborProjectRobotReconciler) writeSecrets(ctx context.Context, state *robotReconcile, harborName, secret, source string) (bool, error) {
	robot := state.robot

	if conflict, err := r.secretConflict(ctx, robot); conflict || err != nil {
		return conflict, err
	}

	rendered, err := secrets.Render(secrets.Input{
		Owner: robot, Namespace: robot.Namespace, Spec: robot.Spec.Secret,
		RegistryHost: state.connection.Status.RegistryHost, HarborName: harborName, Secret: secret,
		Scheme: r.Scheme(),
	})
	if err != nil {
		return false, err
	}

	keep := make(map[string]bool, len(rendered))

	for _, want := range rendered {
		keep[want.Name] = true

		if err := r.writeSecret(ctx, want, want.Name != source); err != nil {
			return false, err
		}
	}

	return false, r.deleteOwnedSecrets(ctx, robot, keep)
}

// writeSecret creates the Secret, or updates the robot's own Secret of that name when it differs. A Secret's type cannot
// change, so one of another type is deleted and created again. Without mayCreate a missing Secret is not created and
// errSourceSecretGone is returned.
func (r *HarborProjectRobotReconciler) writeSecret(ctx context.Context, want *corev1.Secret, mayCreate bool) error {
	have := &corev1.Secret{}

	err := r.Get(ctx, client.ObjectKeyFromObject(want), have)

	switch {
	case apierrors.IsNotFound(err) && !mayCreate:
		return errSourceSecretGone
	case apierrors.IsNotFound(err):
		return r.Create(ctx, want)
	case err != nil:
		return err
	case have.Type != want.Type:
		err := r.Delete(ctx, have, client.Preconditions{UID: &have.UID})

		switch {
		case apierrors.IsNotFound(err) && !mayCreate:
			return errSourceSecretGone
		case client.IgnoreNotFound(err) != nil:
			return err
		}

		return r.Create(ctx, want)
	}

	labels := mergeStrings(have.Labels, want.Labels)
	annotations := mergeStrings(have.Annotations, want.Annotations)

	if apiequality.Semantic.DeepEqual(have.Data, want.Data) && apiequality.Semantic.DeepEqual(have.Labels, labels) &&
		apiequality.Semantic.DeepEqual(have.Annotations, annotations) {
		return nil
	}

	have.Data = want.Data
	have.Labels = labels
	have.Annotations = annotations

	return r.Update(ctx, have)
}

// mergeStrings returns a copy of into with the keys of from set.
func mergeStrings(into, from map[string]string) map[string]string {
	merged := make(map[string]string, len(into)+len(from))

	for key, value := range into {
		merged[key] = value
	}

	for key, value := range from {
		merged[key] = value
	}

	return merged
}

// recordID writes the robot's ID, Harbor name and expiry to status.
func (r *HarborProjectRobotReconciler) recordID(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot, found *harbor.Robot) error {
	robot.Status.RobotID = found.ID
	robot.Status.HarborName = found.Name
	robot.Status.ExpiresAt = nil

	if found.ExpiresAt != nil && found.Duration > 0 {
		robot.Status.ExpiresAt = &metav1.Time{Time: *found.ExpiresAt}
	}

	return r.updateStatus(ctx, robot)
}

// markPending sets a pending annotation, with the current time, unless it is already set.
func (r *HarborProjectRobotReconciler) markPending(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot, annotation string) error {
	if _, ok := robot.Annotations[annotation]; ok {
		return nil
	}

	metav1.SetMetaDataAnnotation(&robot.ObjectMeta, annotation, time.Now().UTC().Format(time.RFC3339))

	return r.Update(ctx, robot)
}

// markCreatePending records in status, which namespace users cannot write, that a create call is about to be made,
// unless it is already recorded.
func (r *HarborProjectRobotReconciler) markCreatePending(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) error {
	if robot.Status.CreatePending != nil {
		return nil
	}

	robot.Status.CreatePending = &metav1.Time{Time: time.Now().UTC().Truncate(time.Second)}

	return r.updateStatus(ctx, robot)
}

// clearPending clears the create marker and the secret-refresh-pending annotation. Status is written first: an update
// of the resource replaces the in-memory status with the stored one.
func (r *HarborProjectRobotReconciler) clearPending(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) error {
	if robot.Status.CreatePending != nil {
		robot.Status.CreatePending = nil

		if err := r.updateStatus(ctx, robot); err != nil {
			return err
		}
	}

	if _, ok := robot.Annotations[harborv1alpha1.AnnotationSecretRefreshPending]; !ok {
		return nil
	}

	delete(robot.Annotations, harborv1alpha1.AnnotationSecretRefreshPending)

	return r.Update(ctx, robot)
}

// finalize runs the deletion preamble: it deletes the Harbor robot when the resource owns it and its policy is Delete,
// deletes the robot's Secrets either way and then removes the finalizer.
func (r *HarborProjectRobotReconciler) finalize(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(robot, RobotFinalizer) {
		return reconcile.Result{}, nil
	}

	release := func() (reconcile.Result, error) {
		if err := r.deleteOwnedSecrets(ctx, robot, nil); err != nil {
			return reconcile.Result{}, err
		}

		controllerutil.RemoveFinalizer(robot, RobotFinalizer)

		return reconcile.Result{}, client.IgnoreNotFound(r.Update(ctx, robot))
	}

	if robot.Status.RobotID == 0 {
		return release()
	}

	key := robotProjectKey(robot)
	project := &harborv1alpha1.HarborProject{}

	if err := r.Get(ctx, key, project); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		// Without its project the robot cannot be reached through an admitted reference.
		return release()
	}

	connection := &harborv1alpha1.HarborConnection{}
	connectionErr := r.Get(ctx, connectionKey(project), connection)

	if connectionErr != nil && !apierrors.IsNotFound(connectionErr) {
		return reconcile.Result{}, connectionErr
	}

	policy := robot.Spec.DeletionPolicy
	if policy == "" && connectionErr == nil {
		policy = connection.Spec.Defaults.DeletionPolicy
	}

	if policy == harborv1alpha1.DeletionPolicyOrphan {
		return release()
	}

	admitted, err := access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, robot.Namespace)
	if err != nil {
		return reconcile.Result{}, err
	}

	if !admitted {
		r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"HarborProject %s does not admit namespace %q; the Harbor robot is left in place", key, robot.Namespace)

		return release()
	}

	if connectionErr != nil {
		// A connection that no longer exists admits nothing, so the project reference is no longer admitted.
		r.Recorder.Eventf(robot, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonNotAllowed, "Delete",
			"the HarborConnection of HarborProject %s no longer exists; the Harbor robot is left in place", key)

		return release()
	}

	resync := robotResync(robot, connection)

	identity := connection.Status.HarborIdentity
	if ready := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionTrue || identity == "" {
		reason := harborv1alpha1.ReasonHarborUnavailable
		if ready != nil && ready.Status == metav1.ConditionFalse {
			reason = ready.Reason
		}

		return reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, robot, metav1.ConditionFalse, reason,
			fmt.Sprintf("HarborConnection %s is not ready", client.ObjectKeyFromObject(connection)))
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, robot, metav1.ConditionFalse, reason, message)
	}

	harborKey := ownership.RobotKey(identity, projectName(project), robotName(robot))
	if robot.Status.HarborKey != harborKey {
		// The ID was recorded for another Harbor identity or project; it names nothing of this resource's here.
		return release()
	}

	// Another claimant may own the robot this ID names, for example when this resource is in Conflict after adopting
	// it; only the owner deletes it.
	owner, err := ownership.RobotOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return reconcile.Result{}, err
	}

	if owner == nil || owner.UID != robot.UID {
		return release()
	}

	state := &robotReconcile{robot: robot, project: project, connection: connection, harbor: harborClient, projectName: projectName(project), resync: resync}

	found, err := harborClient.GetRobot(ctx, robot.Status.RobotID)

	switch {
	case harbor.IsNotFound(err):
		return release()
	case err != nil:
		return r.harborFailed(ctx, state, err)
	case !isOwnRobot(state, found):
		// The ID names a robot this resource does not own; nothing is deleted.
		return release()
	}

	if err := harborClient.DeleteRobot(ctx, found.ID); err != nil && !harbor.IsNotFound(err) {
		return r.harborFailed(ctx, state, err)
	}

	return release()
}

// ownedSecrets lists the Secrets in the robot's namespace whose controller is this robot.
func (r *HarborProjectRobotReconciler) ownedSecrets(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) ([]*corev1.Secret, error) {
	var list corev1.SecretList
	if err := r.List(ctx, &list, client.InNamespace(robot.Namespace), client.MatchingLabels{secrets.LabelManagedBy: secrets.ManagedByValue}); err != nil {
		return nil, err
	}

	var owned []*corev1.Secret

	for index := range list.Items {
		if secrets.IsOwnedBy(&list.Items[index], robot) {
			owned = append(owned, &list.Items[index])
		}
	}

	return owned, nil
}

// deleteOwnedSecrets deletes the Secrets whose controller is this robot, except those named in keep. When the
// resource is deleted garbage collection would also remove them; deleting them here makes them go with the resource.
func (r *HarborProjectRobotReconciler) deleteOwnedSecrets(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot, keep map[string]bool) error {
	owned, err := r.ownedSecrets(ctx, robot)
	if err != nil {
		return err
	}

	for _, secret := range owned {
		if keep[secret.Name] {
			continue
		}

		if err := r.Delete(ctx, secret, client.Preconditions{UID: &secret.UID}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	return nil
}

// harborFailed reports a Harbor error. A spec Harbor rejects waits for a spec change; anything else retries with
// backoff.
func (r *HarborProjectRobotReconciler) harborFailed(ctx context.Context, state *robotReconcile, err error) (reconcile.Result, error) {
	reason, retry := harbor.MapError(err)
	if statusErr := r.setReady(ctx, state.robot, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	if !retry {
		return reconcile.Result{}, nil
	}

	return reconcile.Result{}, err
}

func (r *HarborProjectRobotReconciler) setReferenceResolved(robot *harborv1alpha1.HarborProjectRobot, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&robot.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReferenceResolved, Status: status, Reason: reason, Message: message, ObservedGeneration: robot.Generation,
	})
}

// setReady records the Ready condition and emits an event when it changes.
func (r *HarborProjectRobotReconciler) setReady(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot, status metav1.ConditionStatus, reason, message string) error {
	previous := meta.FindStatusCondition(robot.Status.Conditions, harborv1alpha1.ConditionReady)
	changed := previous == nil || previous.Status != status || previous.Reason != reason || previous.Message != message

	robot.Status.ObservedGeneration = robot.Generation
	meta.SetStatusCondition(&robot.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: robot.Generation,
	})

	if err := r.updateStatus(ctx, robot); err != nil {
		return err
	}

	if changed {
		eventType := corev1.EventTypeNormal
		if status != metav1.ConditionTrue {
			eventType = corev1.EventTypeWarning
		}

		r.Recorder.Eventf(robot, nil, eventType, reason, "Reconcile", "%s", message)
	}

	return nil
}

// updateStatus writes status only when it differs from what the API server has.
func (r *HarborProjectRobotReconciler) updateStatus(ctx context.Context, robot *harborv1alpha1.HarborProjectRobot) error {
	stored := &harborv1alpha1.HarborProjectRobot{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(robot), stored); err == nil &&
		stored.ResourceVersion == robot.ResourceVersion && apiequality.Semantic.DeepEqual(stored.Status, robot.Status) {
		return nil
	}

	return r.Status().Update(ctx, robot)
}
