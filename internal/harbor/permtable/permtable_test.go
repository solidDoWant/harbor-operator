package permtable_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/harbor/permtable"
)

const (
	harborDir     = "../"
	controllerDir = "../../controller"
	apiDir        = "../../../api/v1alpha1"
)

func TestEveryClientMethodAReconcilerUsesIsAnnotated(t *testing.T) {
	t.Parallel()

	clientMethods, err := permtable.ClientMethods(harborDir)
	require.NoError(t, err)

	methods, err := permtable.ParseMethods(harborDir)
	require.NoError(t, err)

	for _, kind := range permtable.KindFiles {
		for _, file := range kind.Files {
			called, err := permtable.CalledMethods(filepath.Join(controllerDir, file), clientMethods)
			require.NoError(t, err)

			for _, name := range called {
				_, annotated := methods[name]
				assert.True(t, annotated, "%s calls Client.%s without a //harbor:permission annotation", file, name)
			}
		}
	}
}

func TestBuildFailsOnUnannotatedMethod(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.go"), []byte("package harbor\n\ntype Client struct{}\n\nfunc (c *Client) GetThing() {}\n"), 0o600))

	controllers := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(controllers, "harborproject_controller.go"), []byte("package controller\n\nfunc f(c interface{ GetThing() }) { c.GetThing() }\n"), 0o600))

	for _, kind := range permtable.KindFiles[1:] {
		for _, file := range kind.Files {
			require.NoError(t, os.WriteFile(filepath.Join(controllers, file), []byte("package controller\n"), 0o600))
		}
	}

	_, err := permtable.Build(dir, controllers)
	require.ErrorContains(t, err, "GetThing")
}

func TestTableContent(t *testing.T) {
	t.Parallel()

	table, err := permtable.Build(harborDir, controllerDir)
	require.NoError(t, err)

	has := func(kind, scope, resource, action string) (permtable.Entry, bool) {
		for _, entry := range table {
			if entry.Kind == kind && entry.Scope == scope && entry.Resource == resource && entry.Action == action {
				return entry, true
			}
		}

		return permtable.Entry{}, false
	}

	for _, want := range [][4]string{
		{"HarborProject", "system", "project", "create"},
		{"HarborProject", "system", "quota", "update"},
		{"HarborProjectRobot", "project", "robot", "create"},
		{"HarborRetentionPolicy", "project", "tag-retention", "create"},
		{"HarborImmutabilityPolicy", "project", "immutable-tag", "create"},
		{"HarborSystemRobot", "system", "robot", "create"},
		{"HarborRegistry", "system", "registry", "create"},
	} {
		_, found := has(want[0], want[1], want[2], want[3])
		assert.True(t, found, "%v missing", want)
	}

	for _, kind := range []string{"HarborProjectRobot", "HarborSystemRobot"} {
		scope := map[string]string{"HarborProjectRobot": "project", "HarborSystemRobot": "system"}[kind]
		entry, found := has(kind, scope, "robot", "update")
		require.True(t, found)
		assert.True(t, entry.UserCredentialsOnly, "robot:update is only needed of user credentials")
	}

	_, wrong := has("HarborProjectRobot", "system", "robot", "create")
	assert.False(t, wrong, "project robots are project scope")
}

func TestGeneratedTableMatchesAnnotations(t *testing.T) {
	t.Parallel()

	table, err := permtable.Build(harborDir, controllerDir)
	require.NoError(t, err)
	require.Len(t, harbor.RequiredPermissions, len(table))

	for index, entry := range table {
		assert.Equal(t, harbor.RequiredPermission(entry), harbor.RequiredPermissions[index])
	}
}

// Project-scope project read, update and delete are not offered to project robots; system robots get them through
// their project permissions.
var projectScopeOnlyForSystemRobots = map[string]bool{"project:read": true, "project:update": true, "project:delete": true}

func TestEveryRequiredPermissionIsInTheAllowLists(t *testing.T) {
	t.Parallel()

	system := allowList(t, "harborsystemrobot_types.go")
	project := allowList(t, "harborprojectrobot_types.go")
	require.NotEmpty(t, system)
	require.NotEmpty(t, project)

	for _, entry := range harbor.RequiredPermissions {
		pair := entry.Resource + ":" + entry.Action

		switch entry.Scope {
		case "system":
			assert.True(t, system[pair], "%s needs system %s, which no robot can be granted", entry.Kind, pair)
		case "project":
			assert.True(t, project[pair] || projectScopeOnlyForSystemRobots[pair], "%s needs project %s, which no robot can be granted", entry.Kind, pair)
		default:
			t.Errorf("unknown scope %q", entry.Scope)
		}
	}
}

func allowList(t *testing.T, file string) map[string]bool {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(apiDir, file))
	require.NoError(t, err)

	rule := regexp.MustCompile(`XValidation:rule="\[([^\]]*)\]\.exists`).FindStringSubmatch(string(content))
	require.NotNil(t, rule, file)

	list := map[string]bool{}
	for _, item := range strings.Split(rule[1], ",") {
		list[strings.Trim(item, "'")] = true
	}

	return list
}
