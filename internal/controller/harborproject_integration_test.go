//go:build integration

package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

const projectWait = 90 * time.Second

// projectHarbor gives a test a Harbor client and a unique project name, and deletes the project when the test ends.
// Call it before startOperator so the project is deleted after the operator has stopped and cannot recreate it.
func projectHarbor(t *testing.T) (*harborfixture.Harbor, *harbor.Client, string) {
	t.Helper()

	instance := harborfixture.FromEnv(t)
	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	name := harborfixture.UniqueName(t, "op")
	t.Cleanup(func() { _ = admin.DeleteProject(context.Background(), name) })

	return instance, admin, name
}

// readyConnection creates a credential Secret and a connection in namespace and waits for it to be ready.
func readyConnection(t *testing.T, cl client.Client, instance *harborfixture.Harbor, namespace string, change func(*harborv1alpha1.HarborConnection)) *harborv1alpha1.HarborConnection {
	t.Helper()

	require.NoError(t, cl.Create(t.Context(), credentials(namespace, "harbor-creds", instance.Username, instance.Password)))

	connection := newConnection(namespace, "harbor", instance.URL, "harbor-creds")
	if change != nil {
		change(connection)
	}

	require.NoError(t, cl.Create(t.Context(), connection))
	awaitReady(t, cl, client.ObjectKeyFromObject(connection), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	return connection
}

func newProject(namespace, harborName string, connection *harborv1alpha1.HarborConnection) *harborv1alpha1.HarborProject {
	return &harborv1alpha1.HarborProject{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "project"},
		Spec: harborv1alpha1.HarborProjectSpec{
			ConnectionRef:  harborv1alpha1.ObjectReference{Namespace: connection.Namespace, Name: connection.Name},
			Name:           harborName,
			ResyncInterval: &metav1.Duration{Duration: 2 * time.Second},
		},
	}
}

func awaitProject(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborProject {
	t.Helper()

	project := &harborv1alpha1.HarborProject{}

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), key, project); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(project.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == status && condition.Reason == reason && condition.ObservedGeneration == project.Generation
	}, projectWait, 200*time.Millisecond, "waiting for Ready=%s %s, have %+v", status, reason, project.Status.Conditions)

	return project
}

func readyMessage(project *harborv1alpha1.HarborProject) string {
	return meta.FindStatusCondition(project.Status.Conditions, harborv1alpha1.ConditionReady).Message
}

func updateProject(t *testing.T, cl client.Client, key client.ObjectKey, change func(*harborv1alpha1.HarborProject)) {
	t.Helper()

	require.Eventually(t, func() bool {
		project := &harborv1alpha1.HarborProject{}
		if err := cl.Get(t.Context(), key, project); err != nil {
			return false
		}

		change(project)

		return cl.Update(t.Context(), project) == nil
	}, 10*time.Second, 100*time.Millisecond)
}

func awaitEvent(t *testing.T, cl client.Client, project *harborv1alpha1.HarborProject, reason string) {
	t.Helper()

	require.Eventually(t, func() bool {
		var list eventsv1.EventList
		if err := cl.List(t.Context(), &list, client.InNamespace(project.Namespace)); err != nil {
			return false
		}

		for _, event := range list.Items {
			if event.Regarding.UID == project.UID && event.Reason == reason {
				return true
			}
		}

		return false
	}, projectWait, 200*time.Millisecond, "waiting for a %s event", reason)
}

func fullSpec(project *harborv1alpha1.HarborProject, expires time.Time) {
	quota := resource.MustParse("5Gi")
	project.Spec.Public = ptr(true)
	project.Spec.StorageQuota = &quota
	project.Spec.Vulnerability = &harborv1alpha1.VulnerabilitySpec{
		PreventVulnerableImages: ptr(true),
		Severity:                "high",
		ScanOnPush:              ptr(true),
		CVEAllowlist:            &harborv1alpha1.CVEAllowlist{Items: []string{"CVE-2024-0001", "CVE-2024-0002"}, ExpiresAt: &metav1.Time{Time: expires}},
	}
}

// harborSettings reads the project's settings and storage quota from Harbor.
func harborSettings(t *testing.T, admin *harbor.Client, name string) (*harbor.Project, int64) {
	t.Helper()

	found, err := admin.GetProjectByName(t.Context(), name)
	require.NoError(t, err)

	quota, err := admin.GetProjectStorageQuota(t.Context(), found.ID)
	require.NoError(t, err)

	return found, quota
}

