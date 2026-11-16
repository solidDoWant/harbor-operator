package main

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// permissionsSubcommand is the first argument that selects the permissions printer instead of the manager.
const permissionsSubcommand = "permissions"

// permissionsSpec is the part of a HarborSystemRobot spec that holds permissions.
type permissionsSpec struct {
	SystemPermissions  []harborv1alpha1.SystemRobotAccess `json:"systemPermissions"`
	ProjectPermissions []harborv1alpha1.ProjectPermission `json:"projectPermissions"`
}

// runPermissions prints, as YAML, the permissions a system robot needs to serve the chosen kinds. Robot credentials
// replace robots instead of updating them, so permissions only user credentials need are left out.
func runPermissions(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet(permissionsSubcommand, flag.ContinueOnError)
	flags.SetOutput(errOut)

	kinds := flags.String("kinds", "", "Comma-separated resource kinds to cover, for example HarborProject,HarborRegistry. Empty covers every kind.")
	contentDeletion := flags.Bool("content-deletion", true, "Include the permissions that deleting a project with its contents needs.")

	if err := flags.Parse(args); err != nil {
		return err
	}

	selected, err := parseKinds(*kinds)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)

		return err
	}

	data, err := yaml.Marshal(renderPermissions(selected, *contentDeletion))
	if err != nil {
		return fmt.Errorf("render the permissions: %w", err)
	}

	_, err = out.Write(data)

	return err
}

// parseKinds splits the --kinds value and rejects kinds the operator does not know. An empty value selects every kind.
func parseKinds(value string) ([]string, error) {
	known := map[string]bool{}
	for _, row := range harbor.RequiredPermissions {
		known[row.Kind] = true
	}

	var selected []string

	for _, kind := range strings.Split(value, ",") {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}

		if !known[kind] {
			names := make([]string, 0, len(known))
			for name := range known {
				names = append(names, name)
			}

			sort.Strings(names)

			return nil, fmt.Errorf("unknown kind %q: use one of %s", kind, strings.Join(names, ", "))
		}

		selected = append(selected, kind)
	}

	return selected, nil
}

// renderPermissions merges the table rows of the selected kinds (every kind when none is selected) into sorted,
// duplicate-free system and project permission lists. Project permissions apply to every project.
func renderPermissions(kinds []string, contentDeletion bool) permissionsSpec {
	system := map[harborv1alpha1.SystemRobotAccess]bool{}
	project := map[harborv1alpha1.SystemRobotProjectAccess]bool{}

	for _, row := range harbor.RequiredPermissions {
		if row.UserCredentialsOnly || row.ContentDeletionOnly && !contentDeletion {
			continue
		}

		if len(kinds) > 0 && !slices.Contains(kinds, row.Kind) {
			continue
		}

		if row.Scope == harbor.LevelSystem {
			system[harborv1alpha1.SystemRobotAccess{Resource: row.Resource, Action: row.Action}] = true
		} else {
			project[harborv1alpha1.SystemRobotProjectAccess{Resource: row.Resource, Action: row.Action}] = true
		}
	}

	spec := permissionsSpec{SystemPermissions: []harborv1alpha1.SystemRobotAccess{}, ProjectPermissions: []harborv1alpha1.ProjectPermission{}}

	for access := range system {
		spec.SystemPermissions = append(spec.SystemPermissions, access)
	}

	sort.Slice(spec.SystemPermissions, func(i, j int) bool {
		return accessLess(spec.SystemPermissions[i].Resource, spec.SystemPermissions[i].Action, spec.SystemPermissions[j].Resource, spec.SystemPermissions[j].Action)
	})

	if len(project) > 0 {
		entry := harborv1alpha1.ProjectPermission{Projects: []string{"*"}}
		for access := range project {
			entry.Access = append(entry.Access, access)
		}

		sort.Slice(entry.Access, func(i, j int) bool {
			return accessLess(entry.Access[i].Resource, entry.Access[i].Action, entry.Access[j].Resource, entry.Access[j].Action)
		})

		spec.ProjectPermissions = append(spec.ProjectPermissions, entry)
	}

	return spec
}

func accessLess(resourceA, actionA, resourceB, actionB string) bool {
	if resourceA != resourceB {
		return resourceA < resourceB
	}

	return actionA < actionB
}
