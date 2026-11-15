// Package harborfixture gives integration tests a real Harbor: an admin
// go-client, unique names, projects, pushed images and cleanup.
//
// The Harbor comes from test/harbor/up.sh, which `make test-integration` runs
// and which exports HARBOR_URL, HARBOR_ADMIN_USER and HARBOR_ADMIN_PASSWORD.
// Tests skip when HARBOR_URL is not set.
package harborfixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/goharbor/go-client/pkg/harbor"
	v2client "github.com/goharbor/go-client/pkg/sdk/v2.0/client"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/project"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/robot"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"
)

// Harbor is a reachable Harbor and the admin credentials for it.
type Harbor struct {
	URL      string
	Username string
	Password string
}

// FromEnv returns the Harbor named by the environment, or skips the test with
// a message when there is none.
func FromEnv(t testing.TB) *Harbor {
	t.Helper()

	address := os.Getenv("HARBOR_URL")
	if address == "" {
		t.Skip("HARBOR_URL is not set: run `make test-integration` (needs Docker) or test/harbor/up.sh")
	}

	username := os.Getenv("HARBOR_ADMIN_USER")
	if username == "" {
		username = "admin"
	}

	return &Harbor{URL: address, Username: username, Password: os.Getenv("HARBOR_ADMIN_PASSWORD")}
}

// Registry returns the host and port to push images to.
func (h *Harbor) Registry(t testing.TB) string {
	t.Helper()

	parsed, err := url.Parse(h.URL)
	require.NoError(t, err)

	return parsed.Host
}

// Client returns a go-client signed in with the admin credentials.
func (h *Harbor) Client(t testing.TB) *v2client.HarborAPI {
	t.Helper()
	return h.ClientFor(t, h.Username, h.Password)
}

// ClientFor returns a go-client signed in with the given basic-auth credential.
func (h *Harbor) ClientFor(t testing.TB, username, password string) *v2client.HarborAPI {
	t.Helper()

	clientSet, err := harbor.NewClientSet(&harbor.ClientSetConfig{URL: h.URL + "/api/v2.0", Username: username, Password: password})
	require.NoError(t, err)

	return clientSet.V2()
}

// UniqueName returns a Harbor-safe name that is the same for one test and
// different between tests: the prefix, then a hash of the test name.
func UniqueName(t testing.TB, prefix string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))

	return strings.ToLower(prefix) + "-" + hex.EncodeToString(sum[:])[:10]
}

// CreateProject creates a project named after the test and deletes it, with
// its repositories, when the test ends. It returns the project name.
func (h *Harbor) CreateProject(t testing.TB) string {
	t.Helper()
	projectName := UniqueName(t, "p")
	client := h.Client(t)
	_, err := client.Project.CreateProject(t.Context(), &project.CreateProjectParams{
		Project: &models.ProjectReq{ProjectName: projectName},
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.DeleteProject(context.Background(), t, projectName) })

	return projectName
}

// DeleteProject removes the project's repositories, then the project.
func (h *Harbor) DeleteProject(ctx context.Context, t testing.TB, projectName string) {
	t.Helper()
	client := h.Client(t)

	repositories, err := client.Repository.ListRepositories(ctx, repositoryParams(projectName))
	if err == nil {
		for _, repository := range repositories.Payload {
			_ = deleteRepository(ctx, client, projectName, strings.TrimPrefix(repository.Name, projectName+"/"))
		}
	}

	_, err = client.Project.DeleteProject(ctx, &project.DeleteProjectParams{ProjectNameOrID: projectName})
	if err != nil {
		t.Logf("deleting project %s: %v", projectName, err)
	}
}

// PushImage pushes a tiny random image to project/repository:tag using the
// admin credentials and returns its reference with the digest.
func (h *Harbor) PushImage(t testing.TB, projectName, repository, tag string) string {
	t.Helper()
	return h.PushImageAs(t, h.Username, h.Password, projectName, repository, tag)
}

// PushImageAs is PushImage with the given basic-auth credential.
func (h *Harbor) PushImageAs(t testing.TB, username, password, projectName, repository, tag string) string {
	t.Helper()

	image, err := random.Image(64, 1)
	require.NoError(t, err)
	image = mutate.Annotations(image, map[string]string{"test": t.Name()}).(v1.Image)
	reference, err := name.ParseReference(fmt.Sprintf("%s/%s/%s:%s", h.Registry(t), projectName, repository, tag), name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(reference, image,
		remote.WithAuth(&authn.Basic{Username: username, Password: password}),
		remote.WithContext(t.Context())))
	digest, err := image.Digest()
	require.NoError(t, err)

	return reference.Context().Name() + "@" + digest.String()
}

// CreateSystemRobot creates a system robot with the given permissions and deletes it when the test ends. It returns the robot's
// sign-in name and secret.
func (h *Harbor) CreateSystemRobot(t testing.TB, permissions []*models.RobotPermission) (username, secret string) {
	t.Helper()
	robotName := UniqueName(t, "sys")
	client := h.Client(t)
	created, err := client.Robot.CreateRobot(t.Context(), &robot.CreateRobotParams{Robot: &models.RobotCreate{
		Name:        robotName,
		Level:       "system",
		Duration:    -1,
		Permissions: permissions,
	}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.Robot.DeleteRobot(context.Background(), &robot.DeleteRobotParams{RobotID: created.Payload.ID})
	})

	return created.Payload.Name, created.Payload.Secret
}
