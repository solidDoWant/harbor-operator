package harbor

import (
	"encoding/json"
	"testing"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keepFiveVersionsAndRecentPulls() RetentionPolicy {
	return RetentionPolicy{
		Rules: []RetentionRule{
			{Template: "latestPushedK", Count: 5, RepositoryDecoration: "repoMatches", RepositoryPattern: "**", TagDecoration: "matches", TagPattern: "v*"},
			{Template: "nDaysSinceLastPull", Count: 30, RepositoryDecoration: "repoMatches", RepositoryPattern: "**", TagDecoration: "matches", TagPattern: "**", Untagged: true},
			{Template: "always", Disabled: true, RepositoryDecoration: "repoExcludes", RepositoryPattern: "tmp/**", TagDecoration: "excludes", TagPattern: "dev-*"},
		},
		Schedule: "0 0 0 * * *",
	}
}

func TestRetentionPolicyRequestMatchesHarborsFormat(t *testing.T) {
	model, err := keepFiveVersionsAndRecentPulls().toModel(42)
	require.NoError(t, err)

	encoded, err := json.Marshal(model)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"algorithm": "or",
		"scope": {"level": "project", "ref": 42},
		"trigger": {"kind": "Schedule", "settings": {"cron": "0 0 0 * * *"}, "references": {}},
		"rules": [
			{"action": "retain", "template": "latestPushedK", "params": {"latestPushedK": 5},
			 "scope_selectors": {"repository": [{"kind": "doublestar", "decoration": "repoMatches", "pattern": "**"}]},
			 "tag_selectors": [{"kind": "doublestar", "decoration": "matches", "pattern": "v*", "extras": "{\"untagged\":false}"}]},
			{"action": "retain", "template": "nDaysSinceLastPull", "params": {"nDaysSinceLastPull": 30},
			 "scope_selectors": {"repository": [{"kind": "doublestar", "decoration": "repoMatches", "pattern": "**"}]},
			 "tag_selectors": [{"kind": "doublestar", "decoration": "matches", "pattern": "**", "extras": "{\"untagged\":true}"}]},
			{"action": "retain", "template": "always", "disabled": true,
			 "scope_selectors": {"repository": [{"kind": "doublestar", "decoration": "repoExcludes", "pattern": "tmp/**"}]},
			 "tag_selectors": [{"kind": "doublestar", "decoration": "excludes", "pattern": "dev-*", "extras": "{\"untagged\":false}"}]}
		]
	}`, string(encoded))
}

func TestRetentionPolicyReadBackFromHarborEqualsTheRequest(t *testing.T) {
	want := keepFiveVersionsAndRecentPulls()

	model, err := want.toModel(42)
	require.NoError(t, err)

	// Harbor answers with JSON, so numbers come back as float64 and settings as generic maps.
	encoded, err := json.Marshal(model)
	require.NoError(t, err)

	var answered models.RetentionPolicy
	require.NoError(t, json.Unmarshal(encoded, &answered))

	have := retentionFromModel(&answered)
	assert.Equal(t, int64(42), have.ProjectID)
	assert.True(t, RetentionPolicyEqual(want, *have), "want %+v, have %+v", want, have)
}

func TestRetentionPolicyFromAnotherScopeHasNoProject(t *testing.T) {
	have := retentionFromModel(&models.RetentionPolicy{ID: 3, Scope: &models.RetentionPolicyScope{Level: "system", Ref: 42}})

	assert.Zero(t, have.ProjectID)
}

func TestRetentionPolicyEqual(t *testing.T) {
	changed := func(change func(*RetentionPolicy)) RetentionPolicy {
		policy := keepFiveVersionsAndRecentPulls()
		policy.Rules = append([]RetentionRule(nil), policy.Rules...)
		change(&policy)

		return policy
	}

	tests := []struct {
		name  string
		other RetentionPolicy
		equal bool
	}{
		{name: "same policy", other: keepFiveVersionsAndRecentPulls(), equal: true},
		{name: "another schedule", other: changed(func(policy *RetentionPolicy) { policy.Schedule = "" })},
		{name: "another count", other: changed(func(policy *RetentionPolicy) { policy.Rules[0].Count = 6 })},
		{name: "untagged differs", other: changed(func(policy *RetentionPolicy) { policy.Rules[0].Untagged = true })},
		{name: "disabled differs", other: changed(func(policy *RetentionPolicy) { policy.Rules[2].Disabled = false })},
		{name: "rule added", other: changed(func(policy *RetentionPolicy) { policy.Rules = append(policy.Rules, policy.Rules[0]) })},
		{name: "same rules in another order", other: changed(func(policy *RetentionPolicy) {
			policy.Rules[0], policy.Rules[1] = policy.Rules[1], policy.Rules[0]
		})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.equal, RetentionPolicyEqual(keepFiveVersionsAndRecentPulls(), test.other))
		})
	}
}
