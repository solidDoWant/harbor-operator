package harbor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/registry"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
)

// credentialTypeBasic is Harbor's credential type for an access key and secret.
const credentialTypeBasic = "basic"

// RegistryCredential is the access key and secret Harbor uses to reach an upstream registry.
type RegistryCredential struct {
	AccessKey    string
	AccessSecret string
}

// RegistrySpec is the part of a Harbor registry endpoint the operator manages. A nil Credential leaves Harbor's
// credential as it is.
type RegistrySpec struct {
	Name        string
	Type        string
	URL         string
	Description string
	Insecure    bool
	Credential  *RegistryCredential
}

// Registry is a registry endpoint as Harbor reports it. Harbor never returns the credential's secret.
type Registry struct {
	ID          int64
	Name        string
	Type        string
	URL         string
	Description string
	Insecure    bool
	// AccessKey is the stored credential's access key, empty without a credential.
	AccessKey string
	// CreationTime is when Harbor created the registry. Zero when Harbor did not report it.
	CreationTime time.Time
}

func registryFromModel(model *models.Registry) *Registry {
	registry := &Registry{
		ID: model.ID, Name: model.Name, Type: model.Type, URL: model.URL, Description: model.Description,
		Insecure: model.Insecure, CreationTime: time.Time(model.CreationTime).UTC(),
	}
	if model.Credential != nil {
		registry.AccessKey = model.Credential.AccessKey
	}

	return registry
}

// failRegistry wraps an error of a call that carried a registry credential, removing that secret too.
func (c *Client) failRegistry(err error, credential *RegistryCredential) error {
	failure := c.fail(err)

	var harborErr *Error
	if credential != nil && credential.AccessSecret != "" && errors.As(failure, &harborErr) {
		harborErr.Message = strings.ReplaceAll(harborErr.Message, credential.AccessSecret, "***")
	}

	return failure
}

// GetRegistry returns the registry with the ID, or a 404 error when there is none.
//
//harbor:permission system registry read
func (c *Client) GetRegistry(ctx context.Context, id int64) (*Registry, error) {
	response, err := c.api.Registry.GetRegistry(ctx, &registry.GetRegistryParams{ID: id})
	if err != nil {
		return nil, c.fail(err)
	}

	return registryFromModel(response.Payload), nil
}

// GetRegistryByName returns the registry with exactly the name, or a 404 error when there is none.
//
//harbor:permission system registry list
func (c *Client) GetRegistryByName(ctx context.Context, name string) (*Registry, error) {
	query := "name=" + name
	pageSize := int64(100)

	for page := int64(1); ; page++ {
		response, err := c.api.Registry.ListRegistries(ctx, &registry.ListRegistriesParams{Q: &query, Page: &page, PageSize: &pageSize})
		if err != nil {
			return nil, c.fail(err)
		}

		for _, candidate := range response.Payload {
			if candidate != nil && candidate.Name == name {
				return registryFromModel(candidate), nil
			}
		}

		if int64(len(response.Payload)) < pageSize {
			return nil, &Error{StatusCode: http.StatusNotFound, Message: "registry " + name + " not found"}
		}
	}
}

// CreateRegistry creates the registry endpoint and returns it as Harbor stores it.
//
//harbor:permission system registry create
func (c *Client) CreateRegistry(ctx context.Context, spec RegistrySpec) (*Registry, error) {
	body := &models.Registry{
		Name: spec.Name, Type: spec.Type, URL: spec.URL, Description: spec.Description, Insecure: spec.Insecure,
	}
	if spec.Credential != nil {
		body.Credential = &models.RegistryCredential{
			Type: credentialTypeBasic, AccessKey: spec.Credential.AccessKey, AccessSecret: spec.Credential.AccessSecret,
		}
	}

	if _, err := c.api.Registry.CreateRegistry(ctx, &registry.CreateRegistryParams{Registry: body}); err != nil {
		return nil, c.failRegistry(err, spec.Credential)
	}

	return c.GetRegistryByName(ctx, spec.Name)
}

// UpdateRegistry writes every managed field of the registry, and the credential when the spec has one.
//
//harbor:permission system registry update
func (c *Client) UpdateRegistry(ctx context.Context, id int64, spec RegistrySpec) error {
	body := &models.RegistryUpdate{
		Name: &spec.Name, URL: &spec.URL, Description: &spec.Description, Insecure: &spec.Insecure,
	}
	if spec.Credential != nil {
		credentialType := credentialTypeBasic
		body.CredentialType = &credentialType
		body.AccessKey = &spec.Credential.AccessKey
		body.AccessSecret = &spec.Credential.AccessSecret
	}

	_, err := c.api.Registry.UpdateRegistry(ctx, &registry.UpdateRegistryParams{ID: id, Registry: body})

	return c.failRegistry(err, spec.Credential)
}

// DeleteRegistry deletes the registry; Harbor answers 412 while a project or replication rule uses it.
//
//harbor:permission system registry delete
func (c *Client) DeleteRegistry(ctx context.Context, id int64) error {
	_, err := c.api.Registry.DeleteRegistry(ctx, &registry.DeleteRegistryParams{ID: id})

	return c.fail(err)
}

// PingRegistry asks Harbor to check the stored registry's upstream. It returns nil when the upstream answers, and an
// error otherwise; IsUpstreamFailure tells an unhealthy upstream from a failure of the call itself.
//
//harbor:permission system registry read
func (c *Client) PingRegistry(ctx context.Context, id int64) error {
	_, err := c.api.Registry.PingRegistry(ctx, &registry.PingRegistryParams{Registry: &models.RegistryPing{ID: &id}})

	return c.fail(err)
}

// IsUpstreamFailure reports whether a ping error says the upstream registry did not answer as expected. Harbor reports
// that with 400; any other status is about the call itself.
func IsUpstreamFailure(err error) bool {
	var harborErr *Error

	return errors.As(err, &harborErr) && harborErr.StatusCode == http.StatusBadRequest
}
