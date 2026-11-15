// Package harbor wraps the go-client for the Harbor v2.0 API: a cache of
// authenticated clients, typed project and robot operations, and the mapping
// from Harbor failures to condition reasons.
package harbor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	httptransport "github.com/go-openapi/runtime/client"
	"github.com/goharbor/go-client/pkg/harbor"
	v2client "github.com/goharbor/go-client/pkg/sdk/v2.0/client"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/project"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/quota"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/robot"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/user"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
)

// UnlimitedQuota is Harbor's storage quota value for no limit.
const UnlimitedQuota int64 = -1

// ConnectionConfig is everything needed to reach Harbor.
type ConnectionConfig struct {
	// URL is the Harbor base URL, without the /api/v2.0 path.
	URL                string
	Username           string
	Password           string
	CABundle           string
	InsecureSkipVerify bool
}

// Client talks to one Harbor. Every error it returns is an *Error.
type Client struct {
	api      *v2client.HarborAPI
	password string
}

// NewClient builds a Client for the connection settings.
func NewClient(config ConnectionConfig) (*Client, error) {
	parsed, err := url.Parse(config.URL)
	if err != nil || parsed.Host == "" {
		return nil, &Error{Message: "invalid Harbor URL"}
	}

	parsed.Path = "/api/v2.0"

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: config.InsecureSkipVerify} //nolint:gosec // the connection spec asks for it

	if config.CABundle != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(config.CABundle)) {
			return nil, &Error{Message: "caBundle holds no PEM certificate"}
		}

		tlsConfig.RootCAs = pool
	}

	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
	}

	harborConfig := &harbor.Config{
		URL:       parsed,
		Transport: transport,
		AuthInfo:  httptransport.BasicAuth(config.Username, config.Password),
	}

	return &Client{api: v2client.New(harborConfig.ToV2Config()), password: config.Password}, nil
}

func (c *Client) fail(err error) error { return wrapError(err, c.password) }

// CheckCredentials checks that Harbor answers and the credentials are accepted. Harbor
// treats wrong credentials as anonymous, so it reads the caller's permissions,
// which refuses anonymous callers.
func (c *Client) CheckCredentials(ctx context.Context) error {
	_, err := c.api.User.GetCurrentUserPermissions(ctx, user.NewGetCurrentUserPermissionsParams())

	return c.fail(err)
}

// GetProjectByName returns the project, or a 404 error when there is none.
func (c *Client) GetProjectByName(ctx context.Context, name string) (*Project, error) {
	resource := true

	response, err := c.api.Project.GetProject(ctx, &project.GetProjectParams{ProjectNameOrID: name, XIsResourceName: &resource})
	if err != nil {
		failure := c.fail(err)
		if IsForbidden(failure) {
			return c.findProjectByName(ctx, name)
		}

		return nil, failure
	}

	return projectFromModel(response.Payload), nil
}

// findProjectByName looks the project up in the list of projects the caller can see. Harbor answers a robot's read of a
// project that does not exist with 403 instead of 404, so a 403 alone cannot tell "missing" from "not allowed".
func (c *Client) findProjectByName(ctx context.Context, name string) (*Project, error) {
	page, pageSize := int64(1), int64(100)

	for {
		response, err := c.api.Project.ListProjects(ctx, &project.ListProjectsParams{Name: &name, Page: &page, PageSize: &pageSize})
		if err != nil {
			return nil, c.fail(err)
		}

		for _, found := range response.Payload {
			if found.Name == name {
				return projectFromModel(found), nil
			}
		}

		if int64(len(response.Payload)) < pageSize {
			return nil, &Error{StatusCode: http.StatusNotFound, Message: "project " + name + " not found"}
		}

		page++
	}
}

// GetProject returns the project with the ID, or a 404 error when there is none.
func (c *Client) GetProject(ctx context.Context, id int64) (*Project, error) {
	resource := false

	response, err := c.api.Project.GetProject(ctx, &project.GetProjectParams{ProjectNameOrID: strconv.FormatInt(id, 10), XIsResourceName: &resource})
	if err != nil {
		return nil, c.fail(err)
	}

	return projectFromModel(response.Payload), nil
}

// CreateProject creates a project. A nil storageLimit leaves Harbor's default.
func (c *Client) CreateProject(ctx context.Context, name string, settings ProjectSettings, storageLimit *int64) (*Project, error) {
	return c.CreateProxyCacheProject(ctx, name, settings, storageLimit, 0)
}

