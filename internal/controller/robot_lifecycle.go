package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/secrets"
)

// RobotFinalizer keeps a robot resource until its Harbor robot has been dealt with.
const RobotFinalizer = "harbor.soliddowant.dev/robot"

// The ownership index on status.harborKey covers system robots too, so the manager's cache watches them.
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborsystemrobots,verbs=get;list;watch

// robotResource is a robot custom resource as the shared robot lifecycle sees it. Project and system robots share the
// status type and the Secret, adoption, deletion and resync fields; only the Harbor robot they ask for differs.
type robotResource interface {
	// object is the custom resource itself, for API calls, events and Secret ownership.
	object() client.Object
	status() *harborv1alpha1.HarborProjectRobotStatus
	secretSpec() harborv1alpha1.RobotSecretSpec
	adopt() bool
	deletionPolicy() harborv1alpha1.DeletionPolicy
	resyncInterval() *metav1.Duration
	// shortName is the robot's name in Harbor without Harbor's prefix.
	shortName() string
	// desired is the Harbor robot the spec asks for.
	desired() harbor.RobotSpec
	// empty returns a new, empty resource of the same kind to read the stored copy into.
	empty() robotResource
}

// robotTarget is the Harbor side of one robot kind: where its robot lives and how it is looked up, created and updated.
type robotTarget interface {
	// find looks the robot up by its short name and returns a 404 error when there is none.
	find(ctx context.Context, harborClient *harbor.Client, name string) (*harbor.Robot, error)
	create(ctx context.Context, harborClient *harbor.Client, spec harbor.RobotSpec) (*harbor.Robot, error)
	update(ctx context.Context, harborClient *harbor.Client, id int64, spec harbor.RobotSpec) error
	// isOwn reports whether a Harbor robot found by ID is the resource's: the same short name in the same place.
	isOwn(found *harbor.Robot, name string) bool
	// missingPermissions names the permissions of spec the connection does not hold where the robot needs them. A
	// robot asking for any of them would be refused, so a replace must not start.
	missingPermissions(ctx context.Context, harborClient *harbor.Client, spec harbor.RobotSpec) ([]string, error)
	// location says where the robot lives, for messages, for example `in project "team"`.
	location() string
}

// robotReconcile carries what one reconcile has resolved.
type robotReconcile struct {
	robot      robotResource
	target     robotTarget
	connection *harborv1alpha1.HarborConnection
	harbor     *harbor.Client
	resync     time.Duration
}

// robotLifecycle holds the steps every robot kind shares: create, adopt, update, replace, Secrets, rotation, expiry and
// deletion of the Harbor robot.
type robotLifecycle struct {
	client.Client
	Recorder events.EventRecorder
}

// robotResync is the robot's resync interval: its own, else the connection's default.
func robotResync(robot robotResource, connection *harborv1alpha1.HarborConnection) time.Duration {
	if interval := robot.resyncInterval(); interval != nil && interval.Duration > 0 {
		return interval.Duration
	}

	if connection != nil {
		return resyncInterval(connection)
	}

	return defaultResyncInterval
}

// robotDeletionPolicy is the robot's deletion policy: its own, else the connection's default when the connection is
// known.
func robotDeletionPolicy(robot robotResource, connection *harborv1alpha1.HarborConnection) harborv1alpha1.DeletionPolicy {
	policy := robot.deletionPolicy()
	if policy == "" && connection != nil {
		policy = connection.Spec.Defaults.DeletionPolicy
	}

	return policy
}

// robotDiffers reports whether the Harbor robot's managed fields differ from the spec. Permissions are compared as a
// set of resource and action pairs, or of kind, namespace, resource and action when the spec has grants.
func robotDiffers(want harbor.RobotSpec, have *harbor.Robot) bool {
	return len(robotDriftFields(want, have)) > 0
}

// robotDriftFields names the spec fields whose Harbor value differs from the spec.
func robotDriftFields(want harbor.RobotSpec, have *harbor.Robot) []string {
	fields := []string{
		fieldIf(want.Description != have.Description, "description"),
		fieldIf(want.Duration != have.Duration, "duration"),
		fieldIf(want.Disabled != have.Disabled, "disabled"),
	}

	if want.Grants != nil {
		fields = append(fields, fieldIf(!maps.Equal(harbor.FlattenGrants(want.Grants), harbor.FlattenGrants(have.Grants)), "permissions"))
	} else {
		fields = append(fields, fieldIf(!samePermissions(want.Permissions, have.Permissions), "permissions"))
	}

	return slices.DeleteFunc(fields, func(field string) bool { return field == "" })
}

