package harbor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteRepositoryEncodesTheNameTwiceWithoutTheProjectPrefix(t *testing.T) {
	tests := map[string]struct {
		repository string
		path       string
	}{
		"name with a slash": {repository: "proj/a/b", path: "/api/v2.0/projects/proj/repositories/a%252Fb"},
		"plain name":        {repository: "proj/app", path: "/api/v2.0/projects/proj/repositories/app"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var gotPath string

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				gotPath = request.URL.EscapedPath()

				writer.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			harborClient, err := NewClient(ConnectionConfig{URL: server.URL, Username: "admin", Password: "pass"})
			require.NoError(t, err)

			require.NoError(t, harborClient.DeleteRepository(t.Context(), "proj", test.repository))
			assert.Equal(t, test.path, gotPath)
		})
	}
}
