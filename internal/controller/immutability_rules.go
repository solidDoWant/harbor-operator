package controller

import (
	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// immutabilityRuleKey identifies a rule by its selectors: two rules with the same selectors are the same rule, and
// only their disabled flag can differ.
type immutabilityRuleKey struct {
	repositories harbor.Selector
	tags         harbor.Selector
}

func ruleKey(rule harbor.ImmutabilityRule) immutabilityRuleKey {
	return immutabilityRuleKey{repositories: rule.Repositories, tags: rule.Tags}
}

// desiredImmutabilityRules is the rule list the spec asks for.
func desiredImmutabilityRules(policy *harborv1alpha1.HarborImmutabilityPolicy) []harbor.ImmutabilityRule {
	rules := make([]harbor.ImmutabilityRule, 0, len(policy.Spec.Rules))
	for _, rule := range policy.Spec.Rules {
		rules = append(rules, harbor.ImmutabilityRule{
			Disabled:     rule.Disabled,
			Repositories: harbor.Selector{Decoration: rule.Repositories.Decoration, Pattern: rule.Repositories.Pattern},
			Tags:         harbor.Selector{Decoration: rule.Tags.Decoration, Pattern: rule.Tags.Pattern},
		})
	}

	return rules
}

// immutabilityPlan is what turns Harbor's rule list into the desired one. Applying it in field order (create, then
// update, then remove) never leaves a tag that both lists protect briefly unprotected.
type immutabilityPlan struct {
	create []harbor.ImmutabilityRule
	// update holds Harbor's rules with the desired disabled flag, keeping their IDs.
	update []harbor.ImmutabilityRule
	remove []harbor.ImmutabilityRule
}

// planImmutabilityRules matches the desired rules to Harbor's by their selectors. A Harbor rule that matches no
// desired rule, or repeats the selectors of an earlier Harbor rule, is removed.
func planImmutabilityRules(want, have []harbor.ImmutabilityRule) immutabilityPlan {
	existing := make(map[immutabilityRuleKey]harbor.ImmutabilityRule, len(have))

	var plan immutabilityPlan

	for _, rule := range have {
		if _, duplicate := existing[ruleKey(rule)]; duplicate {
			plan.remove = append(plan.remove, rule)
			continue
		}

		existing[ruleKey(rule)] = rule
	}

	wanted := make(map[immutabilityRuleKey]bool, len(want))

	for _, rule := range want {
		wanted[ruleKey(rule)] = true

		current, found := existing[ruleKey(rule)]

		switch {
		case !found:
			plan.create = append(plan.create, rule)
		case current.Disabled != rule.Disabled:
			current.Disabled = rule.Disabled
			plan.update = append(plan.update, current)
		}
	}

	for _, rule := range have {
		if kept, ok := existing[ruleKey(rule)]; ok && kept.ID == rule.ID && !wanted[ruleKey(rule)] {
			plan.remove = append(plan.remove, rule)
		}
	}

	return plan
}