// CreateProxyCacheProject creates a project that is a proxy cache of the registry with registryID; 0 creates a normal
// project. A nil storageLimit leaves Harbor's default.
func (c *Client) CreateProxyCacheProject(ctx context.Context, name string, settings ProjectSettings, storageLimit *int64, registryID int64) (*Project, error) {
	request := &models.ProjectReq{
		ProjectName:  name,
		Metadata:     settings.metadata(),
		StorageLimit: storageLimit,
	}
	if registryID != 0 {
		request.RegistryID = &registryID
	}

	if settings.Public != nil {
		request.Public = settings.Public
	}

	if _, err := c.api.Project.CreateProject(ctx, &project.CreateProjectParams{Project: request}); err != nil {
		return nil, c.fail(err)
	}

	// Harbor ignores the allowlist on create, so it is set by an update.
	if settings.CVEAllowlist != nil {
		if err := c.UpdateProjectMetadata(ctx, name, ProjectSettings{CVEAllowlist: settings.CVEAllowlist}); err != nil {
			return nil, err
		}
	}

	return c.GetProjectByName(ctx, name)
}

// UpdateProjectMetadata makes Harbor match the managed fields. Fields that are
// nil are left as they are.
func (c *Client) UpdateProjectMetadata(ctx context.Context, name string, settings ProjectSettings) error {
	resource := true
	request := &models.ProjectReq{Metadata: settings.metadata(), CVEAllowlist: settings.CVEAllowlist.toModel()}

	_, err := c.api.Project.UpdateProject(ctx, &project.UpdateProjectParams{ProjectNameOrID: name, XIsResourceName: &resource, Project: request})

	return c.fail(err)
}

// SetProjectRegistry points a proxy-cache project at another upstream registry.
func (c *Client) SetProjectRegistry(ctx context.Context, name string, registryID int64) error {
	resource := true
	request := &models.ProjectReq{RegistryID: &registryID}

	_, err := c.api.Project.UpdateProject(ctx, &project.UpdateProjectParams{ProjectNameOrID: name, XIsResourceName: &resource, Project: request})

	return c.fail(err)
}

// DeleteProject deletes the project; Harbor answers 412 while it holds content.
func (c *Client) DeleteProject(ctx context.Context, name string) error {
	resource := true

	_, err := c.api.Project.DeleteProject(ctx, &project.DeleteProjectParams{ProjectNameOrID: name, XIsResourceName: &resource})

	return c.fail(err)
}

// DeleteProjectByID deletes the project with the ID; Harbor answers 412 while it holds content.
func (c *Client) DeleteProjectByID(ctx context.Context, id int64) error {
	resource := false

	_, err := c.api.Project.DeleteProject(ctx, &project.DeleteProjectParams{ProjectNameOrID: strconv.FormatInt(id, 10), XIsResourceName: &resource})

	return c.fail(err)
}

func (c *Client) projectQuota(ctx context.Context, projectID int64) (*models.Quota, error) {
	reference, referenceID := "project", strconv.FormatInt(projectID, 10)

	response, err := c.api.Quota.ListQuotas(ctx, &quota.ListQuotasParams{Reference: &reference, ReferenceID: &referenceID})
	if err != nil {
		return nil, c.fail(err)
	}

	if len(response.Payload) == 0 {
		return nil, &Error{StatusCode: 404, Message: "no quota for project"}
	}

	return response.Payload[0], nil
}

// GetProjectStorageQuota returns the storage limit in bytes (-1 is unlimited).
func (c *Client) GetProjectStorageQuota(ctx context.Context, projectID int64) (int64, error) {
	found, err := c.projectQuota(ctx, projectID)
	if err != nil {
		return 0, err
	}

	return found.Hard["storage"], nil
}

// SetProjectStorageQuota sets the storage limit in bytes (-1 is unlimited).
func (c *Client) SetProjectStorageQuota(ctx context.Context, projectID, bytes int64) error {
	found, err := c.projectQuota(ctx, projectID)
	if err != nil {
		return err
	}

	_, err = c.api.Quota.UpdateQuota(ctx, &quota.UpdateQuotaParams{
		ID:   found.ID,
		Hard: &models.QuotaUpdateReq{Hard: models.ResourceList{"storage": bytes}},
	})

	return c.fail(err)
}

// CreateRobot creates a project robot. The returned Robot carries the secret,
// which Harbor shows only now.
func (c *Client) CreateRobot(ctx context.Context, projectName string, spec RobotSpec) (*Robot, error) {
	created, err := c.api.Robot.CreateRobot(ctx, &robot.CreateRobotParams{Robot: &models.RobotCreate{
		Name:        spec.Name,
		Description: spec.Description,
		Disable:     spec.Disabled,
		Duration:    spec.Duration,
		Level:       "project",
		Permissions: spec.permissions(projectName),
	}})
	if err != nil {
		return nil, c.fail(err)
	}

	result, err := c.GetRobot(ctx, created.Payload.ID)
	if err != nil {
		return nil, err
	}

	result.Secret = created.Payload.Secret

	return result, nil
}

