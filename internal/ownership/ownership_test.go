package ownership_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/solidDoWant/harbor-operator/internal/ownership"
)

func TestNormalizeURL(t *testing.T) {
	tests := map[string]struct {
		raw         string
		want        string
		assertError require.ErrorAssertionFunc
	}{
		"plain https":                  {raw: "https://harbor.example.com", want: "https://harbor.example.com"},
		"upper-case host and scheme":   {raw: "HTTPS://Harbor.Example.COM", want: "https://harbor.example.com"},
		"trailing slash":               {raw: "https://harbor.example.com/", want: "https://harbor.example.com"},
		"https default port":           {raw: "https://harbor.example.com:443", want: "https://harbor.example.com"},
		"http default port":            {raw: "http://harbor.example.com:80/", want: "http://harbor.example.com"},
		"non-default port kept":        {raw: "https://harbor.example.com:8443", want: "https://harbor.example.com:8443"},
		"http port 443 is not default": {raw: "http://harbor.example.com:443", want: "http://harbor.example.com:443"},
		"IPv6 with default port":       {raw: "https://[FD00::1]:443/", want: "https://[fd00::1]"},
		"IPv6 with port":               {raw: "https://[fd00::1]:8443", want: "https://[fd00::1]:8443"},
		"user info, query and fragment dropped": {
			raw:  "https://user@harbor.example.com/?a=b#c",
			want: "https://harbor.example.com",
		},
		"not http":    {raw: "ftp://harbor.example.com", assertError: require.Error},
		"no host":     {raw: "https://", assertError: require.Error},
		"unparseable": {raw: "https://harbor example.com:port", assertError: require.Error},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if test.assertError == nil {
				test.assertError = require.NoError
			}

			normalized, err := ownership.NormalizeURL(test.raw)
			test.assertError(t, err)
			assert.Equal(t, test.want, normalized)
		})
	}
}

func TestKeysShareOwnershipSpaceAcrossURLSpellings(t *testing.T) {
	spellings := []string{
		"https://harbor.example.com",
		"https://HARBOR.example.com",
		"https://harbor.example.com/",
		"https://harbor.example.com:443",
		"HTTPS://Harbor.Example.com:443/",
	}

	projectKeys := map[string]struct{}{}
	robotKeys := map[string]struct{}{}

	for _, spelling := range spellings {
		identity, err := ownership.NormalizeURL(spelling)
		require.NoError(t, err)

		projectKeys[ownership.ProjectKey(identity, "team")] = struct{}{}
		robotKeys[ownership.RobotKey(identity, "team", "ci")] = struct{}{}
	}

	assert.Len(t, projectKeys, 1, "every spelling of the same Harbor gives one project key")
	assert.Len(t, robotKeys, 1, "every spelling of the same Harbor gives one robot key")

	other, err := ownership.NormalizeURL("https://harbor.example.com:8443")
	require.NoError(t, err)
	assert.NotContains(t, projectKeys, ownership.ProjectKey(other, "team"), "another port is another Harbor")
}

func TestKeysSeparateObjects(t *testing.T) {
	identity := "https://harbor.example.com"

	keys := []string{
		ownership.ProjectKey(identity, "team"),
		ownership.ProjectKey(identity, "other"),
		ownership.ProjectKey("https://other.example.com", "team"),
		ownership.RobotKey(identity, "team", "ci"),
		ownership.RobotKey(identity, "team", "deploy"),
		ownership.RobotKey(identity, "other", "ci"),
	}

	unique := map[string]struct{}{}
	for _, key := range keys {
		unique[key] = struct{}{}
	}

	assert.Len(t, unique, len(keys))
}

func TestOwner(t *testing.T) {
	older := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(older.Add(time.Second))

	tests := map[string]struct {
		claimants   []ownership.Claimant
		want        string
		assertError require.ErrorAssertionFunc
	}{
		"oldest wins": {
			claimants: []ownership.Claimant{
				{UID: "a", CreationTimestamp: newer, Admitted: true},
				{UID: "b", CreationTimestamp: older, Admitted: true},
			},
			want: "b",
		},
		"same creation time, lowest UID wins": {
			claimants: []ownership.Claimant{
				{UID: "b", CreationTimestamp: older, Admitted: true},
				{UID: "a", CreationTimestamp: older, Admitted: true},
			},
			want: "a",
		},
		"recorded ID beats age": {
			claimants: []ownership.Claimant{
				{UID: "a", CreationTimestamp: older, Admitted: true},
				{UID: "b", CreationTimestamp: newer, Admitted: true, HasID: true},
			},
			want: "b",
		},
		"several with an ID, oldest wins": {
			claimants: []ownership.Claimant{
				{UID: "a", CreationTimestamp: newer, Admitted: true, HasID: true},
				{UID: "b", CreationTimestamp: older, Admitted: true, HasID: true},
			},
			want: "b",
		},
		"not admitted does not count": {
			claimants: []ownership.Claimant{
				{UID: "a", CreationTimestamp: older, HasID: true},
				{UID: "b", CreationTimestamp: newer, Admitted: true},
			},
			want: "b",
		},
		"deleting with an ID still owns": {
			claimants: []ownership.Claimant{
				{UID: "a", CreationTimestamp: older, Admitted: true, HasID: true, Deleting: true},
				{UID: "b", CreationTimestamp: newer, Admitted: true},
			},
			want: "a",
		},
		"deleting without an ID does not count": {
			claimants: []ownership.Claimant{
				{UID: "a", CreationTimestamp: older, Admitted: true, Deleting: true},
				{UID: "b", CreationTimestamp: newer, Admitted: true},
			},
			want: "b",
		},
		"nobody counts": {
			claimants: []ownership.Claimant{
				{UID: "a", CreationTimestamp: older},
				{UID: "b", CreationTimestamp: newer, Admitted: true, Deleting: true},
			},
			assertError: require.Error,
		},
		"no claimants": {assertError: require.Error},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if test.assertError == nil {
				test.assertError = require.NoError
			}

			winner, err := ownership.Owner(test.claimants)
			test.assertError(t, err)
			assert.Equal(t, test.want, string(winner.UID))
		})
	}
}