func samePermissions(want, have []harbor.RobotAccess) bool {
	wantSet := make(map[harbor.RobotAccess]struct{}, len(want))
	for _, permission := range want {
		wantSet[permission] = struct{}{}
	}

	haveSet := make(map[harbor.RobotAccess]struct{}, len(have))
	for _, permission := range have {
		haveSet[permission] = struct{}{}
	}

	return maps.Equal(wantSet, haveSet)
}

// connectionNotReady returns the reason and message when the connection is not ready to reach Harbor, or an empty
// reason when it is.
func connectionNotReady(connection *harborv1alpha1.HarborConnection) (string, string) {
	ready := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionReady)
	if ready != nil && ready.Status == metav1.ConditionTrue && connection.Status.HarborIdentity != "" {
		return "", ""
	}

	reason := harborv1alpha1.ReasonHarborUnavailable
	if ready != nil && ready.Status == metav1.ConditionFalse {
		reason = ready.Reason
	}

	return reason, fmt.Sprintf("HarborConnection %s is not ready", client.ObjectKeyFromObject(connection))
}

// connectionNotReadyForDeletion is connectionNotReady for a dependent's finalizer. A terminating connection keeps its
// finalizer, and reports not ready, until its dependents are gone, so it still counts as usable while it knows its
// Harbor identity: requiring Ready there would leave both sides waiting for each other. Whether Harbor is reachable and
// accepts the credentials is then decided by the Harbor calls themselves, whose errors keep the dependent's finalizer.
func connectionNotReadyForDeletion(connection *harborv1alpha1.HarborConnection) (string, string) {
	if !connection.DeletionTimestamp.IsZero() && connection.Status.HarborIdentity != "" {
		return "", ""
	}

	return connectionNotReady(connection)
}

// sync finds, creates or adopts the Harbor robot, then makes it match the spec.
func (r *robotLifecycle) sync(ctx context.Context, state *robotReconcile) (reconcile.Result, error) {
	robot := state.robot
	status := robot.status()
	name := robot.shortName()
	recreate := false

	if err := removeLegacyCreatePending(ctx, r.Client, robot.object()); err != nil {
		return reconcile.Result{}, err
	}

	if status.RobotID != 0 {
		found, err := state.harbor.GetRobot(ctx, status.RobotID)

		switch {
		case harbor.IsNotFound(err):
			recreate = true
		case err != nil:
			return r.harborFailed(ctx, state, err)
		case !state.target.isOwn(found, name):
			// The ID now names another robot; it is not this resource's.
		default:
			return r.update(ctx, state, found)
		}

		status.RobotID = 0

		if err := r.updateStatus(ctx, robot); err != nil {
			return reconcile.Result{}, err
		}
	}

	found, err := state.target.find(ctx, state.harbor, name)

	switch {
	case harbor.IsNotFound(err):
		return r.create(ctx, state, recreate)
	case err != nil:
		return r.harborFailed(ctx, state, err)
	}

	ownCreate := isOwnCreate(status.CreatePending, found.CreationTime)

	switch {
	case ownCreate:
		// The robot was created by this resource just before the reconcile was interrupted; its secret was lost.
	case !robot.adopt():
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists,
			fmt.Sprintf("Harbor robot %q already exists %s; set adopt: true to take it over", name, state.target.location()))
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
		r.Recorder.Eventf(robot.object(), nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonAdopted, "Adopt", "Adopted the existing Harbor robot %q", found.Name)
	}

	return r.update(ctx, state, found)
}

// create creates the Harbor robot, marking the resource first so that an interrupted create is recognised as its own,
// and writes its Secrets.
func (r *robotLifecycle) create(ctx context.Context, state *robotReconcile, recreate bool) (reconcile.Result, error) {
	robot := state.robot

	created, done, result, err := r.createRobot(ctx, state)
	if done {
		return result, err
	}

	if recreate {
		r.Recorder.Eventf(robot.object(), nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonRecreated, "Create", "Created Harbor robot %q again after it was deleted in Harbor", created.Name)
	} else {
		recordCreated(r.Recorder, robot.object(), "robot", created.Name)
	}

	// The robot was created from the spec and its Secrets were just written with its secret, which the cache may not
	// show yet; reading them back now would refresh the new secret for nothing.
	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor robot matches the spec")
}