// GetRobot returns the robot with the ID, or a 404 error.
func (c *Client) GetRobot(ctx context.Context, id int64) (*Robot, error) {
	response, err := c.api.Robot.GetRobotByID(ctx, &robot.GetRobotByIDParams{RobotID: id})
	if err != nil {
		return nil, c.fail(err)
	}

	return robotFromModel(response.Payload), nil
}

// FindProjectRobot returns the robot of a project by its short name, or a 404
// error when there is none.
func (c *Client) FindProjectRobot(ctx context.Context, projectID int64, name string) (*Robot, error) {
	query := fmt.Sprintf("Level=project,ProjectID=%d", projectID)
	pageSize := int64(100)

	for page := int64(1); ; page++ {
		response, err := c.api.Robot.ListRobot(ctx, &robot.ListRobotParams{Q: &query, Page: &page, PageSize: &pageSize})
		if err != nil {
			return nil, c.fail(err)
		}

		for _, candidate := range response.Payload {
			if candidate != nil && (candidate.Name == name || hasRobotSuffix(candidate.Name, name)) {
				return robotFromModel(candidate), nil
			}
		}

		if int64(len(response.Payload)) < pageSize {
			return nil, &Error{StatusCode: 404, Message: "robot not found"}
		}
	}
}

// hasRobotSuffix reports whether full is "<prefix>+<short>" for the short name.
func hasRobotSuffix(full, short string) bool {
	suffix := "+" + short

	return len(full) > len(suffix) && full[len(full)-len(suffix):] == suffix
}

// UpdateRobot makes the robot's description, lifetime, disabled flag and
// permissions match spec. The robot's name does not change.
func (c *Client) UpdateRobot(ctx context.Context, id int64, projectName string, spec RobotSpec) error {
	current, err := c.api.Robot.GetRobotByID(ctx, &robot.GetRobotByIDParams{RobotID: id})
	if err != nil {
		return c.fail(err)
	}

	duration := spec.Duration
	body := current.Payload
	body.Description = spec.Description
	body.Disable = spec.Disabled
	body.Duration = &duration
	body.Permissions = spec.permissions(projectName)
	body.Secret = ""

	_, err = c.api.Robot.UpdateRobot(ctx, &robot.UpdateRobotParams{RobotID: id, Robot: body})

	return c.fail(err)
}

// RefreshRobotSecret has Harbor generate a new secret, which stops the old one
// from working, and returns the new one.
func (c *Client) RefreshRobotSecret(ctx context.Context, id int64) (string, error) {
	response, err := c.api.Robot.RefreshSec(ctx, &robot.RefreshSecParams{RobotID: id, RobotSec: &models.RobotSec{}})
	if err != nil {
		return "", c.fail(err)
	}

	if response.Payload == nil || response.Payload.Secret == "" {
		return "", &Error{Message: "Harbor returned no secret"}
	}

	return response.Payload.Secret, nil
}

// DeleteRobot deletes the robot.
func (c *Client) DeleteRobot(ctx context.Context, id int64) error {
	_, err := c.api.Robot.DeleteRobot(ctx, &robot.DeleteRobotParams{RobotID: id})

	return c.fail(err)
}

// CurrentPermissions returns what the caller may do in scope, for example "/project/1", with each resource relative
// to the scope. Harbor evaluates every permission it knows for the scope against the caller.
func (c *Client) CurrentPermissions(ctx context.Context, scope string) ([]RobotAccess, error) {
	relative := true

	response, err := c.api.User.GetCurrentUserPermissions(ctx, &user.GetCurrentUserPermissionsParams{Scope: &scope, Relative: &relative})
	if err != nil {
		return nil, c.fail(err)
	}

	result := make([]RobotAccess, 0, len(response.Payload))

	for _, permission := range response.Payload {
		if permission != nil {
			result = append(result, RobotAccess{Resource: permission.Resource, Action: permission.Action})
		}
	}

	return result, nil
}

// MissingPermissions returns the permissions in want that the caller does not hold in scope, in want's order. A
// robot created with any of them would be refused, so a replace must not start.
func (c *Client) MissingPermissions(ctx context.Context, scope string, want []RobotAccess) ([]RobotAccess, error) {
	held, err := c.CurrentPermissions(ctx, scope)
	if err != nil {
		return nil, err
	}

	return MissingAccess(want, held), nil
}

// ProjectScope is the permission scope of the project with the ID.
func ProjectScope(projectID int64) string { return "/project/" + strconv.FormatInt(projectID, 10) }
