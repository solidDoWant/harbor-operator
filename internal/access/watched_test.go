package access_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/solidDoWant/harbor-operator/internal/access"
)

func TestWatchedNamespacesAdmitOnlyListedNamespaces(t *testing.T) {
	tests := []struct {
		name      string
		list      string
		namespace string
		want      bool
	}{
		{name: "an empty list watches every namespace", list: "", namespace: "team-a", want: true},
		{name: "a list of separators watches every namespace", list: " , ", namespace: "team-a", want: true},
		{name: "a listed namespace is watched", list: "team-a, team-b", namespace: "team-b", want: true},
		{name: "an unlisted namespace is not watched", list: "team-a,team-b", namespace: "team-c", want: false},
		{name: "a prefix of a listed namespace is not watched", list: "team-a", namespace: "team", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, access.ParseWatchedNamespaces(test.list).Watches(test.namespace))
		})
	}
}

func TestParseWatchedNamespacesDropsBlanksAndDuplicates(t *testing.T) {
	assert.Equal(t, access.WatchedNamespaces{"team-a", "team-b"}, access.ParseWatchedNamespaces(" team-a,,team-b, team-a ,"))
	assert.Empty(t, access.ParseWatchedNamespaces(""))
}

func TestNotWatchedMessageNamesTheNamespace(t *testing.T) {
	assert.Equal(t, "namespace team-b is not watched by the operator", access.NotWatchedMessage("team-b"))
}
