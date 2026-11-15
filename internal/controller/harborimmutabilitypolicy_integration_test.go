//go:build integration

package controller_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

func immutabilityRule(tagPattern string, disabled bool) harborv1alpha1.ImmutabilityRule {
	return harborv1alpha1.ImmutabilityRule{
		Disabled:     disabled,
		Repositories: harborv1alpha1.RepositorySelector{Decoration: "repoMatches", Pattern: "**"},
		Tags:         harborv1alpha1.ImmutabilityTagSelector{Decoration: "matches", Pattern: tagPattern},
	}
}

func harborImmutabilityRule(tagPattern string, disabled bool) harbor.ImmutabilityRule {
	return harbor.ImmutabilityRule{
		Disabled:     disabled,
		Repositories: harbor.Selector{Decoration: "repoMatches", Pattern: "**"},
		Tags:         harbor.Selector{Decoration: "matches", Pattern: tagPattern},
	}
}

func immutabilityPolicy(namespace string, project *harborv1alpha1.HarborProject, rules ...harborv1alpha1.ImmutabilityRule) *harborv1alpha1.HarborImmutabilityPolicy {
	return &harborv1alpha1.HarborImmutabilityPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "immutable-tags"},
		Spec: harborv1alpha1.HarborImmutabilityPolicySpec{
			ProjectRef:     harborv1alpha1.ObjectReference{Namespace: project.Namespace, Name: project.Name},
			Rules:          rules,
			ResyncInterval: &metav1.Duration{Duration: 2 * time.Second},
		},
	}
}

func awaitImmutabilityPolicy(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborImmutabilityPolicy {
	t.Helper()

	policy := &harborv1alpha1.HarborImmutabilityPolicy{}

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), key, policy); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(policy.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == status && condition.Reason == reason && condition.ObservedGeneration == policy.Generation
	}, projectWait, 200*time.Millisecond, "waiting for Ready=%s %s, have %+v", status, reason, policy.Status.Conditions)

	return policy
}

// immutabilityRulesWithoutIDs lists the project's Harbor rules with their IDs cleared, for comparing with a spec.
func immutabilityRulesWithoutIDs(t *testing.T, admin *harbor.Client, projectID int32) []harbor.ImmutabilityRule {
	t.Helper()

	rules, err := admin.ListImmutabilityRules(t.Context(), int64(projectID))
	require.NoError(t, err)

	for index := range rules {
		rules[index].ID = 0
	}

	return rules
}

// pushTag writes a new random image to project/repository:tag as the Harbor admin and returns the error.
func pushTag(t *testing.T, instance *harborfixture.Harbor, project, repository, tag string) error {
	t.Helper()

	image, err := random.Image(64, 1)
	require.NoError(t, err)

	reference, err := name.ParseReference(fmt.Sprintf("%s/%s/%s:%s", instance.Registry(t), project, repository, tag), name.Insecure)
	require.NoError(t, err)

	return remote.Write(reference, image, remote.WithAuth(&authn.Basic{Username: instance.Username, Password: instance.Password}), remote.WithContext(t.Context()))
}

func TestImmutabilityRuleStopsOverwritingMatchingTags(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	policy := immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("v*", false))
	require.NoError(t, setup.cl.Create(t.Context(), policy))
	// Harbor refuses to delete an immutable tag, so the project's cleanup can remove its repositories only once the
	// policy and its rules are gone. Cleanups run in reverse order, so this runs while the operator still runs.
	t.Cleanup(func() {
		ctx := context.Background()
		_ = setup.cl.Delete(ctx, policy)

		assert.Eventually(t, func() bool {
			return apierrors.IsNotFound(setup.cl.Get(ctx, client.ObjectKeyFromObject(policy), &harborv1alpha1.HarborImmutabilityPolicy{}))
		}, projectWait, 200*time.Millisecond, "waiting for the policy to remove its rules")
	})

	ready := awaitImmutabilityPolicy(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, setup.project.Status.ProjectID, ready.Status.ProjectID)

	projectName := setup.project.Spec.Name
	require.NoError(t, pushTag(t, setup.instance, projectName, "app", "v1.0.0"), "the first push of v1.0.0 works")
	require.Error(t, pushTag(t, setup.instance, projectName, "app", "v1.0.0"), "pushing over v1.0.0 fails")

	require.NoError(t, pushTag(t, setup.instance, projectName, "app", "latest"))
	require.NoError(t, pushTag(t, setup.instance, projectName, "app", "latest"), "latest can still be overwritten")
}

