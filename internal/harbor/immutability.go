package harbor

import (
	"context"
	"strconv"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/immutable"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
)

// Fixed values of every immutability rule: Harbor knows only this action and template.
const (
	immutabilityAction   = "immutable"
	immutabilityTemplate = "immutable_template"
	selectorKind         = "doublestar"
	repositoryScope      = "repository"
)

// Selector is a doublestar pattern with its decoration, for example repoMatches or excludes.
type Selector struct {
	Decoration string
	Pattern    string
}

// ImmutabilityRule is one tag immutability rule of a project.
type ImmutabilityRule struct {
	// ID is Harbor's ID of the rule; zero for a rule not yet created.
	ID           int64
	Disabled     bool
	Repositories Selector
	Tags         Selector
}

func (r ImmutabilityRule) model() *models.ImmutableRule {
	return &models.ImmutableRule{
		ID:       r.ID,
		Action:   immutabilityAction,
		Template: immutabilityTemplate,
		Disabled: r.Disabled,
		Params:   map[string]any{},
		ScopeSelectors: map[string][]models.ImmutableSelector{
			repositoryScope: {{Kind: selectorKind, Decoration: r.Repositories.Decoration, Pattern: r.Repositories.Pattern}},
		},
		TagSelectors: []*models.ImmutableSelector{{Kind: selectorKind, Decoration: r.Tags.Decoration, Pattern: r.Tags.Pattern}},
	}
}

func immutabilityRuleFromModel(model *models.ImmutableRule) ImmutabilityRule {
	rule := ImmutabilityRule{ID: model.ID, Disabled: model.Disabled}

	if selectors := model.ScopeSelectors[repositoryScope]; len(selectors) > 0 {
		rule.Repositories = Selector{Decoration: selectors[0].Decoration, Pattern: selectors[0].Pattern}
	}

	if len(model.TagSelectors) > 0 && model.TagSelectors[0] != nil {
		rule.Tags = Selector{Decoration: model.TagSelectors[0].Decoration, Pattern: model.TagSelectors[0].Pattern}
	}

	return rule
}

func projectIDParam(projectID int64) (string, *bool) {
	resourceName := false

	return strconv.FormatInt(projectID, 10), &resourceName
}

// ListImmutabilityRules returns every immutability rule of the project.
func (c *Client) ListImmutabilityRules(ctx context.Context, projectID int64) ([]ImmutabilityRule, error) {
	project, resourceName := projectIDParam(projectID)
	pageSize := int64(100)

	var rules []ImmutabilityRule

	for page := int64(1); ; page++ {
		response, err := c.api.Immutable.ListImmuRules(ctx, &immutable.ListImmuRulesParams{
			ProjectNameOrID: project, XIsResourceName: resourceName, Page: &page, PageSize: &pageSize,
		})
		if err != nil {
			return nil, c.fail(err)
		}

		for _, model := range response.Payload {
			if model != nil {
				rules = append(rules, immutabilityRuleFromModel(model))
			}
		}

		if int64(len(response.Payload)) < pageSize {
			return rules, nil
		}
	}
}

// CreateImmutabilityRule adds the rule to the project.
func (c *Client) CreateImmutabilityRule(ctx context.Context, projectID int64, rule ImmutabilityRule) error {
	project, resourceName := projectIDParam(projectID)
	rule.ID = 0

	_, err := c.api.Immutable.CreateImmuRule(ctx, &immutable.CreateImmuRuleParams{
		ProjectNameOrID: project, XIsResourceName: resourceName, ImmutableRule: rule.model(),
	})

	return c.fail(err)
}

// UpdateImmutabilityRule replaces the project's rule with rule.ID by rule.
func (c *Client) UpdateImmutabilityRule(ctx context.Context, projectID int64, rule ImmutabilityRule) error {
	project, resourceName := projectIDParam(projectID)

	_, err := c.api.Immutable.UpdateImmuRule(ctx, &immutable.UpdateImmuRuleParams{
		ProjectNameOrID: project, XIsResourceName: resourceName, ImmutableRuleID: rule.ID, ImmutableRule: rule.model(),
	})

	return c.fail(err)
}

// DeleteImmutabilityRule deletes the project's rule with the ID.
func (c *Client) DeleteImmutabilityRule(ctx context.Context, projectID, ruleID int64) error {
	project, resourceName := projectIDParam(projectID)

	_, err := c.api.Immutable.DeleteImmuRule(ctx, &immutable.DeleteImmuRuleParams{
		ProjectNameOrID: project, XIsResourceName: resourceName, ImmutableRuleID: ruleID,
	})

	return c.fail(err)
}
