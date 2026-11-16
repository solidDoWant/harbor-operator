package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

func printPermissions(t *testing.T, args ...string) (permissionsSpec, error) {
	t.Helper()

	var out, errOut bytes.Buffer

	if err := runPermissions(args, &out, &errOut); err != nil {
		return permissionsSpec{}, err
	}

	var spec permissionsSpec
	require.NoError(t, yaml.UnmarshalStrict(out.Bytes(), &spec))

	return spec, nil
}

func projectAccess(spec permissionsSpec) []harborv1alpha1.SystemRobotProjectAccess {
	if len(spec.ProjectPermissions) == 0 {
		return nil
	}

	return spec.ProjectPermissions[0].Access
}

func TestPermissionsPrintsEveryKindForAllProjects(t *testing.T) {
	spec, err := printPermissions(t)
	require.NoError(t, err)

	require.Len(t, spec.ProjectPermissions, 1)
	assert.Equal(t, []string{"*"}, spec.ProjectPermissions[0].Projects)
	assert.Contains(t, spec.SystemPermissions, harborv1alpha1.SystemRobotAccess{Resource: "quota", Action: "list"})
	assert.Contains(t, spec.SystemPermissions, harborv1alpha1.SystemRobotAccess{Resource: "registry", Action: "create"})
	assert.Contains(t, projectAccess(spec), harborv1alpha1.SystemRobotProjectAccess{Resource: "tag-retention", Action: "create"})
	assert.Contains(t, projectAccess(spec), harborv1alpha1.SystemRobotProjectAccess{Resource: "repository", Action: "delete"})
	assert.NotContains(t, spec.SystemPermissions, harborv1alpha1.SystemRobotAccess{Resource: "robot", Action: "update"})
	assert.NotContains(t, projectAccess(spec), harborv1alpha1.SystemRobotProjectAccess{Resource: "robot", Action: "update"})
}

func TestPermissionsLeavesOutRepositoryPermissionsWithoutContentDeletion(t *testing.T) {
	spec, err := printPermissions(t, "--content-deletion=false")
	require.NoError(t, err)

	assert.NotContains(t, projectAccess(spec), harborv1alpha1.SystemRobotProjectAccess{Resource: "repository", Action: "delete"})
	assert.NotContains(t, projectAccess(spec), harborv1alpha1.SystemRobotProjectAccess{Resource: "repository", Action: "list"})
	assert.Contains(t, projectAccess(spec), harborv1alpha1.SystemRobotProjectAccess{Resource: "project", Action: "read"})
}

func TestPermissionsCoversOnlyTheChosenKinds(t *testing.T) {
	spec, err := printPermissions(t, "--kinds", "HarborRegistry")
	require.NoError(t, err)

	assert.Equal(t, []harborv1alpha1.SystemRobotAccess{
		{Resource: "registry", Action: "create"},
		{Resource: "registry", Action: "delete"},
		{Resource: "registry", Action: "list"},
		{Resource: "registry", Action: "read"},
		{Resource: "registry", Action: "update"},
	}, spec.SystemPermissions)
	assert.Empty(t, spec.ProjectPermissions)
}

func TestPermissionsRejectsAnUnknownKind(t *testing.T) {
	_, err := printPermissions(t, "--kinds", "HarborNothing")
	require.ErrorContains(t, err, `unknown kind "HarborNothing"`)
}
