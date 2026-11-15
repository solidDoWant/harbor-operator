package harbor

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-openapi/runtime"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapErrorReturnsReasonAndRetry(t *testing.T) {
	tests := map[string]struct {
		err    error
		reason string
		retry  bool
	}{
		"400":           {project.NewDeleteProjectBadRequest(), ReasonInvalidSpec, false},
		"401":           {project.NewCreateProjectUnauthorized(), ReasonUnauthorized, true},
		"403":           {project.NewDeleteProjectForbidden(), ReasonForbidden, true},
		"404":           {project.NewDeleteProjectNotFound(), ReasonNotFound, true},
		"409":           {project.NewCreateProjectConflict(), ReasonConflict, true},
		"412":           {project.NewDeleteProjectPreconditionFailed(), ReasonDeleteBlocked, true},
		"500":           {project.NewDeleteProjectInternalServerError(), ReasonHarborUnavailable, true},
		"unknown 502":   {runtime.NewAPIError("x", nil, 502), ReasonHarborUnavailable, true},
		"unknown 400":   {runtime.NewAPIError("x", nil, 400), ReasonInvalidSpec, false},
		"network error": {&net.OpError{Op: "dial", Err: errors.New("refused")}, ReasonHarborUnavailable, true},
		"wrapped 404":   {wrapError(project.NewDeleteProjectNotFound(), "pw"), ReasonNotFound, true},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reason, retry := MapError(test.err)
			assert.Equal(t, test.reason, reason)
			assert.Equal(t, test.retry, retry)
		})
	}
}

func TestErrorsNeverContainPassword(t *testing.T) {
	const password = "s3cret-pass-word"

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"errors":[{"code":"BAD","message":"bad value ` + password + `"}]}`))
	}))
	defer server.Close()

	harborClient, err := NewClient(ConnectionConfig{URL: server.URL, Username: "admin", Password: password})
	require.NoError(t, err)

	_, err = harborClient.CreateProject(t.Context(), "demo", ProjectSettings{}, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), password)
	assert.Contains(t, err.Error(), "bad value")

	reason, _ := MapError(err)
	assert.Equal(t, ReasonInvalidSpec, reason)

	server.Close()

	_, err = harborClient.GetProjectByName(t.Context(), "demo")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), password)
	reason, retry := MapError(err)
	assert.Equal(t, ReasonHarborUnavailable, reason)
	assert.True(t, retry)
}
