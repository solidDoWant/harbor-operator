package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

func TestPlanImmutabilityRulesMatchesBySelectors(t *testing.T) {
	allRepositories := harbor.Selector{Decoration: "repoMatches", Pattern: "**"}
	versionTags := harbor.Selector{Decoration: "matches", Pattern: "v*"}
	releaseTags := harbor.Selector{Decoration: "matches", Pattern: "release-*"}
	otherTags := harbor.Selector{Decoration: "excludes", Pattern: "latest"}

	tests := []struct {
		name string
		want []harbor.ImmutabilityRule
		have []harbor.ImmutabilityRule
		plan immutabilityPlan
	}{
		{
			name: "matching lists need nothing",
			want: []harbor.ImmutabilityRule{{Repositories: allRepositories, Tags: versionTags}},
			have: []harbor.ImmutabilityRule{{ID: 7, Repositories: allRepositories, Tags: versionTags}},
		},
		{
			name: "a missing rule is created",
			want: []harbor.ImmutabilityRule{{Repositories: allRepositories, Tags: versionTags}},
			plan: immutabilityPlan{create: []harbor.ImmutabilityRule{{Repositories: allRepositories, Tags: versionTags}}},
		},
		{
			name: "a rule with other selectors is a different rule: create the wanted one and remove the other",
			want: []harbor.ImmutabilityRule{{Repositories: allRepositories, Tags: versionTags}},
			have: []harbor.ImmutabilityRule{{ID: 3, Repositories: allRepositories, Tags: releaseTags}},
			plan: immutabilityPlan{
				create: []harbor.ImmutabilityRule{{Repositories: allRepositories, Tags: versionTags}},
				remove: []harbor.ImmutabilityRule{{ID: 3, Repositories: allRepositories, Tags: releaseTags}},
			},
		},
		{
			name: "a changed disabled flag updates the rule in place",
			want: []harbor.ImmutabilityRule{{Disabled: true, Repositories: allRepositories, Tags: versionTags}},
			have: []harbor.ImmutabilityRule{{ID: 4, Repositories: allRepositories, Tags: versionTags}},
			plan: immutabilityPlan{update: []harbor.ImmutabilityRule{{ID: 4, Disabled: true, Repositories: allRepositories, Tags: versionTags}}},
		},
		{
			name: "a rule made in Harbor only is removed",
			have: []harbor.ImmutabilityRule{{ID: 5, Repositories: allRepositories, Tags: otherTags}},
			plan: immutabilityPlan{remove: []harbor.ImmutabilityRule{{ID: 5, Repositories: allRepositories, Tags: otherTags}}},
		},
		{
			name: "a second Harbor rule with the same selectors is removed",
			want: []harbor.ImmutabilityRule{{Repositories: allRepositories, Tags: versionTags}},
			have: []harbor.ImmutabilityRule{
				{ID: 1, Repositories: allRepositories, Tags: versionTags},
				{ID: 2, Disabled: true, Repositories: allRepositories, Tags: versionTags},
			},
			plan: immutabilityPlan{remove: []harbor.ImmutabilityRule{{ID: 2, Disabled: true, Repositories: allRepositories, Tags: versionTags}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.plan, planImmutabilityRules(test.want, test.have))
		})
	}
}
