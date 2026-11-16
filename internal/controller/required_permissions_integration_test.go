//go:build integration

package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// requiredSetRobotPermissions is exactly what the generated table lists for every kind, plus the push and pull the
// test's project robot asks for (the connection must hold what its robots request). A permission is left out when
// omit returns true for it.
func requiredSetRobotPermissions(omit func(harbor.RequiredPermission) bool) []*models.RobotPermission {
	system := &models.RobotPermission{Kind: "system", Namespace: "/"}
	project := &models.RobotPermission{Kind: "project", Namespace: "*"}
	seen := map[string]bool{}

	add := func(target *models.RobotPermission, resource, action string) {
		key := target.Kind + resource + action
		if seen[key] {
			return
		}

		seen[key] = true

		target.Access = append(target.Access, &models.Access{Resource: resource, Action: action, Effect: "allow"})
	}

	for _, required := range harbor.RequiredPermissions {
		// The robot signs in as itself, so it never needs the permissions only user credentials need.
		if required.UserCredentialsOnly || (omit != nil && omit(required)) {
			continue
		}

		if required.Scope == "system" {
			add(system, required.Resource, required.Action)
		} else {
			add(project, required.Resource, required.Action)
		}
	}

	add(project, "repository", "push")
	add(project, "repository", "pull")

	return []*models.RobotPermission{system, project}
}

func TestEveryKindBecomesReadyWithExactlyTheRequiredPermissions(t *testing.T) {
	t.Parallel()

	instance := harborfixture.FromEnv(t)
	projectName := harborfixture.UniqueName(t, "perm")
	t.Cleanup(func() { instance.DeleteProject(context.Background(), t, projectName) })

	cl := startOperator(t)
	connectionInstance := *instance
	connectionInstance.Username, connectionInstance.Password = instance.CreateSystemRobot(t, requiredSetRobotPermissions(nil))

	namespace := newNamespace(t, cl, "perm")
	connection := readyConnection(t, cl, &connectionInstance, namespace, shortConnectionResync)

	project := newProject(namespace, projectName, connection)
	fullSpec(project, time.Now().Add(48*time.Hour))
	require.NoError(t, cl.Create(t.Context(), project))
	project = awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	robot := ciRobot(namespace, project)
	require.NoError(t, cl.Create(t.Context(), robot))
	awaitRobot(t, cl, client.ObjectKeyFromObject(robot), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	retention := keepVersionsAndRecentPulls(namespace, "keep", project)
	require.NoError(t, cl.Create(t.Context(), retention))
	awaitRetention(t, cl, client.ObjectKeyFromObject(retention), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	immutability := immutabilityPolicy(namespace, project, immutabilityRule("v*", false))
	require.NoError(t, cl.Create(t.Context(), immutability))
	awaitImmutabilityPolicy(t, cl, client.ObjectKeyFromObject(immutability), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	awaitPermissions(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonPermissionsSufficient)
}

func TestConnectionNamesMissingQuotaListPermission(t *testing.T) {
	t.Parallel()

	instance := harborfixture.FromEnv(t)
	projectName := harborfixture.UniqueName(t, "perm")
	t.Cleanup(func() { instance.DeleteProject(context.Background(), t, projectName) })

	cl := startOperator(t)
	connectionInstance := *instance
	connectionInstance.Username, connectionInstance.Password = instance.CreateSystemRobot(t, requiredSetRobotPermissions(func(required harbor.RequiredPermission) bool {
		return required.Resource == "quota" && required.Action == "list"
	}))

	namespace := newNamespace(t, cl, "perm")
	connection := readyConnection(t, cl, &connectionInstance, namespace, shortConnectionResync)

	project := newProject(namespace, projectName, connection)
	require.NoError(t, cl.Create(t.Context(), project))
	awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	condition := awaitPermissions(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionFalse, harborv1alpha1.ReasonInsufficientPermissions)
	require.Contains(t, condition.Message, "system quota:list")
}
