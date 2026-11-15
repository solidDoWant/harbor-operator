//go:build integration

package v1alpha1_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

func registry(name string, mutate func(*v1alpha1.HarborRegistry)) *v1alpha1.HarborRegistry {
	obj := &v1alpha1.HarborRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.HarborRegistrySpec{
			ConnectionRef: v1alpha1.ObjectReference{Name: "conn"},
			Type:          "docker-hub",
			URL:           "https://registry-1.docker.io",
		},
	}
	if mutate != nil {
		mutate(obj)
	}

	return obj
}

func retentionRule(template string, count *int32) v1alpha1.RetentionRule {
	return v1alpha1.RetentionRule{
		Template:     template,
		Count:        count,
		Repositories: v1alpha1.RepositorySelector{Decoration: "repoMatches", Pattern: "**"},
		Tags:         v1alpha1.TagSelector{Decoration: "matches", Pattern: "**"},
	}
}

func retention(name string, rules ...v1alpha1.RetentionRule) *v1alpha1.HarborRetentionPolicy {
	return &v1alpha1.HarborRetentionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.HarborRetentionPolicySpec{ProjectRef: v1alpha1.ObjectReference{Name: "proj"}, Rules: rules},
	}
}

func immutabilityRule(pattern string) v1alpha1.ImmutabilityRule {
	return v1alpha1.ImmutabilityRule{
		Repositories: v1alpha1.RepositorySelector{Decoration: "repoMatches", Pattern: "**"},
		Tags:         v1alpha1.ImmutabilityTagSelector{Decoration: "matches", Pattern: pattern},
	}
}

func immutability(name string, rules ...v1alpha1.ImmutabilityRule) *v1alpha1.HarborImmutabilityPolicy {
	return &v1alpha1.HarborImmutabilityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.HarborImmutabilityPolicySpec{ProjectRef: v1alpha1.ObjectReference{Name: "proj"}, Rules: rules},
	}
}

func systemRobot(name string, mutate func(*v1alpha1.HarborSystemRobot)) *v1alpha1.HarborSystemRobot {
	obj := &v1alpha1.HarborSystemRobot{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.HarborSystemRobotSpec{
			ConnectionRef:     v1alpha1.ObjectReference{Name: "conn"},
			SystemPermissions: []v1alpha1.SystemRobotAccess{{Resource: "project", Action: "list"}},
		},
	}
	if mutate != nil {
		mutate(obj)
	}

	return obj
}

