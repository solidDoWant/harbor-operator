package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	"github.com/solidDoWant/harbor-operator/internal/access"
)

func TestCacheOptionsLimitTheCacheToTheWatchedNamespaces(t *testing.T) {
	tests := []struct {
		name            string
		watchNamespaces string
		want            map[string]cache.Config
	}{
		{name: "empty watches every namespace", watchNamespaces: "", want: nil},
		{name: "only separators watch every namespace", watchNamespaces: " , ", want: nil},
		{name: "one namespace", watchNamespaces: "team-a", want: map[string]cache.Config{"team-a": {}}},
		{
			name:            "several namespaces with spaces and duplicates",
			watchNamespaces: "team-a, team-b,team-a,",
			want:            map[string]cache.Config{"team-a": {}, "team-b": {}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, cacheOptions(access.ParseWatchedNamespaces(test.watchNamespaces)).DefaultNamespaces)
		})
	}
}