func TestProjectIsCreatedWithTheSpecSettings(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	connection := readyConnection(t, cl, instance, "default", nil)
	expires := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)

	project := newProject("default", name, connection)
	fullSpec(project, expires)
	require.NoError(t, cl.Create(t.Context(), project))

	ready := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	found, quota := harborSettings(t, admin, name)
	assert.Equal(t, int32(found.ID), ready.Status.ProjectID) //nolint:gosec // test IDs are small
	assert.Equal(t, int64(5<<30), quota)
	assert.Equal(t, harbor.ProjectSettings{
		Public:                  ptr(true),
		PreventVulnerableImages: ptr(true),
		Severity:                ptr("high"),
		ScanOnPush:              ptr(true),
		UseSystemAllowlist:      ptr(false),
		CVEAllowlist:            &harbor.CVEAllowlist{Items: []string{"CVE-2024-0001", "CVE-2024-0002"}, ExpiresAt: &expires},
	}, found.Settings)
	assert.Empty(t, ready.Annotations[harborv1alpha1.AnnotationCreatePending], "the create-pending annotation is cleared once the ID is recorded")
}

func TestProjectFollowsSpecChanges(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	connection := readyConnection(t, cl, instance, "default", nil)

	project := newProject("default", name, connection)
	fullSpec(project, time.Now().Add(72*time.Hour))
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)
	awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	updateProject(t, cl, key, func(project *harborv1alpha1.HarborProject) {
		project.Spec.Public = ptr(false)
		project.Spec.StorageQuota = nil
		project.Spec.UnlimitedStorage = true
		project.Spec.Vulnerability = &harborv1alpha1.VulnerabilitySpec{
			PreventVulnerableImages: ptr(false),
			Severity:                "low",
			ScanOnPush:              ptr(false),
			CVEAllowlist:            &harborv1alpha1.CVEAllowlist{Items: []string{"CVE-2024-0003"}},
		}
	})
	awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	found, quota := harborSettings(t, admin, name)
	assert.Equal(t, harbor.UnlimitedQuota, quota)
	assert.Equal(t, harbor.ProjectSettings{
		Public:                  ptr(false),
		PreventVulnerableImages: ptr(false),
		Severity:                ptr("low"),
		ScanOnPush:              ptr(false),
		UseSystemAllowlist:      ptr(false),
		CVEAllowlist:            &harbor.CVEAllowlist{Items: []string{"CVE-2024-0003"}},
	}, found.Settings)

	// Switching from the project's own allowlist to the system one, and back to a quota.
	updateProject(t, cl, key, func(project *harborv1alpha1.HarborProject) {
		project.Spec.UnlimitedStorage = false
		project.Spec.StorageQuota = ptr(resource.MustParse("1Gi"))
		project.Spec.Vulnerability.CVEAllowlist = nil
		project.Spec.Vulnerability.UseSystemAllowlist = ptr(true)
	})
	awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	found, quota = harborSettings(t, admin, name)
	assert.Equal(t, int64(1<<30), quota)
	assert.Equal(t, ptr(true), found.Settings.UseSystemAllowlist)
}

func TestProjectDriftInHarborIsReverted(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	connection := readyConnection(t, cl, instance, "default", nil)
	expires := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)

	project := newProject("default", name, connection)
	fullSpec(project, expires)
	// scanOnPush is left unset: its Harbor value must survive every resync.
	project.Spec.Vulnerability.ScanOnPush = nil
	require.NoError(t, cl.Create(t.Context(), project))
	ready := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	require.NoError(t, admin.UpdateProjectMetadata(t.Context(), name, harbor.ProjectSettings{
		Public:                  ptr(false),
		PreventVulnerableImages: ptr(false),
		Severity:                ptr("low"),
		ScanOnPush:              ptr(true),
		CVEAllowlist:            &harbor.CVEAllowlist{Items: []string{"CVE-2024-9999"}},
	}))
	require.NoError(t, admin.SetProjectStorageQuota(t.Context(), int64(ready.Status.ProjectID), 1<<30))

	want := harbor.ProjectSettings{
		Public:                  ptr(true),
		PreventVulnerableImages: ptr(true),
		Severity:                ptr("high"),
		ScanOnPush:              ptr(true),
		UseSystemAllowlist:      ptr(false),
		CVEAllowlist:            &harbor.CVEAllowlist{Items: []string{"CVE-2024-0001", "CVE-2024-0002"}, ExpiresAt: &expires},
	}

	require.Eventually(t, func() bool {
		found, quota := harborSettings(t, admin, name)
		return assert.ObjectsAreEqual(want, found.Settings) && quota == 5<<30
	}, projectWait, 500*time.Millisecond, "drift should be reverted on resync")

	// A few more resyncs leave the unset field alone.
	time.Sleep(5 * time.Second)

	found, _ := harborSettings(t, admin, name)
	assert.Equal(t, ptr(true), found.Settings.ScanOnPush, "a field the spec leaves unset keeps its Harbor value")
}

