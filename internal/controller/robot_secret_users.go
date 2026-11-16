package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/secrets"
)

// secretUsers returns the names of the connections in namespace whose credentials Secret is one of names, sorted.
func secretUsers(ctx context.Context, reader client.Reader, namespace string, names []string) ([]string, error) {
	var users []string

	for _, name := range names {
		var connections harborv1alpha1.HarborConnectionList
		if err := reader.List(ctx, &connections, client.InNamespace(namespace),
			client.MatchingFields{ConnectionCredentialsSecretIndex: namespace + "/" + name}); err != nil {
			return nil, err
		}

		for index := range connections.Items {
			users = append(users, connections.Items[index].Name)
		}
	}

	slices.Sort(users)

	return slices.Compact(users), nil
}

// secretUsersMessage names the connections that keep the robot's Secrets in use.
func secretUsersMessage(users []string) string {
	return fmt.Sprintf("the robot's Secret is the credentials Secret of HarborConnection %s", strings.Join(users, ", "))
}

// waitForSecretUsers reports DependentsExist and returns true while a connection in the robot's namespace uses a Secret
// the robot owns. It runs before anything else on deletion, because the robot's Secrets go with it by owner reference
// whatever its deletion policy, and the connection would lose its credentials. A Secret the spec names but the robot
// does not own, such as one held by another robot or made by hand, stays when the robot goes, so the robot does not
// wait for its users. Deleting the connection or pointing it elsewhere re-queues the robot through its watch.
func (r *robotLifecycle) waitForSecretUsers(ctx context.Context, robot robotResource) (bool, error) {
	object := robot.object()

	owned, err := r.ownedSecrets(ctx, robot)
	if err != nil {
		return false, err
	}

	names := make([]string, 0, len(owned))
	for _, secret := range owned {
		names = append(names, secret.Name)
	}

	users, err := secretUsers(ctx, r.Client, object.GetNamespace(), names)
	if err != nil || len(users) == 0 {
		return false, err
	}

	return true, r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist, secretUsersMessage(users))
}

// retainedSecretUsers returns the connections that use a Secret the robot owns but the spec no longer names, such as
// one just disabled or renamed. Those Secrets are kept until no connection uses them.
func (r *robotLifecycle) retainedSecretUsers(ctx context.Context, robot robotResource) ([]string, error) {
	object := robot.object()
	wanted := secrets.Names(object.GetName(), robot.secretSpec())

	owned, err := r.ownedSecrets(ctx, robot)
	if err != nil {
		return nil, err
	}

	var retained []string

	for _, secret := range owned {
		if !slices.Contains(wanted, secret.Name) {
			retained = append(retained, secret.Name)
		}
	}

	return secretUsers(ctx, r.Client, object.GetNamespace(), retained)
}

// setSynced records the end of a successful sync: Ready, unless a Secret the spec no longer names is kept for a
// connection that still uses it.
func (r *robotLifecycle) setSynced(ctx context.Context, robot robotResource) error {
	users, err := r.retainedSecretUsers(ctx, robot)
	if err != nil {
		return err
	}

	if len(users) > 0 {
		return r.setReady(ctx, robot, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist, secretUsersMessage(users))
	}

	return r.setReady(ctx, robot, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor robot matches the spec")
}

// robotsUsingSecretOf maps a connection to the robots of one kind in its namespace whose Secret it names: those whose
// enabled output Secret has that name, and the robot that owns the Secret of that name. list lists the robots of the
// kind in a namespace as their keys and spec-named Secrets.
func robotsUsingSecretOf(ctx context.Context, reader client.Reader, connection client.Object, kind string,
	list func(ctx context.Context, namespace string) (map[string][]string, error),
) []reconcile.Request {
	name := connection.(*harborv1alpha1.HarborConnection).Spec.CredentialsSecretRef.Name
	namespace := connection.GetNamespace()

	robots, err := list(ctx, namespace)
	if err != nil {
		return nil
	}

	var requests []reconcile.Request

	for robot, names := range robots {
		if slices.Contains(names, name) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: robot}})
		}
	}

	secret := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		return requests
	}

	if owner := metav1.GetControllerOf(secret); owner != nil && owner.Kind == kind &&
		strings.HasPrefix(owner.APIVersion, harborv1alpha1.GroupVersion.Group+"/") {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: owner.Name}})
	}

	return requests
}
