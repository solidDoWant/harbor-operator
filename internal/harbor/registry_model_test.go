package harbor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordUpdates starts a Harbor stand-in that accepts registry updates and returns each request body.
func recordUpdates(t *testing.T) (*Client, *[]map[string]any) {
	t.Helper()

	var bodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(request.Body)
		assert.NoError(t, err)

		body := map[string]any{}
		assert.NoError(t, json.Unmarshal(raw, &body))

		bodies = append(bodies, body)

		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	harborClient, err := NewClient(ConnectionConfig{URL: server.URL, Username: "admin", Password: "pw"})
	require.NoError(t, err)

	return harborClient, &bodies
}

func TestUpdateRegistryWithoutCredentialLeavesHarborsCredential(t *testing.T) {
	harborClient, bodies := recordUpdates(t)

	require.NoError(t, harborClient.UpdateRegistry(t.Context(), 7, RegistrySpec{Name: "upstream", URL: "https://ghcr.io", Description: "d"}))
	require.Len(t, *bodies, 1)

	body := (*bodies)[0]
	assert.Equal(t, "https://ghcr.io", body["url"])
	assert.Equal(t, false, body["insecure"], "every managed field is written")

	for _, key := range []string{"access_key", "access_secret", "credential_type"} {
		assert.NotContains(t, body, key, "the credential is not touched when the spec has none")
	}
}

func TestUpdateRegistryWithCredentialSendsItEveryTime(t *testing.T) {
	harborClient, bodies := recordUpdates(t)
	spec := RegistrySpec{Name: "upstream", URL: "https://ghcr.io", Credential: &RegistryCredential{AccessKey: "user", AccessSecret: "token"}}

	for range 2 {
		require.NoError(t, harborClient.UpdateRegistry(t.Context(), 7, spec))
	}

	require.Len(t, *bodies, 2)

	for _, body := range *bodies {
		assert.Equal(t, "basic", body["credential_type"])
		assert.Equal(t, "user", body["access_key"])
		assert.Equal(t, "token", body["access_secret"])
	}
}

func TestRegistryErrorsNeverContainTheAccessSecret(t *testing.T) {
	const accessSecret = "upstream-token-value"

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"errors":[{"code":"BAD","message":"bad credential ` + accessSecret + `"}]}`))
	}))
	defer server.Close()

	harborClient, err := NewClient(ConnectionConfig{URL: server.URL, Username: "admin", Password: "pw"})
	require.NoError(t, err)

	spec := RegistrySpec{Name: "upstream", Type: "docker-registry", URL: "https://example.test", Credential: &RegistryCredential{AccessKey: "user", AccessSecret: accessSecret}}

	_, err = harborClient.CreateRegistry(t.Context(), spec)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), accessSecret)
	assert.Contains(t, err.Error(), "bad credential")

	err = harborClient.UpdateRegistry(t.Context(), 7, spec)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), accessSecret)
}
