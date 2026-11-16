package harborfixture

import (
	"context"
	"net/url"
	"strings"
	"testing"

	v2client "github.com/goharbor/go-client/pkg/sdk/v2.0/client"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/repository"
)

func repositoryParams(projectName string) *repository.ListRepositoriesParams {
	pageSize := int64(100)
	return &repository.ListRepositoriesParams{ProjectName: projectName, PageSize: &pageSize}
}

func deleteRepository(ctx context.Context, client *v2client.HarborAPI, projectName, repositoryName string) error {
	// Harbor wants a repository name with slashes encoded twice: once here, and once more by go-client.
	_, err := client.Repository.DeleteRepository(ctx, &repository.DeleteRepositoryParams{
		ProjectName:    projectName,
		RepositoryName: url.PathEscape(repositoryName),
	})

	return err
}

// DeleteRepositories deletes every repository of the project, leaving the project itself.
func (h *Harbor) DeleteRepositories(t testing.TB, projectName string) error {
	t.Helper()
	ctx := t.Context()
	client := h.Client(t)

	repositories, err := client.Repository.ListRepositories(ctx, repositoryParams(projectName))
	if err != nil {
		return err
	}

	for _, repository := range repositories.Payload {
		if err := deleteRepository(ctx, client, projectName, strings.TrimPrefix(repository.Name, projectName+"/")); err != nil {
			return err
		}
	}

	return nil
}