func TestPhase2CRDValidation(t *testing.T) {
	kubeClient := newClient(t)

	many := func(n int) []v1alpha1.ImmutabilityRule {
		rules := make([]v1alpha1.ImmutabilityRule, 0, n)
		for i := range n {
			rules = append(rules, immutabilityRule(fmt.Sprintf("v%d", i)))
		}

		return rules
	}

	tests := map[string]struct {
		object   client.Object
		rejected bool
	}{
		"registry minimal":       {object: registry("reg-ok", nil)},
		"registry url userinfo":  {rejected: true, object: registry("reg-userinfo", func(r *v1alpha1.HarborRegistry) { r.Spec.URL = "https://user:pw@example.com" })},
		"registry url trailing":  {rejected: true, object: registry("reg-slash", func(r *v1alpha1.HarborRegistry) { r.Spec.URL = "https://example.com/" })},
		"registry url with path": {object: registry("reg-path", func(r *v1alpha1.HarborRegistry) { r.Spec.URL = "https://example.com/a@b" })},
		"registry bad type":      {rejected: true, object: registry("reg-type", func(r *v1alpha1.HarborRegistry) { r.Spec.Type = "Docker Hub" })},
		"registry bad name":      {rejected: true, object: registry("reg-name", func(r *v1alpha1.HarborRegistry) { r.Spec.Name = "Bad_Name" })},

		"retention latest with count":    {object: retention("ret-ok", retentionRule("latestPushedK", ptr.To[int32](5)))},
		"retention always without count": {object: retention("ret-always", retentionRule("always", nil))},
		"retention count missing":        {rejected: true, object: retention("ret-nocount", retentionRule("nDaysSinceLastPush", nil))},
		"retention count zero":           {rejected: true, object: retention("ret-zero", retentionRule("latestPulledN", ptr.To[int32](0)))},
		"retention always with count":    {rejected: true, object: retention("ret-alwayscount", retentionRule("always", ptr.To[int32](1)))},
		"retention unknown template":     {rejected: true, object: retention("ret-template", retentionRule("sometimes", ptr.To[int32](1)))},
		"retention no rules":             {rejected: true, object: retention("ret-norules")},
		"retention bad decoration": {rejected: true, object: retention("ret-decoration", func() v1alpha1.RetentionRule {
			rule := retentionRule("always", nil)
			rule.Tags.Decoration = "repoMatches"

			return rule
		}())},

		"immutability two distinct rules": {object: immutability("imm-ok", immutabilityRule("a"), immutabilityRule("b"))},
		"immutability duplicate selectors": {rejected: true, object: immutability("imm-dup", immutabilityRule("a"), func() v1alpha1.ImmutabilityRule {
			rule := immutabilityRule("a")
			rule.Disabled = true

			return rule
		}())},
		"immutability 15 rules": {object: immutability("imm-15", many(15)...)},
		"immutability 16 rules": {rejected: true, object: immutability("imm-16", many(16)...)},

		"system robot system permission": {object: systemRobot("sys-ok", nil)},
		"system robot project permission only": {object: systemRobot("sys-proj", func(r *v1alpha1.HarborSystemRobot) {
			r.Spec.SystemPermissions = nil
			r.Spec.ProjectPermissions = []v1alpha1.ProjectPermission{{Projects: []string{"*"}, Access: []v1alpha1.RobotAccess{{Resource: "repository", Action: "pull"}}}}
		})},
		"system robot no permissions": {rejected: true, object: systemRobot("sys-none", func(r *v1alpha1.HarborSystemRobot) { r.Spec.SystemPermissions = nil })},
		"system robot unlisted project action": {rejected: true, object: systemRobot("sys-badaction", func(r *v1alpha1.HarborSystemRobot) {
			r.Spec.ProjectPermissions = []v1alpha1.ProjectPermission{{Projects: []string{"a"}, Access: []v1alpha1.RobotAccess{{Resource: "repository", Action: "explode"}}}}
		})},
		"system robot unlisted system action": {rejected: true, object: systemRobot("sys-badsysaction", func(r *v1alpha1.HarborSystemRobot) {
			r.Spec.SystemPermissions = []v1alpha1.SystemRobotAccess{{Resource: "project", Action: "explode"}}
		})},
		"system robot unlisted system resource": {rejected: true, object: systemRobot("sys-badsysres", func(r *v1alpha1.HarborSystemRobot) {
			r.Spec.SystemPermissions = []v1alpha1.SystemRobotAccess{{Resource: "nothing", Action: "list"}}
		})},
		"system robot bad name": {rejected: true, object: systemRobot("sys-name", func(r *v1alpha1.HarborSystemRobot) { r.Spec.Name = "Bad_Name" })},

		"proxy cache speed -1": {object: project("pc-unlimited", func(p *v1alpha1.HarborProject) {
			p.Spec.ProxyCache = &v1alpha1.ProxyCacheSpec{RegistryRef: v1alpha1.ObjectReference{Name: "reg"}, SpeedLimitKbps: ptr.To[int32](-1)}
		})},
		"proxy cache speed 0": {rejected: true, object: project("pc-zero", func(p *v1alpha1.HarborProject) {
			p.Spec.ProxyCache = &v1alpha1.ProxyCacheSpec{RegistryRef: v1alpha1.ObjectReference{Name: "reg"}, SpeedLimitKbps: ptr.To[int32](0)}
		})},
		"proxy cache speed -2": {rejected: true, object: project("pc-neg", func(p *v1alpha1.HarborProject) {
			p.Spec.ProxyCache = &v1alpha1.ProxyCacheSpec{RegistryRef: v1alpha1.ObjectReference{Name: "reg"}, SpeedLimitKbps: ptr.To[int32](-2)}
		})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := kubeClient.Create(t.Context(), tc.object)
			if tc.rejected {
				require.Error(t, err)
				require.True(t, apierrors.IsInvalid(err), err.Error())

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestPhase2CRDImmutability(t *testing.T) {
	kubeClient := newClient(t)

	withProxy := func(name string) *v1alpha1.HarborProject {
		return project(name, func(p *v1alpha1.HarborProject) {
			p.Spec.ProxyCache = &v1alpha1.ProxyCacheSpec{RegistryRef: v1alpha1.ObjectReference{Name: "reg"}}
		})
	}

	t.Run("proxy cache cannot be added", func(t *testing.T) {
		obj := project("imm-pc-add", nil)
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProxyCache = &v1alpha1.ProxyCacheSpec{RegistryRef: v1alpha1.ObjectReference{Name: "reg"}}
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("proxy cache cannot be removed", func(t *testing.T) {
		obj := withProxy("imm-pc-remove")
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProxyCache = nil
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("proxy cache registry name cannot change", func(t *testing.T) {
		obj := withProxy("imm-pc-name")
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProxyCache.RegistryRef.Name = "other"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("proxy cache registry namespace cannot change", func(t *testing.T) {
		obj := withProxy("imm-pc-ns")
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProxyCache.RegistryRef.Namespace = "elsewhere"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("proxy cache registry namespace spelled out as the default is rejected", func(t *testing.T) {
		// CEL cannot read the object's namespace, so an empty namespace and its default are different values.
		obj := withProxy("imm-pc-ns-same")
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProxyCache.RegistryRef.Namespace = "default"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("proxy cache speed limit can change", func(t *testing.T) {
		obj := withProxy("imm-pc-speed")
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProxyCache.SpeedLimitKbps = ptr.To[int32](100)
		require.NoError(t, kubeClient.Update(t.Context(), obj))
	})

	t.Run("registry type", func(t *testing.T) {
		obj := registry("imm-reg-type", nil)
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.Type = "quay"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("registry url can change", func(t *testing.T) {
		obj := registry("imm-reg-url", nil)
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.URL = "https://other.example.com"
		require.NoError(t, kubeClient.Update(t.Context(), obj))
	})

	t.Run("retention project reference", func(t *testing.T) {
		obj := retention("imm-ret", retentionRule("always", nil))
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProjectRef.Name = "other"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("immutability project reference", func(t *testing.T) {
		obj := immutability("imm-imm", immutabilityRule("a"))
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProjectRef.Name = "other"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("system robot name", func(t *testing.T) {
		obj := systemRobot("imm-sys", func(r *v1alpha1.HarborSystemRobot) { r.Spec.Name = "first" })
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.Name = "second"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})
}
