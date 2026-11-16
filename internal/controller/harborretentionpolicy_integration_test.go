//go:build integration

package controller_test

import (
	"slices"
	"testing"
	"time"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/artifact"
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

// keepVersionsAndRecentPulls keeps the 5 most recently pushed v* tags and anything pulled in the last 30 days, daily.
func keepVersionsAndRecentPulls(namespace, name string, project *harborv1alpha1.HarborProject) *harborv1alpha1.HarborRetentionPolicy {
	allRepositories := harborv1alpha1.RepositorySelector{Decoration: "repoMatches", Pattern: "**"}

	return &harborv1alpha1.HarborRetentionPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: harborv1alpha1.HarborRetentionPolicySpec{
			ProjectRef: harborv1alpha1.ObjectReference{Namespace: project.Namespace, Name: project.Name},
			Rules: []harborv1alpha1.RetentionRule{
				{Template: "latestPushedK", Count: ptr(int32(5)), Repositories: allRepositories, Tags: harborv1alpha1.TagSelector{Decoration: "matches", Pattern: "v*"}},
				{Template: "nDaysSinceLastPull", Count: ptr(int32(30)), Repositories: allRepositories, Tags: harborv1alpha1.TagSelector{Decoration: "matches", Pattern: "**"}},
			},
			Schedule:       "0 0 0 * * *",
			ResyncInterval: &metav1.Duration{Duration: 2 * time.Second},
		},
	}
}

// keepVersionsAndRecentPullsInHarbor is the Harbor policy keepVersionsAndRecentPulls asks for.
func keepVersionsAndRecentPullsInHarbor() harbor.RetentionPolicy {
	return harbor.RetentionPolicy{
		Rules: []harbor.RetentionRule{
			{Template: "latestPushedK", Count: 5, RepositoryDecoration: "repoMatches", RepositoryPattern: "**", TagDecoration: "matches", TagPattern: "v*"},
			{Template: "nDaysSinceLastPull", Count: 30, RepositoryDecoration: "repoMatches", RepositoryPattern: "**", TagDecoration: "matches", TagPattern: "**"},
		},
		Schedule: "0 0 0 * * *",
	}
}

func awaitRetention(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborRetentionPolicy {
	t.Helper()

	policy := &harborv1alpha1.HarborRetentionPolicy{}

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), key, policy); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(policy.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == status && condition.Reason == reason && condition.ObservedGeneration == policy.Generation
	}, projectWait, 200*time.Millisecond, "waiting for Ready=%s %s, have %+v", status, reason, policy.Status.Conditions)

	return policy
}

// projectRetention reads the retention policy Harbor has for the project, or nil when it has none.
func projectRetention(t *testing.T, admin *harbor.Client, projectID int32) *harbor.RetentionPolicy {
	t.Helper()

	project, err := admin.GetProject(t.Context(), int64(projectID))
	require.NoError(t, err)

	if project.RetentionID == 0 {
		return nil
	}

	policy, err := admin.GetRetention(t.Context(), project.RetentionID)
	require.NoError(t, err)

	return policy
}

func TestRetentionPolicyCreatesExactlyTheSpecsPolicy(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	policy := keepVersionsAndRecentPulls(setup.project.Namespace, "retention", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), policy))

	ready := awaitRetention(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, setup.project.Status.ProjectID, ready.Status.ProjectID)
	assert.NotZero(t, ready.Status.RetentionID)

	have := projectRetention(t, setup.admin, setup.project.Status.ProjectID)
	require.NotNil(t, have, "Harbor has a retention policy for the project")
	assert.Equal(t, ready.Status.RetentionID, have.ID)
	assert.Equal(t, int64(setup.project.Status.ProjectID), have.ProjectID)
	assert.Equal(t, keepVersionsAndRecentPullsInHarbor().Rules, have.Rules)
	assert.Equal(t, "0 0 0 * * *", have.Schedule)
}

// repositoryTags lists the tags left in the repository.
func repositoryTags(t *testing.T, instance *harborfixture.Harbor, projectName, repository string) []string {
	t.Helper()

	withTag, pageSize := true, int64(100)

	response, err := instance.Client(t).Artifact.ListArtifacts(t.Context(), &artifact.ListArtifactsParams{
		ProjectName: projectName, RepositoryName: repository, WithTag: &withTag, PageSize: &pageSize,
	})
	require.NoError(t, err)

	var tags []string

	for _, found := range response.Payload {
		for _, tag := range found.Tags {
			tags = append(tags, tag.Name)
		}
	}

	slices.Sort(tags)

	return tags
}

func TestRetentionPolicyRunKeepsTheFiveNewestVersionTags(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	versions := []string{"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8"}
	for _, version := range versions {
		setup.instance.PushImage(t, setup.project.Spec.Name, "app", version)
	}

	require.Equal(t, versions, repositoryTags(t, setup.instance, setup.project.Spec.Name, "app"))

	policy := keepVersionsAndRecentPulls(setup.project.Namespace, "retention", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), policy))
	ready := awaitRetention(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	require.NoError(t, setup.admin.TriggerRetention(t.Context(), ready.Status.RetentionID))

	var statuses []string

	require.Eventually(t, func() bool {
		var err error

		statuses, err = setup.admin.RetentionExecutionStatuses(t.Context(), ready.Status.RetentionID)

		return err == nil && len(statuses) > 0 && statuses[0] != "Running" && statuses[0] != "Pending"
	}, 3*time.Minute, time.Second, "waiting for the retention run to finish, have %v", &statuses)
	require.Equal(t, "Success", statuses[0], "the retention run succeeds")

	assert.Equal(t, []string{"v4", "v5", "v6", "v7", "v8"}, repositoryTags(t, setup.instance, setup.project.Spec.Name, "app"))
}

