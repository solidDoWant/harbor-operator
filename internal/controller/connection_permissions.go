package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// maxListedPermissions bounds the missing permissions a condition message names.
const maxListedPermissions = 10

// requiredPermission is one permission the connection's credentials need, at system scope when projectID is 0.
type requiredPermission struct {
	projectID   int64
	projectName string
	access      harbor.RobotAccess
	// userOnly is set when robot credentials do not need it.
	userOnly bool
}

// requiredPermissions collects what the admitted dependents of a connection need.
type requiredPermissions struct {
	entries []requiredPermission
	// projects names each project the entries are checked in, by ID.
	projects map[int64]string
}

// addKind adds every table row of kind: system rows at system scope, project rows in the project when its ID is known.
func (s *requiredPermissions) addKind(kind string, projectID int64, projectName string) {
	for _, row := range harbor.RequiredPermissions {
		if row.Kind != kind {
			continue
		}

		access := harbor.RobotAccess{Resource: row.Resource, Action: row.Action}

		switch {
		case row.Scope == harbor.LevelSystem:
			s.add(0, "", access, row.UserCredentialsOnly)
		case projectID != 0:
			s.add(projectID, projectName, access, row.UserCredentialsOnly)
		}
	}
}

func (s *requiredPermissions) add(projectID int64, projectName string, access harbor.RobotAccess, userOnly bool) {
	if projectID != 0 {
		s.projects[projectID] = projectName
	}

	s.entries = append(s.entries, requiredPermission{projectID: projectID, projectName: projectName, access: access, userOnly: userOnly})
}

// permissionsResult is the PermissionsSufficient condition one check arrives at.
type permissionsResult struct {
	status  metav1.ConditionStatus
	reason  string
	message string
}

// checkPermissions compares what the connection's admitted dependents need with what its credentials hold. Resources in
// namespaces the connection, or their project, does not admit are left out, so they cannot change the result.
func (r *HarborConnectionReconciler) checkPermissions(ctx context.Context, connection *harborv1alpha1.HarborConnection, harborClient *harbor.Client) permissionsResult {
	required, err := r.requiredPermissions(ctx, connection, harborClient)
	if err == nil {
		var held *harbor.HeldPermissions

		held, err = harborClient.ReadHeldPermissions(ctx, slices.Sorted(maps.Keys(required.projects)))
		if err == nil {
			return permissionsResultFor(missingPermissions(required, held))
		}
	}

	reason, _ := harbor.MapError(err)

	return permissionsResult{status: metav1.ConditionUnknown, reason: reason, message: "checking permissions failed: " + err.Error()}
}

func permissionsResultFor(missing []string) permissionsResult {
	if len(missing) == 0 {
		return permissionsResult{status: metav1.ConditionTrue, reason: harborv1alpha1.ReasonPermissionsSufficient, message: "the credentials hold every permission the resources using this connection need"}
	}

	listed, suffix := missing, ""
	if len(listed) > maxListedPermissions {
		listed, suffix = listed[:maxListedPermissions], fmt.Sprintf(" and %d more", len(missing)-maxListedPermissions)
	}

	return permissionsResult{
		status: metav1.ConditionFalse, reason: harborv1alpha1.ReasonInsufficientPermissions,
		message: "the credentials lack " + strings.Join(listed, ", ") + suffix,
	}
}

// missingPermissions names each required permission not held, system scope first, then by project name, without
// repeats.
func missingPermissions(required *requiredPermissions, held *harbor.HeldPermissions) []string {
	seen := map[string]bool{}

	var system, projects []string

	for _, entry := range required.entries {
		if entry.userOnly && held.Robot {
			continue
		}

		want := []harbor.RobotAccess{entry.access}
		name := entry.access.Resource + ":" + entry.access.Action

		if entry.projectID == 0 {
			if len(held.MissingSystem(want)) > 0 && !seen["system "+name] {
				seen["system "+name] = true
				system = append(system, "system "+name)
			}

			continue
		}

		entryName := "project " + entry.projectName + " " + name
		if len(held.MissingInProject(entry.projectID, want)) > 0 && !seen[entryName] {
			seen[entryName] = true
			projects = append(projects, entryName)
		}
	}

	slices.Sort(system)
	slices.Sort(projects)

	return append(system, projects...)
}

