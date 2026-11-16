package harbor

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestIsRefusedOnlyForAnAnswerThatTheRequestWasNotCarriedOut(t *testing.T) {
	tests := map[string]struct {
		err     error
		refused bool
	}{
		"nil":             {nil, false},
		"400":             {project.NewCreateProjectBadRequest(), true},
		"403":             {project.NewDeleteProjectForbidden(), true},
		"409":             {project.NewCreateProjectConflict(), true},
		"unknown 499":     {runtime.NewAPIError("x", nil, 499), true},
		"wrapped 403":     {wrapError(project.NewDeleteProjectForbidden(), "pw"), true},
		"500":             {project.NewCreateProjectInternalServerError(), false},
		"unknown 502":     {runtime.NewAPIError("x", nil, 502), false},
		"unknown 399":     {runtime.NewAPIError("x", nil, 399), false},
		"network error":   {&net.OpError{Op: "dial", Err: errors.New("refused")}, false},
		"wrapped network": {wrapError(&net.OpError{Op: "dial", Err: errors.New("refused")}, "pw"), false},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.refused, IsRefused(test.err))
		})
	}
}

func TestWrapErrorGivesNetworkFailuresAStableMessage(t *testing.T) {
	failure := func(sourcePort int) error {
		return &url.Error{Op: "Get", URL: "http://harbor.example:8080/api/v2.0/systeminfo", Err: &net.OpError{
			Op:     "read",
			Net:    "tcp",
			Source: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: sourcePort},
			Addr:   &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 8080},
			Err:    errors.New("connection reset by peer"),
		}}
	}

	first := wrapError(failure(41588), "pw")
	second := wrapError(failure(52000), "pw")

	require.Error(t, first)
	assert.Equal(t, first.Error(), second.Error())
	assert.NotContains(t, first.Error(), "41588")
	assert.Contains(t, first.Error(), "connection reset by peer")
	assert.Contains(t, first.Error(), "http://harbor.example:8080/api/v2.0/systeminfo")
}
