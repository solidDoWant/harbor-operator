package harbor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClientCacheRebuildsWhenInputsChange(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "creds"},
		Data:       map[string][]byte{"username": []byte("admin"), "password": []byte("one")},
	}
	kube := fake.NewClientBuilder().WithObjects(secret).Build()
	cache := NewClientCache(kube)
	connection := Connection{UID: "uid-1", Generation: 1, Namespace: "ns", URL: "https://harbor.example.com", CredentialsSecret: "creds"}

	first, err := cache.Get(t.Context(), connection)
	require.NoError(t, err)

	again, err := cache.Get(t.Context(), connection)
	require.NoError(t, err)
	assert.Same(t, first, again, "unchanged inputs reuse the client")

	connection.Generation = 2
	afterGeneration, err := cache.Get(t.Context(), connection)
	require.NoError(t, err)
	assert.NotSame(t, first, afterGeneration, "a new generation builds a client")

	var current corev1.Secret
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(secret), &current))
	current.Data["password"] = []byte("two")
	require.NoError(t, kube.Update(t.Context(), &current))

	afterSecret, err := cache.Get(t.Context(), connection)
	require.NoError(t, err)
	assert.NotSame(t, afterGeneration, afterSecret, "a new Secret resourceVersion builds a client")
}

func TestClientCacheRejectsIncompleteCredentials(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "creds"}, Data: map[string][]byte{"username": []byte("admin")}}
	cache := NewClientCache(fake.NewClientBuilder().WithObjects(secret).Build())

	_, err := cache.Get(t.Context(), Connection{UID: "u", Namespace: "ns", URL: "https://h", CredentialsSecret: "creds"})
	require.Error(t, err)

	_, err = cache.Get(t.Context(), Connection{UID: "u", Namespace: "other", URL: "https://h", CredentialsSecret: "creds"})
	require.Error(t, err, "the Secret is read only from the connection's namespace")
}
