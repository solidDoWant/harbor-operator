//go:build integration

package ownership_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

const testFinalizer = "harbor.soliddowant.dev/test"

type environment struct {
	config *rest.Config
	scheme *runtime.Scheme
	client client.Client
}

func startEnvironment(t *testing.T) environment {
	t.Helper()

	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	config, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	runtimeScheme, err := scheme.New()
	require.NoError(t, err)
	kubeClient, err := client.New(config, client.Options{Scheme: runtimeScheme})
	require.NoError(t, err)

	return environment{config: config, scheme: runtimeScheme, client: kubeClient}
}

// startCache starts an informer cache like the manager's, with the ownership indexes registered when withIndexes is
// set.
func startCache(t *testing.T, env environment, withIndexes bool) cache.Cache {
	t.Helper()

	informers, err := cache.New(env.config, cache.Options{Scheme: env.scheme})
	require.NoError(t, err)

	if withIndexes {
		require.NoError(t, ownership.SetupIndexes(t.Context(), informers))
	}

	var running sync.WaitGroup

	running.Add(1)
	go func() {
		defer running.Done()

		assert.NoError(t, informers.Start(t.Context()))
	}()

	t.Cleanup(running.Wait)
	require.True(t, informers.WaitForCacheSync(t.Context()))

	return informers
}

func createNamespace(t *testing.T, kubeClient client.Client, name string) {
	t.Helper()
	require.NoError(t, kubeClient.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
}

func createProject(t *testing.T, kubeClient client.Client, namespace, name, key string, projectID int32) *v1alpha1.HarborProject {
	t.Helper()

	project := &v1alpha1.HarborProject{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Finalizers: []string{testFinalizer}},
		Spec:       v1alpha1.HarborProjectSpec{ConnectionRef: v1alpha1.ObjectReference{Name: "conn"}, Name: "team"},
	}
	require.NoError(t, kubeClient.Create(t.Context(), project))
	project.Status.HarborKey = key
	project.Status.ProjectID = projectID
	require.NoError(t, kubeClient.Status().Update(t.Context(), project))

	return project
}

func createRobot(t *testing.T, kubeClient client.Client, namespace, name, key string, robotID int64) *v1alpha1.HarborProjectRobot {
	t.Helper()

	robot := &v1alpha1.HarborProjectRobot{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.HarborProjectRobotSpec{
			ProjectRef:  v1alpha1.ObjectReference{Name: "team"},
			Name:        "ci",
			Permissions: []v1alpha1.RobotAccess{{Resource: "repository", Action: "push"}},
		},
	}
	require.NoError(t, kubeClient.Create(t.Context(), robot))
	robot.Status.HarborKey = key
	robot.Status.RobotID = robotID
	require.NoError(t, kubeClient.Status().Update(t.Context(), robot))

	return robot
}

// recordingAdmission admits every claimant and records which ones the lookup looked at.
type recordingAdmission struct {
	mutex sync.Mutex
	seen  []string
}

func (recorder *recordingAdmission) admit(_ context.Context, claimant client.Object) (bool, error) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	recorder.seen = append(recorder.seen, claimant.GetNamespace()+"/"+claimant.GetName())

	return true, nil
}

func (recorder *recordingAdmission) reset() []string {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	seen := recorder.seen
	recorder.seen = nil

	return seen
}

func projectOwnerName(t *testing.T, reader client.Reader, key string, recorder *recordingAdmission) string {
	t.Helper()

	owner, err := ownership.ProjectOwner(t.Context(), reader, key, func(ctx context.Context, project *v1alpha1.HarborProject) (bool, error) {
		return recorder.admit(ctx, project)
	})
	require.NoError(t, err)

	if owner == nil {
		return ""
	}

	return owner.Namespace + "/" + owner.Name
}

