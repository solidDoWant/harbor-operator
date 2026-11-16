package harbor

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Connection names a HarborConnection and the settings the cache needs from it.
type Connection struct {
	UID        types.UID
	Generation int64
	// Namespace is the connection's namespace; the credential Secret is read
	// only from there.
	Namespace          string
	URL                string
	CredentialsSecret  string
	CredentialKeys     CredentialKeys
	CABundle           string
	InsecureSkipVerify bool
}

type cacheKey struct {
	keys                  CredentialKeys
	uid                   types.UID
	generation            int64
	secretResourceVersion string
}

type cacheEntry struct {
	key    cacheKey
	client *Client
}

// ClientCache keeps one Client per connection. A change to the connection's
// generation or to the credential Secret's resourceVersion builds a new one.
type ClientCache struct {
	reader client.Reader

	mutex   sync.Mutex
	entries map[types.UID]cacheEntry
}

// NewClientCache returns a cache that reads credential Secrets through reader.
func NewClientCache(reader client.Reader) *ClientCache {
	return &ClientCache{reader: reader, entries: map[types.UID]cacheEntry{}}
}

// Get returns the client for the connection, building it when needed.
func (c *ClientCache) Get(ctx context.Context, connection Connection) (*Client, error) {
	var secret corev1.Secret
	if err := c.reader.Get(ctx, client.ObjectKey{Namespace: connection.Namespace, Name: connection.CredentialsSecret}, &secret); err != nil {
		return nil, fmt.Errorf("reading credentials Secret %s/%s: %w", connection.Namespace, connection.CredentialsSecret, err)
	}

	key := cacheKey{keys: connection.CredentialKeys, uid: connection.UID, generation: connection.Generation, secretResourceVersion: secret.ResourceVersion}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if entry, ok := c.entries[connection.UID]; ok && entry.key == key {
		return entry.client, nil
	}

	username, password, err := MappedCredentials(connection.CredentialKeys, &secret)
	if err != nil {
		return nil, err
	}

	built, err := NewClient(ConnectionConfig{
		URL: connection.URL, Username: username, Password: password,
		CABundle: connection.CABundle, InsecureSkipVerify: connection.InsecureSkipVerify,
	})
	if err != nil {
		return nil, err
	}

	c.entries[connection.UID] = cacheEntry{key: key, client: built}

	return built, nil
}

// Forget drops the client of a deleted connection.
func (c *ClientCache) Forget(uid types.UID) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	delete(c.entries, uid)
}