func TestProjectDeletedInHarborIsRecreated(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	connection := readyConnection(t, cl, instance, "default", nil)

	project := newProject("default", name, connection)
	project.Spec.Public = ptr(true)
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)
	first := awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	require.NoError(t, admin.DeleteProject(t.Context(), name))

	require.Eventually(t, func() bool {
		found, err := admin.GetProjectByName(t.Context(), name)
		if err != nil {
			return false
		}

		current := &harborv1alpha1.HarborProject{}

		return cl.Get(t.Context(), key, current) == nil && current.Status.ProjectID == int32(found.ID) && found.ID != int64(first.Status.ProjectID) //nolint:gosec // test IDs are small
	}, projectWait, 500*time.Millisecond, "the project should be created again and its new ID recorded")

	found, err := admin.GetProjectByName(t.Context(), name)
	require.NoError(t, err)
	assert.Equal(t, ptr(true), found.Settings.Public)
	awaitEvent(t, cl, first, harborv1alpha1.EventReasonRecreated)
}

func TestProjectNamingAnExistingHarborProject(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)

	_, err := admin.CreateProject(t.Context(), name, harbor.ProjectSettings{Public: ptr(false)}, nil)
	require.NoError(t, err)

	t.Run("without adopt reports AlreadyExists and changes nothing", func(t *testing.T) {
		connection := readyConnection(t, cl, instance, "default", func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true })

		project := newProject("default", name, connection)
		project.Spec.Public = ptr(true)
		require.NoError(t, cl.Create(t.Context(), project))
		reported := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists)
		assert.Zero(t, reported.Status.ProjectID)

		found, _ := harborSettings(t, admin, name)
		assert.Equal(t, ptr(false), found.Settings.Public)

		require.NoError(t, cl.Delete(t.Context(), project))
		require.Eventually(t, func() bool {
			return cl.Get(t.Context(), client.ObjectKeyFromObject(project), &harborv1alpha1.HarborProject{}) != nil
		}, projectWait, 200*time.Millisecond, "a resource without an ID is released at once")
	})

	t.Run("adopt without allowAdoption reports AdoptionNotAllowed", func(t *testing.T) {
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "no-adoption"}}))
		connection := readyConnection(t, cl, instance, "no-adoption", nil)

		project := newProject("no-adoption", name, connection)
		project.Spec.Adopt = true
		project.Spec.Public = ptr(true)
		require.NoError(t, cl.Create(t.Context(), project))
		awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonAdoptionNotAllowed)

		found, _ := harborSettings(t, admin, name)
		assert.Equal(t, ptr(false), found.Settings.Public)

		require.NoError(t, cl.Delete(t.Context(), project))
	})

	t.Run("adopt with allowAdoption takes the project over", func(t *testing.T) {
		require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "adoption"}}))
		connection := readyConnection(t, cl, instance, "adoption", func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true })

		project := newProject("adoption", name, connection)
		project.Spec.Adopt = true
		project.Spec.Public = ptr(true)
		require.NoError(t, cl.Create(t.Context(), project))
		adopted := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		found, _ := harborSettings(t, admin, name)
		assert.Equal(t, int32(found.ID), adopted.Status.ProjectID) //nolint:gosec // test IDs are small
		assert.Equal(t, ptr(true), found.Settings.Public, "the adopted project gets the spec's settings")
		awaitEvent(t, cl, adopted, harborv1alpha1.EventReasonAdopted)
	})
}

