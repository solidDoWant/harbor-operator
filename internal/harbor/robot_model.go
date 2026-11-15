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
}

// Robot is a project robot as Harbor reports it.
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
}

func (s RobotSpec) permissions(projectName string) []*models.RobotPermission {
	access := make([]*models.Access, 0, len(s.Permissions))
	for _, permission := range s.Permissions {
		access = append(access, &models.Access{Resource: permission.Resource, Action: permission.Action, Effect: "allow"})
	}

	return []*models.RobotPermission{{Kind: "project", Namespace: projectName, Access: access}}
}

func robotFromModel(model *models.Robot) *Robot {
	robot := &Robot{
		ID:          model.ID,
		Name:        model.Name,
		Secret:      model.Secret,
		Description: model.Description,
		Disabled:    model.Disable,
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

		for _, access := range permission.Access {
			if access != nil {
				robot.Permissions = append(robot.Permissions, RobotAccess{Resource: access.Resource, Action: access.Action})
			}
		}
	}

	return robot
}
