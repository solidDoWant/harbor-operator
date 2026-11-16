package harbor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMappedCredentials(t *testing.T) {
	t.Parallel()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds"},
		Data:       map[string][]byte{"username": []byte("u"), "password": []byte("p"), "USER": []byte("mapped"), "PASS": []byte("secret-value")},
	}

	for name, test := range map[string]struct {
		keys     CredentialKeys
		username string
		password string
		err      string
	}{
		"defaults":         {keys: CredentialKeys{}, username: "u", password: "p"},
		"mapped keys":      {keys: CredentialKeys{UsernameKey: "USER", PasswordKey: "PASS"}, username: "mapped", password: "secret-value"},
		"literal username": {keys: CredentialKeys{Username: "admin", PasswordKey: "PASS"}, username: "admin", password: "secret-value"},
		"missing password": {keys: CredentialKeys{PasswordKey: "NOPE"}, err: `has no key "NOPE"`},
		"missing username": {keys: CredentialKeys{UsernameKey: "NOUSER"}, err: `has no key "NOUSER"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			username, password, err := MappedCredentials(test.keys, secret)
			if test.err != "" {
				require.ErrorContains(t, err, test.err)
				assert.NotContains(t, err.Error(), "secret-value")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.username, username)
			assert.Equal(t, test.password, password)
			assert.Equal(t, test.username, MappedUsername(test.keys, secret))
		})
	}
}
