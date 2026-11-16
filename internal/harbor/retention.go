package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strconv"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/retention"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
)

const (
	retentionScopeProject = "project"
	retentionAlgorithmOr  = "or"
	retentionActionRetain = "retain"
	retentionTriggerKind  = "Schedule"
	selectorKindDouble    = "doublestar"
	// RetentionTemplateAlways keeps every selected artifact and takes no parameter.
	RetentionTemplateAlways = "always"
)

// RetentionRule is one rule of a tag retention policy.
type RetentionRule struct {
	Disabled bool
	// Template is the Harbor rule template; its parameter is Count, except for always, which has none.
	Template             string
	Count                int64
	RepositoryDecoration string
	RepositoryPattern    string
	TagDecoration        string
	TagPattern           string
	Untagged             bool
}

// RetentionPolicy is a project's tag retention policy.
type RetentionPolicy struct {
	ID int64
	// ProjectID is the project the policy is scoped to, or 0 when Harbor scopes it to something other than a project.
	ProjectID int64
	Rules     []RetentionRule
	// Schedule is the Harbor cron expression. Empty means manual runs only.
	Schedule string
}

// RetentionPolicyEqual reports whether two policies have the same rules, in the same order, and the same schedule.
func RetentionPolicyEqual(left, right RetentionPolicy) bool {
	if left.Schedule != right.Schedule || len(left.Rules) != len(right.Rules) {
		return false
	}

	for index := range left.Rules {
		if left.Rules[index] != right.Rules[index] {
			return false
		}
	}

	return true
}

type untaggedExtras struct {
	Untagged bool `json:"untagged"`
}

func (p RetentionPolicy) toModel(projectID int64) (*models.RetentionPolicy, error) {
	rules := make([]*models.RetentionRule, 0, len(p.Rules))

	for _, rule := range p.Rules {
		extras, err := json.Marshal(untaggedExtras{Untagged: rule.Untagged})
		if err != nil {
			return nil, &Error{Message: "encoding tag selector extras: " + err.Error()}
		}

		params := map[string]any{}
		if rule.Template != RetentionTemplateAlways {
			params[rule.Template] = rule.Count
		}

		rules = append(rules, &models.RetentionRule{
			Action:   retentionActionRetain,
			Disabled: rule.Disabled,
			Template: rule.Template,
			Params:   params,
			ScopeSelectors: map[string][]models.RetentionSelector{
				"repository": {{Kind: selectorKindDouble, Decoration: rule.RepositoryDecoration, Pattern: rule.RepositoryPattern}},
			},
			TagSelectors: []*models.RetentionSelector{
				{Kind: selectorKindDouble, Decoration: rule.TagDecoration, Pattern: rule.TagPattern, Extras: string(extras)},
			},
		})
	}

	return &models.RetentionPolicy{
		ID:        p.ID,
		Algorithm: retentionAlgorithmOr,
		Rules:     rules,
		Scope:     &models.RetentionPolicyScope{Level: retentionScopeProject, Ref: projectID},
		Trigger: &models.RetentionRuleTrigger{
			Kind:       retentionTriggerKind,
			Settings:   map[string]any{"cron": p.Schedule},
			References: map[string]any{},
		},
	}, nil
}

// numberParam reads a numeric rule parameter, which Harbor returns as a JSON number or a string.
func numberParam(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		number, _ := typed.Int64()
		return number
	case string:
		number, _ := strconv.ParseInt(typed, 10, 64)
		return number
	}

	return 0
}