func TestProjectOwnerLooksUpClaimantsThroughTheIndex(t *testing.T) {
	env := startEnvironment(t)
	informers := startCache(t, env, true)

	createNamespace(t, env.client, "first")
	createNamespace(t, env.client, "second")
	createNamespace(t, env.client, "elsewhere")

	// The two claimants reach the same Harbor through URLs spelled differently.
	firstIdentity, err := ownership.NormalizeURL("https://Harbor.example.com:443/")
	require.NoError(t, err)
	secondIdentity, err := ownership.NormalizeURL("https://harbor.example.com")
	require.NoError(t, err)

	key := ownership.ProjectKey(secondIdentity, "team")

	createProject(t, env.client, "first", "team", ownership.ProjectKey(firstIdentity, "team"), 0)
	createProject(t, env.client, "second", "team", key, 7)
	createProject(t, env.client, "elsewhere", "team", ownership.ProjectKey(secondIdentity, "other"), 9)

	recorder := &recordingAdmission{}

	require.Eventually(t, func() bool {
		recorder.reset()
		return projectOwnerName(t, informers, key, recorder) == "second/team" && len(recorder.seen) == 2
	}, 30*time.Second, 100*time.Millisecond)

	assert.ElementsMatch(t, []string{"first/team", "second/team"}, recorder.reset(), "only the claimants of the key are read")
}

func TestOwnershipLookupsNeedTheIndex(t *testing.T) {
	env := startEnvironment(t)
	informers := startCache(t, env, false)

	_, err := ownership.ProjectOwner(t.Context(), informers, "https://harbor.example.com/projects/team",
		func(context.Context, *v1alpha1.HarborProject) (bool, error) { return true, nil })
	require.Error(t, err, "without the index the lookup must fail instead of listing every project")

	_, err = ownership.RobotOwner(t.Context(), informers, "https://harbor.example.com/projects/team/robots/ci",
		func(context.Context, *v1alpha1.HarborProjectRobot) (bool, error) { return true, nil })
	require.Error(t, err, "without the index the lookup must fail instead of listing every robot")
}

func TestDeletingProjectOwnsUntilItsFinalizerIsRemoved(t *testing.T) {
	env := startEnvironment(t)
	informers := startCache(t, env, true)

	createNamespace(t, env.client, "first")
	createNamespace(t, env.client, "second")

	key := ownership.ProjectKey("https://harbor.example.com", "team")

	owner := createProject(t, env.client, "first", "team", key, 7)
	createProject(t, env.client, "second", "team", key, 0)

	recorder := &recordingAdmission{}

	require.Eventually(t, func() bool {
		return projectOwnerName(t, informers, key, recorder) == "first/team"
	}, 30*time.Second, 100*time.Millisecond)

	require.NoError(t, env.client.Delete(t.Context(), owner))
	require.Eventually(t, func() bool {
		current := &v1alpha1.HarborProject{}
		require.NoError(t, informers.Get(t.Context(), client.ObjectKeyFromObject(owner), current))

		return !current.DeletionTimestamp.IsZero()
	}, 30*time.Second, 100*time.Millisecond)
	assert.Equal(t, "first/team", projectOwnerName(t, informers, key, recorder), "a deleting owner keeps its object")

	require.NoError(t, env.client.Get(t.Context(), client.ObjectKeyFromObject(owner), owner))
	owner.Finalizers = nil
	require.NoError(t, env.client.Update(t.Context(), owner))
	require.Eventually(t, func() bool {
		return projectOwnerName(t, informers, key, recorder) == "second/team"
	}, 30*time.Second, 100*time.Millisecond, "the next claimant owns once the finalizer is gone")
}

func TestRobotOwnerLooksUpClaimantsThroughTheIndex(t *testing.T) {
	env := startEnvironment(t)
	informers := startCache(t, env, true)

	createNamespace(t, env.client, "first")
	createNamespace(t, env.client, "second")

	key := ownership.RobotKey("https://harbor.example.com", "team", "ci")

	first := createRobot(t, env.client, "first", "ci", key, 0)
	second := createRobot(t, env.client, "second", "ci", key, 0)

	// Give the robot that would lose on age the ID, so only the recorded ID can make it the owner.
	younger := second
	if second.CreationTimestamp.Before(&first.CreationTimestamp) ||
		(second.CreationTimestamp.Equal(&first.CreationTimestamp) && second.UID < first.UID) {
		younger = first
	}

	younger.Status.RobotID = 3
	require.NoError(t, env.client.Status().Update(t.Context(), younger))
	createRobot(t, env.client, "first", "deploy", ownership.RobotKey("https://harbor.example.com", "team", "deploy"), 4)

	var seen []string

	require.Eventually(t, func() bool {
		seen = nil
		owner, err := ownership.RobotOwner(t.Context(), informers, key, func(_ context.Context, robot *v1alpha1.HarborProjectRobot) (bool, error) {
			seen = append(seen, robot.Namespace+"/"+robot.Name)
			return true, nil
		})
		require.NoError(t, err)

		return owner != nil && owner.UID == younger.UID && len(seen) == 2
	}, 30*time.Second, 100*time.Millisecond)

	assert.ElementsMatch(t, []string{"first/ci", "second/ci"}, seen)
}