func TestImmutabilityRulesChangedInHarborArePutBack(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)
	ctx := t.Context()

	policy := immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("v*", false), immutabilityRule("release-*", true))
	require.NoError(t, setup.cl.Create(ctx, policy))
	ready := awaitImmutabilityPolicy(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	want := []harbor.ImmutabilityRule{harborImmutabilityRule("v*", false), harborImmutabilityRule("release-*", true)}
	require.ElementsMatch(t, want, immutabilityRulesWithoutIDs(t, setup.admin, ready.Status.ProjectID))

	projectID := int64(ready.Status.ProjectID)
	rules, err := setup.admin.ListImmutabilityRules(ctx, projectID)
	require.NoError(t, err)

	// Change one rule, remove the other and add one, as a user could in Harbor's UI.
	for _, rule := range rules {
		switch rule.Tags.Pattern {
		case "v*":
			rule.Disabled = true
			require.NoError(t, setup.admin.UpdateImmutabilityRule(ctx, projectID, rule))
		case "release-*":
			require.NoError(t, setup.admin.DeleteImmutabilityRule(ctx, projectID, rule.ID))
		}
	}

	require.NoError(t, setup.admin.CreateImmutabilityRule(ctx, projectID, harborImmutabilityRule("added-in-ui", false)))

	require.Eventually(t, func() bool {
		rules, err := setup.admin.ListImmutabilityRules(ctx, projectID)
		if err != nil {
			return false
		}

		for index := range rules {
			rules[index].ID = 0
		}

		return assert.ElementsMatch(new(testing.T), want, rules)
	}, projectWait, 200*time.Millisecond, "waiting for Harbor's rules to be put back to the spec")
}

func TestImmutabilityPolicyDeletion(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		policy      harborv1alpha1.DeletionPolicy
		harborStays bool
	}{
		{name: "Delete removes the project's rules", policy: harborv1alpha1.DeletionPolicyDelete},
		{name: "Orphan leaves the project's rules", policy: harborv1alpha1.DeletionPolicyOrphan, harborStays: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			setup := readyRobotProject(t, nil, nil)

			policy := immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("v*", false))
			policy.Spec.DeletionPolicy = test.policy
			require.NoError(t, setup.cl.Create(t.Context(), policy))
			key := client.ObjectKeyFromObject(policy)
			ready := awaitImmutabilityPolicy(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			// A rule made in Harbor's UI after the policy took over the project's rules goes with them.
			require.NoError(t, setup.admin.CreateImmutabilityRule(t.Context(), int64(ready.Status.ProjectID), harborImmutabilityRule("added-in-ui", false)))

			require.NoError(t, setup.cl.Delete(t.Context(), ready))

			require.Eventually(t, func() bool {
				return apierrors.IsNotFound(setup.cl.Get(t.Context(), key, &harborv1alpha1.HarborImmutabilityPolicy{}))
			}, projectWait, 200*time.Millisecond, "waiting for the policy resource to go")

			rules := immutabilityRulesWithoutIDs(t, setup.admin, ready.Status.ProjectID)
			if test.harborStays {
				assert.NotEmpty(t, rules, "the Harbor rules stay")
			} else {
				assert.Empty(t, rules, "every immutability rule of the project is deleted")
			}
		})
	}
}

func TestImmutabilityPolicyInANamespaceTheProjectDoesNotAdmit(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	namespace := newNamespace(t, setup.cl, "policies")

	policy := immutabilityPolicy(namespace, setup.project, immutabilityRule("v*", false))
	require.NoError(t, setup.cl.Create(t.Context(), policy))

	notAllowed := awaitImmutabilityPolicy(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)
	assert.Zero(t, notAllowed.Status.ProjectID, "a policy that is not admitted claims nothing")
	assert.Empty(t, immutabilityRulesWithoutIDs(t, setup.admin, setup.project.Status.ProjectID), "no rule is written for a namespace the project does not admit")
}

func TestSecondImmutabilityPolicyForAProjectIsInConflict(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	first := immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("v*", false))
	require.NoError(t, setup.cl.Create(t.Context(), first))
	awaitImmutabilityPolicy(t, setup.cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	second := immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("release-*", false))
	second.Name = "other-rules"
	second.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
	require.NoError(t, setup.cl.Create(t.Context(), second))
	conflict := awaitImmutabilityPolicy(t, setup.cl, client.ObjectKeyFromObject(second), metav1.ConditionFalse, harborv1alpha1.ReasonConflict)
	assert.Zero(t, conflict.Status.ProjectID)

	want := []harbor.ImmutabilityRule{harborImmutabilityRule("v*", false)}
	assert.Equal(t, want, immutabilityRulesWithoutIDs(t, setup.admin, setup.project.Status.ProjectID), "the policy in conflict writes nothing")

	require.NoError(t, setup.cl.Delete(t.Context(), conflict))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKeyFromObject(second), &harborv1alpha1.HarborImmutabilityPolicy{}))
	}, projectWait, 200*time.Millisecond, "waiting for the policy in conflict to go")
	assert.Equal(t, want, immutabilityRulesWithoutIDs(t, setup.admin, setup.project.Status.ProjectID), "the policy in conflict deletes nothing")
}

func TestProjectDeletionWaitsForItsImmutabilityPolicy(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	policy := immutabilityPolicy(setup.project.Namespace, setup.project, immutabilityRule("v*", false))
	require.NoError(t, setup.cl.Create(t.Context(), policy))
	awaitImmutabilityPolicy(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	require.NoError(t, setup.cl.Delete(t.Context(), setup.project))
	waiting := awaitProject(t, setup.cl, client.ObjectKeyFromObject(setup.project), metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	assert.Contains(t, meta.FindStatusCondition(waiting.Status.Conditions, harborv1alpha1.ConditionReady).Message, "HarborImmutabilityPolicy "+setup.project.Namespace+"/immutable-tags")

	require.NoError(t, setup.cl.Delete(t.Context(), policy))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKeyFromObject(setup.project), &harborv1alpha1.HarborProject{}))
	}, projectWait, 200*time.Millisecond, "waiting for the project to go once its policy is gone")
}
