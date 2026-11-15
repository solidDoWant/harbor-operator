//go:build integration

package controller_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// TestConnectionDoesNotRetryRejectedCredentials uses a stub Harbor, never a real account, so it cannot lock anything.
func TestConnectionDoesNotRetryRejectedCredentials(t *testing.T) {
	t.Parallel()

	cl := startOperator(t)

	for name, rejection := range map[string]int{"unauthorized": http.StatusUnauthorized, "forbidden": http.StatusForbidden} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int64

			stub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)

				_, password, _ := request.BasicAuth()
				if password != "good" {
					writer.WriteHeader(rejection)

					return
				}

				writer.Header().Set("Content-Type", "application/json")

				if request.URL.Path == "/api/v2.0/systeminfo" {
					_, _ = writer.Write([]byte(`{"harbor_version":"v2.15.0","external_url":"https://harbor.example.com"}`))

					return
				}

				_, _ = writer.Write([]byte(`[]`))
			}))
			t.Cleanup(stub.Close)

			namespace := newNamespace(t, cl, "conn")
			secret := credentials(namespace, "harbor-creds", "someone", "bad")
			require.NoError(t, cl.Create(t.Context(), secret))

			connection := newConnection(namespace, "harbor", stub.URL, "harbor-creds")
			require.NoError(t, cl.Create(t.Context(), connection))

			key := client.ObjectKeyFromObject(connection)

			reason := harborv1alpha1.ReasonUnauthorized
			if rejection == http.StatusForbidden {
				reason = harborv1alpha1.ReasonForbidden
			}

			awaitReady(t, cl, key, metav1.ConditionFalse, reason)

			// The default backoff would retry many times in this window.
			time.Sleep(10 * time.Second)
			assert.LessOrEqual(t, requests.Load(), int64(2), "a rejected credential must not be retried")

			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
			secret.Data = nil
			secret.StringData = map[string]string{"username": "someone", "password": "good"}
			require.NoError(t, cl.Update(t.Context(), secret))
			awaitReady(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		})
	}
}