func TestDeletingProjectWithoutIDDoesNotOwn(t *testing.T) {
	env := startEnvironment(t)
	informers := startCache(t, env, true)

	createNamespace(t, env.client, "first")

	key := ownership.ProjectKey("https://harbor.example.com", "team")
	claimant := createProject(t, env.client, "first", "team", key, 0)

	recorder := &recordingAdmission{}

	require.Eventually(t, func() bool {
		return projectOwnerName(t, informers, key, recorder) == "first/team"
	}, 30*time.Second, 100*time.Millisecond)

	require.NoError(t, env.client.Delete(t.Context(), claimant))
	require.Eventually(t, func() bool {
		return projectOwnerName(t, informers, key, recorder) == ""
	}, 30*time.Second, 100*time.Millisecond, "a claimant being deleted without an ID owns nothing")

	require.NoError(t, env.client.Get(t.Context(), client.ObjectKeyFromObject(claimant), claimant))
	claimant.Finalizers = nil
	require.NoError(t, env.client.Update(t.Context(), claimant))
}

func createSystemRobot(t *testing.T, kubeClient client.Client, namespace, name, key string, robotID int64) *v1alpha1.HarborSystemRobot {
	t.Helper()

	robot := &v1alpha1.HarborSystemRobot{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.HarborSystemRobotSpec{
			ConnectionRef:     v1alpha1.ObjectReference{Name: "conn"},
			Name:              "ci",
			SystemPermissions: []v1alpha1.SystemRobotAccess{{Resource: "project", Action: "list"}},
		},
	}
	require.NoError(t, kubeClient.Create(t.Context(), robot))
	robot.Status.HarborKey = key
	robot.Status.RobotID = robotID
	require.NoError(t, kubeClient.Status().Update(t.Context(), robot))

	return robot
}

func TestRobotOwnerPicksTheOwnerAmongSystemRobots(t *testing.T) {
	env := startEnvironment(t)
	informers := startCache(t, env, true)

	createNamespace(t, env.client, "first")
	createNamespace(t, env.client, "second")

	key := "https://harbor.example.com/robots/ci"

	first := createSystemRobot(t, env.client, "first", "ci", key, 0)
	second := createSystemRobot(t, env.client, "second", "ci", key, 0)

	// Give the robot that would lose on age the ID, so only the recorded ID can make it the owner.
	younger := second
	if second.CreationTimestamp.Before(&first.CreationTimestamp) ||
		(second.CreationTimestamp.Equal(&first.CreationTimestamp) && second.UID < first.UID) {
		younger = first
	}

	younger.Status.RobotID = 3
	require.NoError(t, env.client.Status().Update(t.Context(), younger))
	createSystemRobot(t, env.client, "first", "deploy", "https://harbor.example.com/robots/deploy", 4)
	// A project robot with the same key is another kind and never a claimant of a system robot.
	createRobot(t, env.client, "second", "ci", key, 5)

	var seen []string

	require.Eventually(t, func() bool {
		seen = nil
		owner, err := ownership.RobotOwner(t.Context(), informers, key, func(_ context.Context, robot *v1alpha1.HarborSystemRobot) (bool, error) {
			seen = append(seen, robot.Namespace+"/"+robot.Name)
			return true, nil
		})
		require.NoError(t, err)

		return owner != nil && owner.UID == younger.UID && len(seen) == 2
	}, 30*time.Second, 100*time.Millisecond)

	assert.ElementsMatch(t, []string{"first/ci", "second/ci"}, seen)
}
