package harbor

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

const (
	defaultUsernameKey = "username"
	defaultPasswordKey = "password"
)

// CredentialKeys says where a connection's credentials are in its Secret. Empty key names mean the defaults.
type CredentialKeys struct {
	// Username is a literal user name; it replaces the value at UsernameKey.
	Username    string
	UsernameKey string
	PasswordKey string
}

// MappedUsername returns the user name the connection signs in as: the literal one, else the value at the username
// key. It returns "" when the key is absent.
func MappedUsername(keys CredentialKeys, secret *corev1.Secret) string {
	if keys.Username != "" {
		return keys.Username
	}

	key := keys.UsernameKey
	if key == "" {
		key = defaultUsernameKey
	}

	return string(secret.Data[key])
}

// MappedCredentials returns the user name and password of a connection. The error names the missing key and never
// includes a value.
func MappedCredentials(keys CredentialKeys, secret *corev1.Secret) (string, string, error) {
	usernameKey := keys.UsernameKey
	if usernameKey == "" {
		usernameKey = defaultUsernameKey
	}

	passwordKey := keys.PasswordKey
	if passwordKey == "" {
		passwordKey = defaultPasswordKey
	}

	username := MappedUsername(keys, secret)
	if username == "" {
		return "", "", fmt.Errorf("credentials Secret %q has no key %q", secret.Name, usernameKey)
	}

	password := string(secret.Data[passwordKey])
	if password == "" {
		return "", "", fmt.Errorf("credentials Secret %q has no key %q", secret.Name, passwordKey)
	}

	return username, password, nil
}