// requiredPermissions lists what the connection's admitted dependents need: the table rows of each kind with at least
// one, plus every permission the robots among them request.
func (r *HarborConnectionReconciler) requiredPermissions(ctx context.Context, connection *harborv1alpha1.HarborConnection, harborClient *harbor.Client) (*requiredPermissions, error) {
	required := &requiredPermissions{projects: map[int64]string{}}
	key := client.ObjectKeyFromObject(connection).String()

	var projects harborv1alpha1.HarborProjectList
	if err := r.List(ctx, &projects, client.MatchingFields{ProjectConnectionIndex: key}); err != nil {
		return nil, err
	}

	for index := range projects.Items {
		project := &projects.Items[index]

		admitted, err := access.Admits(ctx, r.Client, connection.Spec.AllowedNamespaces, connection.Namespace, project.Namespace)
		if err != nil {
			return nil, err
		}

		if !admitted {
			continue
		}

		if err := r.addProjectDependents(ctx, required, project); err != nil {
			return nil, err
		}
	}

	var registries harborv1alpha1.HarborRegistryList
	if err := r.List(ctx, &registries, client.MatchingFields{RegistryConnectionIndex: key}); err != nil {
		return nil, err
	}

	for index := range registries.Items {
		admitted, err := access.Admits(ctx, r.Client, connection.Spec.SystemAllowedNamespaces, connection.Namespace, registries.Items[index].Namespace)
		if err != nil {
			return nil, err
		}

		if admitted {
			required.addKind("HarborRegistry", 0, "")

			break
		}
	}

	var robots harborv1alpha1.HarborSystemRobotList
	if err := r.List(ctx, &robots, client.MatchingFields{SystemRobotConnectionIndex: key}); err != nil {
		return nil, err
	}

	for index := range robots.Items {
		robot := &robots.Items[index]

		admitted, err := access.Admits(ctx, r.Client, connection.Spec.SystemAllowedNamespaces, connection.Namespace, robot.Namespace)
		if err != nil {
			return nil, err
		}

		if !admitted {
			continue
		}

		if err := addSystemRobot(ctx, required, harborClient, robot); err != nil {
			return nil, err
		}
	}

	return required, nil
}

// addProjectDependents adds what an admitted project needs, and what its robots and policies in namespaces it admits
// need in it. A project without a Harbor ID yet is checked at system scope only.
func (r *HarborConnectionReconciler) addProjectDependents(ctx context.Context, required *requiredPermissions, project *harborv1alpha1.HarborProject) error {
	id, name := int64(project.Status.ProjectID), projectName(project)
	required.addKind("HarborProject", id, name)

	projectKey := client.ObjectKeyFromObject(project).String()
	admits := func(namespace string) (bool, error) {
		return access.Admits(ctx, r.Client, project.Spec.AllowedNamespaces, project.Namespace, namespace)
	}

	var robots harborv1alpha1.HarborProjectRobotList
	if err := r.List(ctx, &robots, client.MatchingFields{RobotProjectIndex: projectKey}); err != nil {
		return err
	}

	for index := range robots.Items {
		robot := &robots.Items[index]

		admitted, err := admits(robot.Namespace)
		if err != nil {
			return err
		}

		if !admitted {
			continue
		}

		required.addKind("HarborProjectRobot", id, name)

		if id != 0 {
			for _, permission := range desiredRobot(robot).Permissions {
				required.add(id, name, permission, false)
			}
		}
	}

	var retention harborv1alpha1.HarborRetentionPolicyList
	if err := r.List(ctx, &retention, client.MatchingFields{RetentionProjectIndex: projectKey}); err != nil {
		return err
	}

	for index := range retention.Items {
		admitted, err := admits(retention.Items[index].Namespace)
		if err != nil {
			return err
		}

		if admitted {
			required.addKind("HarborRetentionPolicy", id, name)

			break
		}
	}

	var immutability harborv1alpha1.HarborImmutabilityPolicyList
	if err := r.List(ctx, &immutability, client.MatchingFields{ImmutabilityProjectIndex: projectKey}); err != nil {
		return err
	}

	for index := range immutability.Items {
		admitted, err := admits(immutability.Items[index].Namespace)
		if err != nil {
			return err
		}

		if admitted {
			required.addKind("HarborImmutabilityPolicy", id, name)

			break
		}
	}

	return nil
}

