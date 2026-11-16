package harbor

import (
	"context"
	"net/url"
	"strings"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/repository"
)

// RepositoryPageSize is how many repositories one listing returns. Content deletion works through one page per
// reconcile, so the work each reconcile does stays bounded.
const RepositoryPageSize int64 = 100

// Repository is one repository of a project.
type Repository struct {
	// Name is the full name Harbor lists, with the project prefix, for example "proj/team/app".
	Name      string
	ProjectID int64
}

// RepositoryPage is the first page of a project's repositories and the total count Harbor reports.
type RepositoryPage struct {
	Repositories []Repository
	Total        int64
}

// ListRepositories returns the first page of the project's repositories. Deleting while paging forward would skip
// items, so callers always take the first page again after deleting.
//
//harbor:permission project repository list content-deletion
func (c *Client) ListRepositories(ctx context.Context, projectName string) (*RepositoryPage, error) {
	page, pageSize := int64(1), RepositoryPageSize

	response, err := c.api.Repository.ListRepositories(ctx, &repository.ListRepositoriesParams{
		ProjectName: projectName, Page: &page, PageSize: &pageSize,
	})
	if err != nil {
		return nil, c.fail(err)
	}

	result := &RepositoryPage{Total: response.XTotalCount}
	for _, found := range response.Payload {
		if found != nil {
			result.Repositories = append(result.Repositories, Repository{Name: found.Name, ProjectID: found.ProjectID})
		}
	}

	return result, nil
}

// DeleteRepository deletes the repository, with every artifact in it. repositoryName is the full name a listing
// returns; the project prefix is removed. Harbor wants the remaining name encoded twice: it is encoded once here and
// go-client encodes it again as a path parameter.
//
//harbor:permission project repository delete content-deletion
func (c *Client) DeleteRepository(ctx context.Context, projectName, repositoryName string) error {
	name := strings.TrimPrefix(repositoryName, projectName+"/")

	_, err := c.api.Repository.DeleteRepository(ctx, &repository.DeleteRepositoryParams{
		ProjectName: projectName, RepositoryName: url.PathEscape(name),
	})

	return c.fail(err)
}
