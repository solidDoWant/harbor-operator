// Package chart_test renders the operator's Helm chart and checks the objects it produces.
package chart_test

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const releaseNamespace = "operator-system"

type rendered struct {
	clusterRoles        map[string]rbacv1.ClusterRole
	clusterRoleBindings map[string]rbacv1.ClusterRoleBinding
	roles               map[string]rbacv1.Role // keyed by namespace/name
	roleBindings        map[string]rbacv1.RoleBinding
	args                []string
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	return filepath.Join(filepath.Dir(file), "..", "..")
}

func render(t *testing.T, args ...string) rendered {
	t.Helper()

	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed: run the tests in the dev shell")
	}

	command := exec.CommandContext(t.Context(), "helm", append([]string{"template", "harbor-operator",
		filepath.Join(repoRoot(t), "deploy", "charts", "harbor-operator"), "--namespace", releaseNamespace}, args...)...)

	var stderr bytes.Buffer

	command.Stderr = &stderr
	output, err := command.Output()
	require.NoError(t, err, stderr.String())

	result := rendered{
		clusterRoles:        map[string]rbacv1.ClusterRole{},
		clusterRoleBindings: map[string]rbacv1.ClusterRoleBinding{},
		roles:               map[string]rbacv1.Role{},
		roleBindings:        map[string]rbacv1.RoleBinding{},
	}

	for document := range strings.SplitSeq(string(output), "\n---\n") {
		var header struct{ Kind string }
		require.NoError(t, yaml.Unmarshal([]byte(document), &header))

		switch header.Kind {
		case "ClusterRole":
			var object rbacv1.ClusterRole
			require.NoError(t, yaml.Unmarshal([]byte(document), &object))
			result.clusterRoles[object.Name] = object
		case "ClusterRoleBinding":
			var object rbacv1.ClusterRoleBinding
			require.NoError(t, yaml.Unmarshal([]byte(document), &object))
			result.clusterRoleBindings[object.Name] = object
		case "Role":
			var object rbacv1.Role
			require.NoError(t, yaml.Unmarshal([]byte(document), &object))
			result.roles[object.Namespace+"/"+object.Name] = object
		case "RoleBinding":
			var object rbacv1.RoleBinding
			require.NoError(t, yaml.Unmarshal([]byte(document), &object))
			result.roleBindings[object.Namespace+"/"+object.Name] = object
		case "Deployment":
			var object appsv1.Deployment
			require.NoError(t, yaml.Unmarshal([]byte(document), &object))
			result.args = object.Spec.Template.Spec.Containers[0].Args
		}
	}

	return result
}

// generatedRules reads the manager's rules that controller-gen writes from the kubebuilder markers.
func generatedRules(t *testing.T) []rbacv1.PolicyRule {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "rbac", "role.yaml"))
	require.NoError(t, err)

	var role rbacv1.ClusterRole
	require.NoError(t, yaml.Unmarshal(data, &role))
	require.NotEmpty(t, role.Rules)

	return role.Rules
}

// grants lists every apiGroup/resource:verb the rules allow, so tests compare permissions, not rule layout.
func grants(rules []rbacv1.PolicyRule) []string {
	var result []string

	for _, rule := range rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					result = append(result, group+"/"+resource+":"+verb)
				}
			}
		}
	}

	return result
}

func withoutResources(rules []rbacv1.PolicyRule, keep func(resource string) bool) []string {
	var result []string

	for _, grant := range grants(rules) {
		resource := strings.SplitN(strings.SplitN(grant, "/", 2)[1], ":", 2)[0]
		if keep(resource) {
			result = append(result, grant)
		}
	}

	return result
}

func TestChartGrantsTheManagerRulesClusterWideWithoutWatchNamespaces(t *testing.T) {
	result := render(t)

	require.Contains(t, result.clusterRoles, "harbor-operator")
	assert.Equal(t, generatedRules(t), result.clusterRoles["harbor-operator"].Rules)
	assert.Equal(t, "harbor-operator", result.clusterRoleBindings["harbor-operator"].RoleRef.Name)
	assert.NotContains(t, result.clusterRoles, "harbor-operator-namespaces")
	assert.Empty(t, result.roles)
	assert.Empty(t, result.roleBindings)

	for _, arg := range result.args {
		assert.NotContains(t, arg, "--watch-namespaces")
	}
}

func TestChartGrantsTheManagerRulesOnlyInWatchedNamespaces(t *testing.T) {
	result := render(t, "--set", "watchNamespaces={team-a,team-b}")
	rules := generatedRules(t)
	subject := rbacv1.Subject{Kind: "ServiceAccount", Name: "harbor-operator", Namespace: releaseNamespace}

	// The only cluster-wide grants left: reading Namespaces, and the metrics endpoint's token and access reviews.
	clusterGrants := []string{}
	for name, role := range result.clusterRoles {
		clusterGrants = append(clusterGrants, grants(role.Rules)...)

		assert.True(t, slices.ContainsFunc(slices.Collect(maps.Values(result.clusterRoleBindings)), func(binding rbacv1.ClusterRoleBinding) bool {
			return binding.RoleRef.Name == name && slices.Contains(binding.Subjects, subject)
		}), "ClusterRole %s is bound to the operator", name)
	}

	assert.ElementsMatch(t, []string{
		"/namespaces:get", "/namespaces:list", "/namespaces:watch",
		"authentication.k8s.io/tokenreviews:create", "authorization.k8s.io/subjectaccessreviews:create",
	}, clusterGrants)
	assert.NotContains(t, result.clusterRoleBindings, "harbor-operator", "the cluster-wide binding is renamed so its roleRef never changes")

	for _, namespace := range []string{"team-a", "team-b"} {
		key := namespace + "/harbor-operator"
		require.Contains(t, result.roles, key)
		assert.ElementsMatch(t,
			withoutResources(rules, func(resource string) bool { return resource != "namespaces" && resource != "leases" }),
			grants(result.roles[key].Rules))
		assert.Equal(t, "harbor-operator", result.roleBindings[key].RoleRef.Name)
		assert.Equal(t, "Role", result.roleBindings[key].RoleRef.Kind)
		assert.Equal(t, []rbacv1.Subject{subject}, result.roleBindings[key].Subjects)
	}

	leaderElection := releaseNamespace + "/harbor-operator-leader-election"
	require.Contains(t, result.roles, leaderElection)
	assert.ElementsMatch(t,
		withoutResources(rules, func(resource string) bool { return resource == "leases" || resource == "events" }),
		grants(result.roles[leaderElection].Rules))
	assert.Equal(t, []rbacv1.Subject{subject}, result.roleBindings[leaderElection].Subjects)
	assert.Len(t, result.roles, 3)

	assert.Contains(t, result.args, "--watch-namespaces=team-a,team-b")
}