func TestRetentionPolicyRulesChangedInHarborArePutBack(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	policy := keepVersionsAndRecentPulls(setup.project.Namespace, "retention", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), policy))
	ready := awaitRetention(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	changed := keepVersionsAndRecentPullsInHarbor()
	changed.Rules[0].Count = 1
	changed.Rules = append(changed.Rules, harbor.RetentionRule{
		Template: "always", RepositoryDecoration: "repoMatches", RepositoryPattern: "**", TagDecoration: "matches", TagPattern: "latest",
	})
	changed.Schedule = ""
	require.NoError(t, setup.admin.UpdateRetention(t.Context(), ready.Status.RetentionID, int64(setup.project.Status.ProjectID), changed))

	var have *harbor.RetentionPolicy

	require.Eventually(t, func() bool {
		have = projectRetention(t, setup.admin, setup.project.Status.ProjectID)

		return have != nil && harbor.RetentionPolicyEqual(keepVersionsAndRecentPullsInHarbor(), *have)
	}, projectWait, 500*time.Millisecond, "waiting for Harbor's retention policy to match the spec again, have %+v", have)
}

func TestSecondRetentionPolicyForTheSameProjectReportsConflict(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	first := keepVersionsAndRecentPulls(setup.project.Namespace, "retention", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), first))
	awaitRetention(t, setup.cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	second := keepVersionsAndRecentPulls(setup.project.Namespace, "retention-two", setup.project)
	second.Spec.Rules = second.Spec.Rules[:1]
	second.Spec.Rules[0].Count = ptr(int32(1))
	require.NoError(t, setup.cl.Create(t.Context(), second))

	conflict := awaitRetention(t, setup.cl, client.ObjectKeyFromObject(second), metav1.ConditionFalse, harborv1alpha1.ReasonConflict)
	assert.Zero(t, conflict.Status.RetentionID)

	have := projectRetention(t, setup.admin, setup.project.Status.ProjectID)
	require.NotNil(t, have)
	assert.True(t, harbor.RetentionPolicyEqual(keepVersionsAndRecentPullsInHarbor(), *have), "the second resource writes nothing, have %+v", have)

	// A non-owner never clears the owner's policy on deletion.
	require.NoError(t, setup.cl.Delete(t.Context(), second))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKeyFromObject(second), &harborv1alpha1.HarborRetentionPolicy{}))
	}, projectWait, 200*time.Millisecond)

	have = projectRetention(t, setup.admin, setup.project.Status.ProjectID)
	require.NotNil(t, have)
	assert.True(t, harbor.RetentionPolicyEqual(keepVersionsAndRecentPullsInHarbor(), *have), "deleting the second resource leaves the policy, have %+v", have)
}

func TestDeletingRetentionPolicyFollowsItsDeletionPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   harborv1alpha1.DeletionPolicy
		wantKept bool
	}{
		{name: "Delete removes the rules", policy: harborv1alpha1.DeletionPolicyDelete},
		{name: "DeleteWithContents removes the rules like Delete", policy: harborv1alpha1.DeletionPolicyDeleteWithContents},
		{name: "Orphan keeps the rules", policy: harborv1alpha1.DeletionPolicyOrphan, wantKept: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			setup := readyRobotProject(t, nil, nil)

			policy := keepVersionsAndRecentPulls(setup.project.Namespace, "retention", setup.project)
			policy.Spec.DeletionPolicy = test.policy
			require.NoError(t, setup.cl.Create(t.Context(), policy))
			awaitRetention(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			require.NoError(t, setup.cl.Delete(t.Context(), policy))
			require.Eventually(t, func() bool {
				return apierrors.IsNotFound(setup.cl.Get(t.Context(), client.ObjectKeyFromObject(policy), &harborv1alpha1.HarborRetentionPolicy{}))
			}, projectWait, 200*time.Millisecond)

			have := projectRetention(t, setup.admin, setup.project.Status.ProjectID)
			if test.wantKept {
				require.NotNil(t, have)
				assert.True(t, harbor.RetentionPolicyEqual(keepVersionsAndRecentPullsInHarbor(), *have), "have %+v", have)

				return
			}

			// Harbor accepts a policy with no rules, so the policy stays and is emptied.
			require.NotNil(t, have)
			assert.Empty(t, have.Rules, "no retention rules are left")
			assert.Empty(t, have.Schedule, "no schedule is left")
		})
	}
}

func TestRetentionPolicyInANamespaceTheProjectDoesNotAdmit(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	namespace := newNamespace(t, setup.cl, "policies")

	policy := keepVersionsAndRecentPulls(namespace, "retention", setup.project)
	require.NoError(t, setup.cl.Create(t.Context(), policy))

	denied := awaitRetention(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)
	assert.Zero(t, denied.Status.RetentionID)
	assert.Nil(t, projectRetention(t, setup.admin, setup.project.Status.ProjectID), "no retention policy is written for a namespace the project does not admit")
}

func TestRetentionPolicyWithoutItsProjectWaits(t *testing.T) {
	t.Parallel()
	setup := readyRobotProject(t, nil, nil)

	missing := setup.project.DeepCopy()
	missing.Name = "missing"

	policy := keepVersionsAndRecentPulls(setup.project.Namespace, "retention", missing)
	require.NoError(t, setup.cl.Create(t.Context(), policy))

	awaitRetention(t, setup.cl, client.ObjectKeyFromObject(policy), metav1.ConditionFalse, harborv1alpha1.ReasonProjectNotReady)
}
