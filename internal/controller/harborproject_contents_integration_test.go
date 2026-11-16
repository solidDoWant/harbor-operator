//go:build integration

package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

// repositoryNames lists the repositories of the Harbor project as an admin sees them.
func repositoryNames(t *testing.T, instance *harborfixture.Harbor, projectName string) []string {
	t.Helper()

	pageSize := int64(100)
	response, err := instance.Client(t).Repository.ListRepositories(t.Context(),
		&repository.ListRepositoriesParams{ProjectName: projectName, PageSize: &pageSize})
	require.NoError(t, err)

	names := make([]string, 0, len(response.Payload))
	for _, found := range response.Payload {
		names = append(names, found.Name)
	}

	return names
}

// repositoryDeleteFailingProxy forwards to Harbor. While failing is set, it lets the first repository delete through and
// answers 503 to every later one, so a content deletion stops partway.
type repositoryDeleteFailingProxy struct {
	server  *httptest.Server
	failing atomic.Bool
	deletes atomic.Int32
}

func newRepositoryDeleteFailingProxy(t *testing.T, target string) *repositoryDeleteFailingProxy {
	t.Helper()

	parsed, err := url.Parse(target)
	require.NoError(t, err)

	proxy := &repositoryDeleteFailingProxy{}
	forward := httputil.NewSingleHostReverseProxy(parsed)
	proxy.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete && strings.Contains(request.URL.Path, "/repositories/") &&
			proxy.failing.Load() && proxy.deletes.Add(1) > 1 {
			http.Error(writer, "Harbor is down", http.StatusServiceUnavailable)

			return
		}

		request.Host = parsed.Host
		forward.ServeHTTP(writer, request)
	}))
	t.Cleanup(proxy.server.Close)

	return proxy
}

func TestProjectDeletionWithContents(t *testing.T) {
	t.Parallel()

	instance := harborfixture.FromEnv(t)
	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	cl := startOperator(t, true)

	allowContentDeletion := func(connection *harborv1alpha1.HarborConnection) {
		connection.Spec.AllowContentDeletion = true
	}

	t.Run("an allowed project is deleted together with every repository", func(t *testing.T) {
		t.Parallel()
		namespace := newNamespace(t, cl, "contents-allowed")
		name := runUniqueName(t, "op")
		t.Cleanup(func() { instance.DeleteProject(context.Background(), t, name) })
		connection := readyConnection(t, cl, instance, namespace, allowContentDeletion)

		project := newProject(namespace, name, connection)
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDeleteWithContents
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		instance.PushImage(t, name, "app", "v1")
		instance.PushImage(t, name, "team/app", "v1")
		require.Len(t, repositoryNames(t, instance, name), 2)

		require.NoError(t, cl.Delete(t.Context(), project))
		awaitGone(t, cl, key, "the finalizer is removed once the project and its images are deleted")

		_, err := admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})

	t.Run("the connection's default policy applies to the project", func(t *testing.T) {
		t.Parallel()
		namespace := newNamespace(t, cl, "contents-default")
		name := runUniqueName(t, "op")
		t.Cleanup(func() { instance.DeleteProject(context.Background(), t, name) })
		connection := readyConnection(t, cl, instance, namespace, func(connection *harborv1alpha1.HarborConnection) {
			connection.Spec.AllowContentDeletion = true
			connection.Spec.Defaults.DeletionPolicy = harborv1alpha1.DeletionPolicyDeleteWithContents
		})

		project := newProject(namespace, name, connection)
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		instance.PushImage(t, name, "app", "v1")

		require.NoError(t, cl.Delete(t.Context(), project))
		awaitGone(t, cl, key, "the finalizer is removed once the project and its images are deleted")
	})

	t.Run("a connection that does not allow content deletion leaves the images and blocks the deletion", func(t *testing.T) {
		t.Parallel()
		namespace := newNamespace(t, cl, "contents-refused")
		name := runUniqueName(t, "op")
		t.Cleanup(func() { instance.DeleteProject(context.Background(), t, name) })
		connection := readyConnection(t, cl, instance, namespace, nil)

		project := newProject(namespace, name, connection)
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDeleteWithContents
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		instance.PushImage(t, name, "app", "v1")
		instance.PushImage(t, name, "team/app", "v1")

		require.NoError(t, cl.Delete(t.Context(), project))
		blocked := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonDeleteBlocked)
		require.Eventually(t, func() bool {
			require.NoError(t, cl.Get(t.Context(), key, blocked))

			return strings.Contains(readyMessage(blocked), "content deletion is not allowed by HarborConnection "+namespace+"/harbor")
		}, projectWait, 200*time.Millisecond, "the message names the connection that refuses content deletion")
		awaitEvent(t, cl, blocked, harborv1alpha1.ReasonContentDeletionNotAllowed)

		assert.Len(t, repositoryNames(t, instance, name), 2, "no image is deleted")

		removeRepositories(t, instance, name)
		awaitGone(t, cl, key, "the deletion finishes once the images are removed by hand")

		_, err := admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})

	t.Run("a failure partway is reported with what is left and the deletion resumes", func(t *testing.T) {
		t.Parallel()
		namespace := newNamespace(t, cl, "contents-resume")
		name := runUniqueName(t, "op")
		t.Cleanup(func() { instance.DeleteProject(context.Background(), t, name) })

		proxy := newRepositoryDeleteFailingProxy(t, instance.URL)
		proxied := *instance
		proxied.URL = proxy.server.URL
		connection := readyConnection(t, cl, &proxied, namespace, allowContentDeletion)

		project := newProject(namespace, name, connection)
		project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDeleteWithContents
		require.NoError(t, cl.Create(t.Context(), project))
		key := client.ObjectKeyFromObject(project)
		awaitProject(t, cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		instance.PushImage(t, name, "app", "v1")
		instance.PushImage(t, name, "team/app", "v1")

		proxy.failing.Store(true)
		require.NoError(t, cl.Delete(t.Context(), project))

		failed := awaitProject(t, cl, key, metav1.ConditionFalse, harborv1alpha1.ReasonHarborUnavailable)
		assert.Contains(t, readyMessage(failed), "deleting contents: 1 repositories left")
		assert.Len(t, repositoryNames(t, instance, name), 1, "the first repository is deleted, the second is left")

		proxy.failing.Store(false)
		awaitGone(t, cl, key, "the deletion finishes by itself once Harbor answers again")

		_, err := admin.GetProjectByName(t.Context(), name)
		assert.True(t, harbor.IsNotFound(err), "the Harbor project is deleted, got %v", err)
	})
}
