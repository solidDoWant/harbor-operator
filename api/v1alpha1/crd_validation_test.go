//go:build integration

package v1alpha1_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

func newClient(t *testing.T) client.Client {
	t.Helper()

	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	runtimeScheme, err := scheme.New()
	require.NoError(t, err)
	kubeClient, err := client.New(cfg, client.Options{Scheme: runtimeScheme})
	require.NoError(t, err)

	return kubeClient
}

func project(name string, mutate func(*v1alpha1.HarborProject)) *v1alpha1.HarborProject {
	obj := &v1alpha1.HarborProject{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.HarborProjectSpec{ConnectionRef: v1alpha1.ObjectReference{Name: "conn"}},
	}
	if mutate != nil {
		mutate(obj)
	}

	return obj
}

func robot(name string, mutate func(*v1alpha1.HarborProjectRobot)) *v1alpha1.HarborProjectRobot {
	obj := &v1alpha1.HarborProjectRobot{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.HarborProjectRobotSpec{
			ProjectRef:  v1alpha1.ObjectReference{Name: "proj"},
			Permissions: []v1alpha1.RobotAccess{{Resource: "repository", Action: "push"}},
		},
	}
	if mutate != nil {
		mutate(obj)
	}

	return obj
}

func TestCRDValidation(t *testing.T) {
	kubeClient := newClient(t)
	quota := resource.MustParse("5Gi")
	zero := resource.MustParse("0")

	tests := map[string]struct {
		object   client.Object
		rejected bool
	}{
		"minimal project": {object: project("ok", nil)},
		"quota alone":     {object: project("quota", func(p *v1alpha1.HarborProject) { p.Spec.StorageQuota = &quota })},
		"unlimited alone": {object: project("unlimited", func(p *v1alpha1.HarborProject) { p.Spec.UnlimitedStorage = true })},
		"quota with unlimited": {rejected: true, object: project("both", func(p *v1alpha1.HarborProject) {
			p.Spec.StorageQuota = &quota
			p.Spec.UnlimitedStorage = true
		})},
		"zero quota": {rejected: true, object: project("zero", func(p *v1alpha1.HarborProject) { p.Spec.StorageQuota = &zero })},
		"system allowlist with cve list": {rejected: true, object: project("sysallow", func(p *v1alpha1.HarborProject) {
			p.Spec.Vulnerability = &v1alpha1.VulnerabilitySpec{
				UseSystemAllowlist: ptr.To(true),
				CVEAllowlist:       &v1alpha1.CVEAllowlist{Items: []string{"CVE-2024-1"}},
			}
		})},
		"cve list with system allowlist false": {object: project("nosysallow", func(p *v1alpha1.HarborProject) {
			p.Spec.Vulnerability = &v1alpha1.VulnerabilitySpec{
				UseSystemAllowlist: ptr.To(false),
				CVEAllowlist:       &v1alpha1.CVEAllowlist{Items: []string{"CVE-2024-1"}},
			}
		})},
		"bad project name":  {rejected: true, object: project("badname", func(p *v1alpha1.HarborProject) { p.Spec.Name = "Bad_Name" })},
		"listed permission": {object: robot("perm", nil)},
		"unlisted action": {rejected: true, object: robot("badaction", func(r *v1alpha1.HarborProjectRobot) {
			r.Spec.Permissions = []v1alpha1.RobotAccess{{Resource: "repository", Action: "explode"}}
		})},
		"unlisted resource": {rejected: true, object: robot("badresource", func(r *v1alpha1.HarborProjectRobot) {
			r.Spec.Permissions = []v1alpha1.RobotAccess{{Resource: "system", Action: "push"}}
		})},
		"project read refused": {rejected: true, object: robot("proj-read", func(r *v1alpha1.HarborProjectRobot) {
			r.Spec.Permissions = []v1alpha1.RobotAccess{{Resource: "project", Action: "read"}}
		})},
		"project update refused": {rejected: true, object: robot("proj-update", func(r *v1alpha1.HarborProjectRobot) {
			r.Spec.Permissions = []v1alpha1.RobotAccess{{Resource: "project", Action: "update"}}
		})},
		"project delete refused": {rejected: true, object: robot("proj-delete", func(r *v1alpha1.HarborProjectRobot) {
			r.Spec.Permissions = []v1alpha1.RobotAccess{{Resource: "project", Action: "delete"}}
		})},
		"no permissions":      {rejected: true, object: robot("noperms", func(r *v1alpha1.HarborProjectRobot) { r.Spec.Permissions = nil })},
		"duration -2":         {rejected: true, object: robot("durneg", func(r *v1alpha1.HarborProjectRobot) { r.Spec.Duration = -2 })},
		"duration 1":          {object: robot("dur1", func(r *v1alpha1.HarborProjectRobot) { r.Spec.Duration = 1 })},
		"duration 2147483646": {object: robot("dur-max", func(r *v1alpha1.HarborProjectRobot) { r.Spec.Duration = 2147483646 })},
		"duration 2147483647": {rejected: true, object: robot("dur-over", func(r *v1alpha1.HarborProjectRobot) { r.Spec.Duration = 2147483647 })},
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

func TestCRDImmutability(t *testing.T) {
	kubeClient := newClient(t)

	t.Run("project name", func(t *testing.T) {
		obj := project("imm-project", func(p *v1alpha1.HarborProject) { p.Spec.Name = "first" })
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.Name = "second"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("project name defaulted from metadata then set differently", func(t *testing.T) {
		obj := project("imm-project2", nil)
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.Name = "other"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("project mutable fields", func(t *testing.T) {
		obj := project("imm-project3", nil)
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.Public = ptr.To(true)
		require.NoError(t, kubeClient.Update(t.Context(), obj))
	})

	t.Run("robot name", func(t *testing.T) {
		obj := robot("imm-robot", func(r *v1alpha1.HarborProjectRobot) { r.Spec.Name = "first" })
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.Name = "second"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("project connection reference", func(t *testing.T) {
		obj := project("imm-conn", nil)
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ConnectionRef.Name = "other"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})

	t.Run("robot project reference", func(t *testing.T) {
		obj := robot("imm-ref", nil)
		require.NoError(t, kubeClient.Create(t.Context(), obj))
		obj.Spec.ProjectRef.Name = "other"
		require.True(t, apierrors.IsInvalid(kubeClient.Update(t.Context(), obj)))
	})
}