// createRobot creates the robot, records it and writes its Secrets. done is true when the reconcile ends here.
func (r *robotLifecycle) createRobot(ctx context.Context, state *robotReconcile) (*harbor.Robot, bool, reconcile.Result, error) {
	robot := state.robot
	status := robot.status()

	if err := r.markCreatePending(ctx, robot); err != nil {
		return nil, true, reconcile.Result{}, err
	}

	created, err := state.target.create(ctx, state.harbor, robot.desired())
	if err != nil {
		// Harbor refused, so nothing was created: a robot made later by someone else must not pass for this one.
		if harbor.IsRefused(err) {
			status.CreatePending = nil
		}

		result, err := r.harborFailed(ctx, state, err)

		return nil, true, result, err
	}

	status.ObservedRotate = robot.secretSpec().Rotate

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
func (r *robotLifecycle) update(ctx context.Context, state *robotReconcile, current *harbor.Robot) (reconcile.Result, error) {
	robot := state.robot

	if current.Duration > 0 && current.ExpiresAt != nil && current.ExpiresAt.Before(time.Now()) {
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonExpired,
			fmt.Sprintf("Harbor robot %q expired at %s", current.Name, current.ExpiresAt.Format(time.RFC3339)))
	}

	if want := robot.desired(); robotDiffers(want, current) {
		err := state.target.update(ctx, state.harbor, current.ID, want)

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

		recordUpdated(r.Recorder, robot.object(), "robot", current.Name, robotDriftFields(want, current)...)

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
func (r *robotLifecycle) currentSecret(ctx context.Context, robot robotResource) (string, string, bool, error) {
	status := robot.status()
	createPending := status.CreatePending != nil
	_, refreshPending := robot.object().GetAnnotations()[harborv1alpha1.AnnotationSecretRefreshPending]

	if createPending || refreshPending || robot.secretSpec().Rotate != status.ObservedRotate {
		return "", "", false, nil
	}

	owned, err := r.ownedSecrets(ctx, robot)
	if err != nil {
		return "", "", false, err
	}

	wanted := make(map[string]bool)
	for _, name := range secrets.Names(robot.object().GetName(), robot.secretSpec()) {
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
func (r *robotLifecycle) refreshSecret(ctx context.Context, state *robotReconcile, current *harbor.Robot) (reconcile.Result, error) {
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

	status := robot.status()
	now := metav1.Now()
	status.LastRotated = &now
	status.ObservedRotate = robot.secretSpec().Rotate

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
func (r *robotLifecycle) secretConflict(ctx context.Context, robot robotResource) (bool, error) {
	object := robot.object()

	for _, name := range secrets.Names(object.GetName(), robot.secretSpec()) {
		have := &corev1.Secret{}

		err := r.Get(ctx, client.ObjectKey{Namespace: object.GetNamespace(), Name: name}, have)

		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return false, err
		case !secrets.IsOwnedBy(have, object):
			return true, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonSecretConflict,
				fmt.Sprintf("Secret %q exists and is not owned by this robot", name))
		}
	}

	return false, nil
}

// replace deletes the robot and creates it again with the spec's fields, for connections whose credentials may create
// and delete robots but not update them.
func (r *robotLifecycle) replace(ctx context.Context, state *robotReconcile, current *harbor.Robot) (reconcile.Result, error) {
	robot := state.robot
	status := robot.status()

	// Harbor refuses a robot that asks for more than its creator holds. Deleting first would leave no robot at all.
	missing, err := state.target.missingPermissions(ctx, state.harbor, robot.desired())
	if err != nil {
		return r.harborFailed(ctx, state, err)
	}

	if len(missing) > 0 {
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonForbidden,
			replaceForbiddenMessage(missing))
	}

	if err := r.markCreatePending(ctx, robot); err != nil {
		return reconcile.Result{}, err
	}

	if err := state.harbor.DeleteRobot(ctx, current.ID); err != nil && !harbor.IsNotFound(err) {
		// Harbor refused the delete, so no create follows: the marker must not outlive the call it was set for.
		if harbor.IsRefused(err) {
			status.CreatePending = nil
		}

		return r.harborFailed(ctx, state, err)
	}

	created, done, result, err := r.createRobot(ctx, state)
	if done {
		return result, err
	}

	r.Recorder.Eventf(robot.object(), nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonReplaced, "Replace",
		"Replaced Harbor robot %q because the connection's credentials cannot update robots", created.Name)

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, robot, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor robot matches the spec")
}

// replaceForbiddenMessage says why a robot that cannot be updated is not replaced, naming the permissions the
// connection lacks.
func replaceForbiddenMessage(names []string) string {
	return "the connection's credentials cannot update the Harbor robot, and the robot is not replaced because they lack these permissions: " +
		strings.Join(names, ", ")
}

// errSourceSecretGone reports that the Secret a robot secret was read from was deleted before it was written back. The
// secret is then lost and must be refreshed: writing it again would bring back the secret the user threw away.
var errSourceSecretGone = errors.New("the Secret holding the robot secret was deleted")

// writeSecrets writes every enabled Secret with the robot's credentials, then deletes the Secrets this robot owns that
// the spec no longer names. conflict is true, and nothing is written, when a target name is taken by a Secret this
// robot does not own. source names the Secret the secret was read from, if any: that Secret is updated but never
// created again, and errSourceSecretGone is returned when it no longer exists.
func (r *robotLifecycle) writeSecrets(ctx context.Context, state *robotReconcile, harborName, secret, source string) (bool, error) {
	robot := state.robot

	if conflict, err := r.secretConflict(ctx, robot); conflict || err != nil {
		return conflict, err
	}

	rendered, err := secrets.Render(secrets.Input{
		Owner: robot.object(), Namespace: robot.object().GetNamespace(), Spec: robot.secretSpec(),
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
func (r *robotLifecycle) writeSecret(ctx context.Context, want *corev1.Secret, mayCreate bool) error {
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
func (r *robotLifecycle) recordID(ctx context.Context, robot robotResource, found *harbor.Robot) error {
	status := robot.status()
	status.RobotID = found.ID
	status.HarborName = found.Name
	status.ExpiresAt = nil

	if found.ExpiresAt != nil && found.Duration > 0 {
		status.ExpiresAt = &metav1.Time{Time: *found.ExpiresAt}
	}

	return r.updateStatus(ctx, robot)
}

// markPending sets a pending annotation, with the current time, unless it is already set.
func (r *robotLifecycle) markPending(ctx context.Context, robot robotResource, annotation string) error {
	object := robot.object()
	if _, ok := object.GetAnnotations()[annotation]; ok {
		return nil
	}

	annotations := object.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	annotations[annotation] = time.Now().UTC().Format(time.RFC3339)
	object.SetAnnotations(annotations)

	return r.Update(ctx, object)
}

// markCreatePending records in status, which namespace users cannot write, that a create call is about to be made,
// unless it is already recorded.
func (r *robotLifecycle) markCreatePending(ctx context.Context, robot robotResource) error {
	status := robot.status()
	if status.CreatePending != nil {
		return nil
	}

	status.CreatePending = &metav1.Time{Time: time.Now().UTC().Truncate(time.Second)}

	return r.updateStatus(ctx, robot)
}

// clearPending clears the create marker and the secret-refresh-pending annotation. Status is written first: an update
// of the resource replaces the in-memory status with the stored one.
func (r *robotLifecycle) clearPending(ctx context.Context, robot robotResource) error {
	status := robot.status()
	if status.CreatePending != nil {
		status.CreatePending = nil

		if err := r.updateStatus(ctx, robot); err != nil {
			return err
		}
	}

	object := robot.object()
	annotations := object.GetAnnotations()

	if _, ok := annotations[harborv1alpha1.AnnotationSecretRefreshPending]; !ok {
		return nil
	}

	delete(annotations, harborv1alpha1.AnnotationSecretRefreshPending)
	object.SetAnnotations(annotations)

	return r.Update(ctx, object)
}

// release deletes the robot's Secrets and removes its finalizer, ending the deletion of the resource.
func (r *robotLifecycle) release(ctx context.Context, robot robotResource) (reconcile.Result, error) {
	if err := r.deleteOwnedSecrets(ctx, robot, nil); err != nil {
		return reconcile.Result{}, err
	}

	object := robot.object()
	controllerutil.RemoveFinalizer(object, RobotFinalizer)

	return reconcile.Result{}, client.IgnoreNotFound(r.Update(ctx, object))
}

// deleteHarborRobot deletes the Harbor robot the resource's status records, once the caller has checked that the
// resource owns it and that its deletion policy is Delete, then releases the resource. A recorded ID that now names
// another robot deletes nothing.
func (r *robotLifecycle) deleteHarborRobot(ctx context.Context, state *robotReconcile) (reconcile.Result, error) {
	robot := state.robot

	found, err := state.harbor.GetRobot(ctx, robot.status().RobotID)

	switch {
	case harbor.IsNotFound(err):
		return r.release(ctx, robot)
	case err != nil:
		return r.harborFailed(ctx, state, err)
	case !state.target.isOwn(found, robot.shortName()):
		// The ID names a robot this resource does not own; nothing is deleted.
		return r.release(ctx, robot)
	}

	if err := state.harbor.DeleteRobot(ctx, found.ID); err != nil && !harbor.IsNotFound(err) {
		return r.harborFailed(ctx, state, err)
	}

	recordDeleted(r.Recorder, robot.object(), "robot", found.Name)

	return r.release(ctx, robot)
}

// ownedSecrets lists the Secrets in the robot's namespace whose controller is this robot.
func (r *robotLifecycle) ownedSecrets(ctx context.Context, robot robotResource) ([]*corev1.Secret, error) {
	object := robot.object()

	var list corev1.SecretList
	if err := r.List(ctx, &list, client.InNamespace(object.GetNamespace()), client.MatchingLabels{secrets.LabelManagedBy: secrets.ManagedByValue}); err != nil {
		return nil, err
	}

	var owned []*corev1.Secret

	for index := range list.Items {
		if secrets.IsOwnedBy(&list.Items[index], object) {
			owned = append(owned, &list.Items[index])
		}
	}

	return owned, nil
}

// deleteOwnedSecrets deletes the Secrets whose controller is this robot, except those named in keep. When the
// resource is deleted garbage collection would also remove them; deleting them here makes them go with the resource.
func (r *robotLifecycle) deleteOwnedSecrets(ctx context.Context, robot robotResource, keep map[string]bool) error {
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
func (r *robotLifecycle) harborFailed(ctx context.Context, state *robotReconcile, err error) (reconcile.Result, error) {
	reason, retry := harbor.MapError(err)
	if statusErr := r.setReady(ctx, state.robot, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	if !retry {
		return reconcile.Result{}, nil
	}

	return reconcile.Result{}, err
}

func setReferenceResolved(robot robotResource, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&robot.status().Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReferenceResolved, Status: status, Reason: reason, Message: message, ObservedGeneration: robot.object().GetGeneration(),
	})
}

// setReady records the Ready condition and emits an event when it changes.
func (r *robotLifecycle) setReady(ctx context.Context, robot robotResource, status metav1.ConditionStatus, reason, message string) error {
	robotStatus := robot.status()
	generation := robot.object().GetGeneration()
	previous := meta.FindStatusCondition(robotStatus.Conditions, harborv1alpha1.ConditionReady)
	changed := previous == nil || previous.Status != status || previous.Reason != reason || previous.Message != message

	robotStatus.ObservedGeneration = generation
	meta.SetStatusCondition(&robotStatus.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: generation,
	})

	if err := r.updateStatus(ctx, robot); err != nil {
		return err
	}

	if changed {
		eventType := corev1.EventTypeNormal
		if status != metav1.ConditionTrue {
			eventType = corev1.EventTypeWarning
		}

		r.Recorder.Eventf(robot.object(), nil, eventType, reason, "Reconcile", "%s", message)
	}

	return nil
}

// updateStatus writes status only when it differs from what the API server has.
func (r *robotLifecycle) updateStatus(ctx context.Context, robot robotResource) error {
	object := robot.object()
	stored := robot.empty()

	if err := r.Get(ctx, client.ObjectKeyFromObject(object), stored.object()); err == nil &&
		stored.object().GetResourceVersion() == object.GetResourceVersion() && apiequality.Semantic.DeepEqual(*stored.status(), *robot.status()) {
		return nil
	}

	return r.Status().Update(ctx, object)
}
