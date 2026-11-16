package harbor

import (
	"context"
	"net/http"
	"strconv"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/user"
)

// HeldPermissions is what the connection's credentials may do in Harbor, read by ReadHeldPermissions.
type HeldPermissions struct {
	// All is set for a Harbor system administrator, who holds every permission.
	All bool
	// Robot is set when the credentials are a robot's. Robots replace robots instead of updating them, so they never
	// need permissions only user credentials use.
	Robot bool
	// System is what the credentials hold at system scope.
	System []RobotAccess
	// Projects is what the credentials hold in each project read, by project ID.
	Projects map[int64][]RobotAccess
}

// MissingSystem returns the entries of want not held at system scope, in want's order.
func (h *HeldPermissions) MissingSystem(want []RobotAccess) []RobotAccess {
	if h.All {
		return []RobotAccess{}
	}

	return MissingAccess(want, h.System)
}

// MissingInProject returns the entries of want not held in the project, in want's order. A project that was not read
// holds nothing.
func (h *HeldPermissions) MissingInProject(projectID int64, want []RobotAccess) []RobotAccess {
	if h.All {
		return []RobotAccess{}
	}

	return MissingAccess(want, h.Projects[projectID])
}

// ReadHeldPermissions reads what the credentials hold at system scope and in each of the projects. Harbor's permission
// list answers only for projects, where it names the project itself ".". At system scope it lists nothing, even for an
// administrator, so a system administrator is found through the current user, and a robot holds what its own record
// grants at system scope: Harbor checks a robot's new robot against that record too. Any other user holds nothing
// there. Without robot:list the robot's record can't be read and nothing is held at system scope, so the call needs no
// permission of its own.
//
//harbor:permission none
func (c *Client) ReadHeldPermissions(ctx context.Context, projectIDs []int64) (*HeldPermissions, error) {
	current, err := c.api.User.GetCurrentUserInfo(ctx, user.NewGetCurrentUserInfoParams())
	if err == nil && current.Payload != nil && current.Payload.SysadminFlag {
		return &HeldPermissions{All: true}, nil
	}

	// Harbor answers 412 for a robot, which has no user record.
	isRobot := false

	if err != nil {
		if statusOf(err) != http.StatusPreconditionFailed {
			return nil, c.fail(err)
		}

		isRobot = true
	}

	held := &HeldPermissions{Robot: isRobot, Projects: map[int64][]RobotAccess{}}

	if isRobot {
		held.System, err = c.ownSystemAccess(ctx)
		if err != nil {
			return nil, err
		}
	}

	for _, id := range projectIDs {
		if _, done := held.Projects[id]; done {
			continue
		}

		access, err := c.currentPermissions(ctx, ProjectScope(id))
		if err != nil {
			return nil, err
		}

		held.Projects[id] = access
	}

	return held, nil
}

// ownSystemAccess returns the system-scope access in the record of the system robot the client signs in as. A robot
// that may not list robots, or whose record is gone, holds nothing there.
func (c *Client) ownSystemAccess(ctx context.Context) ([]RobotAccess, error) {
	own, err := c.findRobot(ctx, "Level=system", func(full string) bool { return full == c.username })
	if IsNotFound(err) || IsForbidden(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var access []RobotAccess

	for _, grant := range own.Grants {
		if grant.Kind == LevelSystem {
			access = append(access, grant.Access...)
		}
	}

	return access, nil
}

// currentPermissions returns what the caller may do in scope, with each resource relative to the scope. Harbor names
// the project itself ".", which is resource "project" in a robot's permissions.
func (c *Client) currentPermissions(ctx context.Context, scope string) ([]RobotAccess, error) {
	relative := true

	response, err := c.api.User.GetCurrentUserPermissions(ctx, &user.GetCurrentUserPermissionsParams{Scope: &scope, Relative: &relative})
	if err != nil {
		return nil, c.fail(err)
	}

	result := make([]RobotAccess, 0, len(response.Payload))

	for _, permission := range response.Payload {
		if permission == nil {
			continue
		}

		resource := permission.Resource
		if resource == "." {
			resource = "project"
		}

		result = append(result, RobotAccess{Resource: resource, Action: permission.Action})
	}

	return result, nil
}

// ProjectScope is the permission scope of the project with the ID.
func ProjectScope(projectID int64) string { return "/project/" + strconv.FormatInt(projectID, 10) }

// RequiredPermission is one permission a kind's reconciler needs on the connection's credential. Scope is "system" or
// "project". The table, RequiredPermissions, is generated (make generate) from the //harbor:permission annotations on
// the client methods, so it cannot drift from the calls.
type RequiredPermission struct {
	Kind     string
	Scope    string
	Resource string
	Action   string
	// UserCredentialsOnly is true when robot credentials do not need it: they replace robots instead of updating them.
	UserCredentialsOnly bool
}