func TestSecondResourceForTheSameHarborProjectConflicts(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	connection := readyConnection(t, cl, instance, "default", nil)

	first := newProject("default", name, connection)
	first.Spec.Public = ptr(true)
	require.NoError(t, cl.Create(t.Context(), first))
	owner := awaitProject(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	// The second resource lives in another namespace and reaches the same Harbor through its own connection, even
	// with adoption allowed.
	require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "second"}}))
	secondConnection := readyConnection(t, cl, instance, "second", func(connection *harborv1alpha1.HarborConnection) {
		connection.Spec.URL += "/"
		connection.Spec.AllowAdoption = true
	})

	second := newProject("second", name, secondConnection)
	second.Spec.Public = ptr(false)
	second.Spec.Adopt = true
	require.NoError(t, cl.Create(t.Context(), second))
	conflicted := awaitProject(t, cl, client.ObjectKeyFromObject(second), metav1.ConditionFalse, harborv1alpha1.ReasonConflict)
	assert.Zero(t, conflicted.Status.ProjectID)
	assert.Contains(t, readyMessage(conflicted), "default/project")

	// The first keeps working: its spec changes still reach Harbor, and the second's settings never do.
	updateProject(t, cl, client.ObjectKeyFromObject(first), func(project *harborv1alpha1.HarborProject) {
		project.Spec.Vulnerability = &harborv1alpha1.VulnerabilitySpec{Severity: "critical"}
	})
	awaitProject(t, cl, client.ObjectKeyFromObject(first), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	found, _ := harborSettings(t, admin, name)
	assert.Equal(t, ptr("critical"), found.Settings.Severity)
	assert.Equal(t, ptr(true), found.Settings.Public)
	assert.Equal(t, int64(owner.Status.ProjectID), found.ID)

	// Deleting the second resource releases it without touching the project.
	require.NoError(t, cl.Delete(t.Context(), conflicted))
	require.Eventually(t, func() bool {
		return cl.Get(t.Context(), client.ObjectKeyFromObject(second), &harborv1alpha1.HarborProject{}) != nil
	}, projectWait, 200*time.Millisecond, "the second resource should be released")

	found, _ = harborSettings(t, admin, name)
	assert.Equal(t, int64(owner.Status.ProjectID), found.ID, "deleting the second resource never deletes the project")
}

func TestProjectInANamespaceTheConnectionDoesNotAdmit(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)

	connection := readyConnection(t, cl, instance, "default", func(connection *harborv1alpha1.HarborConnection) {
		connection.Spec.AllowedNamespaces.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"harbor.soliddowant.dev/ci": "true"}}
	})

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}}
	require.NoError(t, cl.Create(t.Context(), namespace))

	project := newProject("team", name, connection)
	// With a long resync only the Namespace watch can bring the project back once the label is added.
	project.Spec.ResyncInterval = &metav1.Duration{Duration: time.Hour}
	require.NoError(t, cl.Create(t.Context(), project))
	awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

	_, err := admin.GetProjectByName(t.Context(), name)
	assert.True(t, harbor.IsNotFound(err), "nothing is created in Harbor for a namespace that is not admitted")

	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(namespace), namespace))
	namespace.Labels = map[string]string{"harbor.soliddowant.dev/ci": "true"}
	require.NoError(t, cl.Update(t.Context(), namespace))

	ready := awaitProject(t, cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	found, err := admin.GetProjectByName(t.Context(), name)
	require.NoError(t, err)
	assert.Equal(t, int32(found.ID), ready.Status.ProjectID) //nolint:gosec // test IDs are small
}

func TestProjectSpecHarborRejectsReportsInvalidSpec(t *testing.T) {
	instance, admin, name := projectHarbor(t)
	cl := startOperator(t, true)
	connection := readyConnection(t, cl, instance, "default", nil)

	// Harbor refuses storage quotas above 1024 TB with a 400; CRD validation cannot know that limit.
	tooLarge := resource.MustParse("2Pi")

	project := newProject("default", name, connection)
	project.Spec.StorageQuota = &tooLarge
	require.NoError(t, cl.Create(t.Context(), project))
	key := client.ObjectKeyFromObject(project)

	rejected := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonInvalidSpec)
	assert.Contains(t, readyMessage(rejected), "1024TB", "the message is Harbor's")

	setQuota := func(quota resource.Quantity) {
		updateProject(t, cl, key, func(project *harborv1alpha1.HarborProject) { project.Spec.StorageQuota = &quota })
	}

	setQuota(resource.MustParse("1Gi"))
	awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	_, quota := harborSettings(t, admin, name)
	assert.Equal(t, int64(1<<30), quota)

	// The same on an update of an existing project.
	setQuota(tooLarge)

	rejected = awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonInvalidSpec)
	assert.Contains(t, readyMessage(rejected), "1024TB")

	setQuota(resource.MustParse("2Gi"))
	awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	_, quota = harborSettings(t, admin, name)
	assert.Equal(t, int64(2<<30), quota)
}