func retentionFromModel(model *models.RetentionPolicy) *RetentionPolicy {
	policy := &RetentionPolicy{ID: model.ID, Rules: []RetentionRule{}}

	if model.Scope != nil && model.Scope.Level == retentionScopeProject {
		policy.ProjectID = model.Scope.Ref
	}

	if model.Trigger != nil {
		if settings, ok := model.Trigger.Settings.(map[string]any); ok {
			if cron, ok := settings["cron"].(string); ok {
				policy.Schedule = cron
			}
		}
	}

	for _, ruleModel := range model.Rules {
		if ruleModel == nil {
			continue
		}

		rule := RetentionRule{Disabled: ruleModel.Disabled, Template: ruleModel.Template}
		if ruleModel.Template != RetentionTemplateAlways {
			rule.Count = numberParam(ruleModel.Params[ruleModel.Template])
		}

		if repositories := ruleModel.ScopeSelectors["repository"]; len(repositories) > 0 {
			rule.RepositoryDecoration = repositories[0].Decoration
			rule.RepositoryPattern = repositories[0].Pattern
		}

		if len(ruleModel.TagSelectors) > 0 && ruleModel.TagSelectors[0] != nil {
			tags := ruleModel.TagSelectors[0]
			rule.TagDecoration = tags.Decoration
			rule.TagPattern = tags.Pattern

			var extras untaggedExtras
			if tags.Extras != "" && json.Unmarshal([]byte(tags.Extras), &extras) == nil {
				rule.Untagged = extras.Untagged
			}
		}

		policy.Rules = append(policy.Rules, rule)
	}

	return policy
}

// GetRetention returns the retention policy with the ID, or a 404 error when there is none.
//
//harbor:permission project tag-retention read
func (c *Client) GetRetention(ctx context.Context, id int64) (*RetentionPolicy, error) {
	response, err := c.api.Retention.GetRetention(ctx, &retention.GetRetentionParams{ID: id})
	if err != nil {
		return nil, c.fail(err)
	}

	if response.Payload == nil {
		return nil, &Error{StatusCode: http.StatusNotFound, Message: fmt.Sprintf("retention policy %d not found", id)}
	}

	return retentionFromModel(response.Payload), nil
}

// CreateRetention creates the project's retention policy and returns its ID.
//
//harbor:permission project tag-retention create
func (c *Client) CreateRetention(ctx context.Context, projectID int64, policy RetentionPolicy) (int64, error) {
	model, err := policy.toModel(projectID)
	if err != nil {
		return 0, err
	}

	model.ID = 0

	response, err := c.api.Retention.CreateRetention(ctx, &retention.CreateRetentionParams{Policy: model})
	if err != nil {
		return 0, c.fail(err)
	}

	id, err := strconv.ParseInt(path.Base(response.Location), 10, 64)
	if err != nil {
		return 0, &Error{Message: "Harbor returned no retention policy ID"}
	}

	return id, nil
}

// UpdateRetention replaces the rules and schedule of the retention policy with the ID.
//
//harbor:permission project tag-retention update
func (c *Client) UpdateRetention(ctx context.Context, id, projectID int64, policy RetentionPolicy) error {
	policy.ID = id

	model, err := policy.toModel(projectID)
	if err != nil {
		return err
	}

	_, err = c.api.Retention.UpdateRetention(ctx, &retention.UpdateRetentionParams{ID: id, Policy: model})

	return c.fail(err)
}

// TriggerRetention starts a run of the retention policy with the ID.
//
//harbor:permission project tag-retention operate
func (c *Client) TriggerRetention(ctx context.Context, id int64) error {
	_, _, err := c.api.Retention.TriggerRetentionExecution(ctx, &retention.TriggerRetentionExecutionParams{ID: id})

	return c.fail(err)
}

// RetentionExecutionStatuses returns the status of each run of the retention policy with the ID, newest first.
//
//harbor:permission project tag-retention read
func (c *Client) RetentionExecutionStatuses(ctx context.Context, id int64) ([]string, error) {
	response, err := c.api.Retention.ListRetentionExecutions(ctx, &retention.ListRetentionExecutionsParams{ID: id})
	if err != nil {
		return nil, c.fail(err)
	}

	statuses := make([]string, 0, len(response.Payload))
	for _, execution := range response.Payload {
		if execution != nil {
			statuses = append(statuses, execution.Status)
		}
	}

	return statuses, nil
}
