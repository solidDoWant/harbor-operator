package harbor

import (
	"time"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
)

// RobotAccess is one permission: a Harbor resource and action.
type RobotAccess struct {
	Resource string
	Action   string
}

// RobotSpec is what the operator sets on a project robot.
type RobotSpec struct {
	// Name is the name without Harbor's "robot$project+" prefix.
	Name        string
	Description string
	// Duration is the lifetime in days; -1 never expires.
	Duration    int64
	Disabled    bool
	Permissions []RobotAccess
	// Grants, when set, are the robot's permissions per scope, for a system robot. Permissions is then unused.
	Grants []RobotGrant
}

// Robot level names Harbor uses.
const (
	LevelProject = "project"
	LevelSystem  = "system"
)

// RobotGrant is the access a robot has in one scope: kind "system" with namespace "/", or kind "project" with a
// project name or "*" for every project.
type RobotGrant struct {
	Kind      string
	Namespace string
	Access    []RobotAccess
}

// ScopedAccess is one permission in one scope, the unit Harbor robot permissions are compared in.
type ScopedAccess struct {
	Kind      string
	Namespace string
	Resource  string
	Action    string
}

// FlattenGrants returns every permission of the grants as a set.
func FlattenGrants(grants []RobotGrant) map[ScopedAccess]struct{} {
	set := map[ScopedAccess]struct{}{}

	for _, grant := range grants {
		for _, access := range grant.Access {
			set[ScopedAccess{Kind: grant.Kind, Namespace: grant.Namespace, Resource: access.Resource, Action: access.Action}] = struct{}{}
		}
	}

	return set
}

// Robot is a robot as Harbor reports it.
type Robot struct {
	ID int64
	// Name is the full name Harbor assigned, with its prefix.
	Name string
	// ProjectName is the project the robot's permissions are scoped to.
	ProjectName string
	// Secret is set only on create and refresh.
	Secret      string
	ExpiresAt   *time.Time
	Description string
	Duration    int64
	Disabled    bool
	Permissions []RobotAccess
	// Level is "project" or "system".
	Level string
	// Grants are the robot's permissions per scope, as Harbor reports them.
	Grants []RobotGrant
	// CreationTime is when Harbor created the robot. Zero when Harbor did not report it.
	CreationTime time.Time
}

func (s RobotSpec) permissions(projectName string) []*models.RobotPermission {
	if s.Grants != nil {
		result := make([]*models.RobotPermission, 0, len(s.Grants))

		for _, grant := range s.Grants {
			result = append(result, &models.RobotPermission{Kind: grant.Kind, Namespace: grant.Namespace, Access: accessModels(grant.Access)})
		}

		return result
	}

	return []*models.RobotPermission{{Kind: LevelProject, Namespace: projectName, Access: accessModels(s.Permissions)}}
}

func accessModels(permissions []RobotAccess) []*models.Access {
	access := make([]*models.Access, 0, len(permissions))
	for _, permission := range permissions {
		access = append(access, &models.Access{Resource: permission.Resource, Action: permission.Action, Effect: "allow"})
	}

	return access
}

func robotFromModel(model *models.Robot) *Robot {
	robot := &Robot{
		ID:           model.ID,
		Name:         model.Name,
		Secret:       model.Secret,
		Description:  model.Description,
		Disabled:     model.Disable,
		Level:        model.Level,
		CreationTime: time.Time(model.CreationTime).UTC(),
	}

	if model.Duration != nil {
		robot.Duration = *model.Duration
	}

	if model.ExpiresAt > 0 {
		expires := time.Unix(model.ExpiresAt, 0).UTC()
		robot.ExpiresAt = &expires
	}

	robot.Permissions = []RobotAccess{}

	for _, permission := range model.Permissions {
		if permission == nil {
			continue
		}

		if permission.Kind == "project" && robot.ProjectName == "" {
			robot.ProjectName = permission.Namespace
		}

		grant := RobotGrant{Kind: permission.Kind, Namespace: permission.Namespace, Access: []RobotAccess{}}

		for _, access := range permission.Access {
			if access != nil {
				robot.Permissions = append(robot.Permissions, RobotAccess{Resource: access.Resource, Action: access.Action})
				grant.Access = append(grant.Access, RobotAccess{Resource: access.Resource, Action: access.Action})
			}
		}

		robot.Grants = append(robot.Grants, grant)
	}

	return robot
}

// MissingAccess returns the entries of want that are not in held, in want's order and without repeats.
func MissingAccess(want, held []RobotAccess) []RobotAccess {
	have := make(map[RobotAccess]bool, len(held))
	for _, access := range held {
		have[access] = true
	}

	missing := []RobotAccess{}

	for _, access := range want {
		if !have[access] {
			missing = append(missing, access)
			have[access] = true
		}
	}

	return missing
}

// String is the permission as "resource action".
func (a RobotAccess) String() string { return a.Resource + " " + a.Action }
