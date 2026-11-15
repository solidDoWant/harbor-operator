package harborfixture

import (
	"context"

	v2client "github.com/goharbor/go-client/pkg/sdk/v2.0/client"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/repository"
)

func repositoryParams(projectName string) *repository.ListRepositoriesParams {
	pageSize := int64(100)
	return &repository.ListRepositoriesParams{ProjectName: projectName, PageSize: &pageSize}
}

func deleteRepository(ctx context.Context, client *v2client.HarborAPI, projectName, repositoryName string) error {
	// Harbor wants a repository name with slashes double-encoded.
	_, err := client.Repository.DeleteRepository(ctx, &repository.DeleteRepositoryParams{
		ProjectName:    projectName,
		RepositoryName: repositoryName,
	})

	return err
}