// addSystemRobot adds what managing an admitted system robot needs, and the permissions it requests: system grants at
// system scope, project grants in each named project Harbor has. A grant on every project is checked in one project
// already known to the check. Projects that do not exist yet are not checked.
func addSystemRobot(ctx context.Context, required *requiredPermissions, harborClient *harbor.Client, robot *harborv1alpha1.HarborSystemRobot) error {
	required.addKind("HarborSystemRobot", 0, "")

	ids := map[string]int64{}

	for _, name := range namedProjects(robot) {
		project, err := harborClient.GetProjectByName(ctx, name)

		switch {
		case harbor.IsNotFound(err) || harbor.IsForbidden(err):
			continue
		case err != nil:
			return err
		}

		ids[name] = project.ID
		required.addKind("HarborSystemRobot", project.ID, name)
	}

	for _, grant := range desiredSystemRobot(robot).Grants {
		if grant.Kind == harbor.LevelSystem {
			for _, permission := range grant.Access {
				required.add(0, "", permission, false)
			}

			continue
		}

		id, name := ids[grant.Namespace], grant.Namespace
		if grant.Namespace == allProjects {
			id, name = anyKnownProject(required.projects)
		}

		if id == 0 {
			continue
		}

		for _, permission := range grant.Access {
			required.add(id, name, permission, false)
		}
	}

	return nil
}

// anyKnownProject returns the project with the lowest ID among those already in the check, or 0 when there is none.
func anyKnownProject(projects map[int64]string) (int64, string) {
	var lowest int64

	for id := range projects {
		if lowest == 0 || id < lowest {
			lowest = id
		}
	}

	return lowest, projects[lowest]
}

// setPermissionsCondition records the result and emits a Warning event when the missing set changes and a Normal event
// when it is resolved. It does not write status.
func (r *HarborConnectionReconciler) setPermissionsCondition(connection *harborv1alpha1.HarborConnection, result permissionsResult) func() {
	previous := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionPermissionsSufficient)

	var emit func()

	switch {
	case result.status == metav1.ConditionFalse && (previous == nil || previous.Status != metav1.ConditionFalse || previous.Message != result.message):
		emit = func() {
			r.Recorder.Eventf(connection, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonInsufficientPermissions, "CheckPermissions", "%s", result.message)
		}
	case result.status == metav1.ConditionTrue && previous != nil && previous.Status == metav1.ConditionFalse:
		emit = func() {
			r.Recorder.Eventf(connection, nil, corev1.EventTypeNormal, harborv1alpha1.ReasonPermissionsSufficient, "CheckPermissions", "%s", result.message)
		}
	default:
		emit = func() {}
	}

	meta.SetStatusCondition(&connection.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionPermissionsSufficient, Status: result.status, Reason: result.reason, Message: result.message,
		ObservedGeneration: connection.Generation,
	})

	return emit
}

// onCreateOrTerminating enqueues what created returns when a dependent is created, so the connection checks its
// permissions again, and what changed returns on every other event.
func onCreateOrTerminating[T client.Object](created, changed func(context.Context, T) []reconcile.Request) handler.EventHandler {
	enqueue := func(queue workqueue.TypedRateLimitingInterface[reconcile.Request], requests []reconcile.Request) {
		for _, request := range requests {
			queue.Add(request)
		}
	}

	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if object, ok := e.Object.(T); ok {
				enqueue(queue, created(ctx, object))
			}
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if object, ok := e.ObjectNew.(T); ok {
				enqueue(queue, changed(ctx, object))
			}
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if object, ok := e.Object.(T); ok {
				enqueue(queue, changed(ctx, object))
			}
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if object, ok := e.Object.(T); ok {
				enqueue(queue, changed(ctx, object))
			}
		},
	}
}

// connectionOfProject returns the connection of the project at key, for a dependent created in it.
func (r *HarborConnectionReconciler) connectionOfProject(ctx context.Context, key types.NamespacedName) []reconcile.Request {
	var project harborv1alpha1.HarborProject
	if err := r.Get(ctx, key, &project); err != nil {
		return nil
	}

	return []reconcile.Request{{NamespacedName: connectionKey(&project)}}
}

func always(key types.NamespacedName) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: key}}
}

func never[T any](context.Context, T) []reconcile.Request { return nil }
