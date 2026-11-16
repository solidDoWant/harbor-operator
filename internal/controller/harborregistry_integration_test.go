//go:build integration

package controller_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/controller"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
	"github.com/solidDoWant/harbor-operator/test/leakscan"
)

// registrySetup is a running operator with a ready connection in its own namespace that admits every namespace,
// for projects and for Harbor-wide kinds.
type registrySetup struct {
	instance *harborfixture.Harbor
	admin    *harbor.Client
	cl       client.Client
	// upstreamURL is where Harbor reaches the plain registry in its cluster.
	upstreamURL  string
	connection   *harborv1alpha1.HarborConnection
	ownNamespace string
	// registries and projects are the Harbor names deleted after the operator stops, so it cannot create them again.
	registries, projects []string
}

func readyRegistrySetup(t *testing.T, logger *leakscan.Log, changeConnection func(*harborv1alpha1.HarborConnection)) *registrySetup {
	t.Helper()

	instance := harborfixture.FromEnv(t)

	upstreamURL := os.Getenv("UPSTREAM_URL")
	if upstreamURL == "" {
		t.Skip("UPSTREAM_URL is not set: start Harbor with test/harbor/up.sh")
	}

	admin, err := harbor.NewClient(harbor.ConnectionConfig{URL: instance.URL, Username: instance.Username, Password: instance.Password})
	require.NoError(t, err)

	setup := &registrySetup{instance: instance, admin: admin, upstreamURL: upstreamURL}

	// Registered before the operator starts, so it runs after the operator has stopped.
	t.Cleanup(func() {
		for _, projectName := range setup.projects {
			instance.DeleteProject(context.Background(), t, projectName)
		}

		for _, registryName := range setup.registries {
			if found, err := admin.GetRegistryByName(context.Background(), registryName); err == nil {
				_ = admin.DeleteRegistry(context.Background(), found.ID)
			}
		}
	})

	var cl client.Client
	if logger != nil {
		cl = startIsolatedOperator(t, operatorOptions{logger: logger.Logger()})
	} else {
		cl = startOperator(t)
	}

	namespace := newNamespace(t, cl, "registry")
	connection := readyConnection(t, cl, instance, namespace, func(connection *harborv1alpha1.HarborConnection) {
		everything := harborv1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{}}
		connection.Spec.AllowedNamespaces = everything
		connection.Spec.SystemAllowedNamespaces = everything

		if changeConnection != nil {
			changeConnection(connection)
		}
	})

	setup.cl, setup.connection, setup.ownNamespace = cl, connection, namespace

	return setup
}

// harborRegistryName returns a unique Harbor registry name for the test. The registry, and the proxy-cache projects
// named in projects, are deleted when the test ends.
func (s *registrySetup) harborRegistryName(t *testing.T, suffix string, projects ...string) string {
	t.Helper()

	registryName := harborfixture.UniqueName(t, "reg"+suffix)
	s.registries = append(s.registries, registryName)
	s.projects = append(s.projects, projects...)

	return registryName
}

func (s *registrySetup) newRegistry(namespace, name, harborName string) *harborv1alpha1.HarborRegistry {
	return &harborv1alpha1.HarborRegistry{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: harborv1alpha1.HarborRegistrySpec{
			ConnectionRef:  harborv1alpha1.ObjectReference{Namespace: s.connection.Namespace, Name: s.connection.Name},
			Name:           harborName,
			Type:           "docker-registry",
			URL:            s.upstreamURL,
			ResyncInterval: &metav1.Duration{Duration: 2 * time.Second},
		},
	}
}

func (s *registrySetup) newProxyProject(namespace, harborName string, registry client.ObjectKey) *harborv1alpha1.HarborProject {
	project := newProject(namespace, harborName, s.connection)
	project.Spec.ProxyCache = &harborv1alpha1.ProxyCacheSpec{RegistryRef: harborv1alpha1.ObjectReference{Namespace: registry.Namespace, Name: registry.Name}}

	return project
}

func awaitRegistry(t *testing.T, cl client.Client, key client.ObjectKey, status metav1.ConditionStatus, reason string) *harborv1alpha1.HarborRegistry {
	t.Helper()

	registry := &harborv1alpha1.HarborRegistry{}

	require.Eventually(t, func() bool {
		if err := cl.Get(t.Context(), key, registry); err != nil {
			return false
		}

		condition := meta.FindStatusCondition(registry.Status.Conditions, harborv1alpha1.ConditionReady)

		return condition != nil && condition.Status == status && condition.Reason == reason && condition.ObservedGeneration == registry.Generation
	}, projectWait, 200*time.Millisecond, "waiting for Ready=%s %s, have %+v", status, reason, registry.Status.Conditions)

	return registry
}

func awaitHarborRegistry(t *testing.T, admin *harbor.Client, registryName string, check func(*harbor.Registry) bool, message string) {
	t.Helper()

	require.Eventually(t, func() bool {
		found, err := admin.GetRegistryByName(t.Context(), registryName)
		return err == nil && check(found)
	}, projectWait, 200*time.Millisecond, message)
}

func registryAbsent(t *testing.T, admin *harbor.Client, registryName string) {
	t.Helper()

	_, err := admin.GetRegistryByName(t.Context(), registryName)
	assert.True(t, harbor.IsNotFound(err), "no Harbor registry %q should exist, got %v", registryName, err)
}

func registryCredentials(namespace, name, accessKey, accessSecret string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       map[string][]byte{"accessKey": []byte(accessKey), "accessSecret": []byte(accessSecret)},
	}
}

func updateRegistry(t *testing.T, cl client.Client, key client.ObjectKey, change func(*harborv1alpha1.HarborRegistry)) {
	t.Helper()

	require.Eventually(t, func() bool {
		registry := &harborv1alpha1.HarborRegistry{}
		if err := cl.Get(t.Context(), key, registry); err != nil {
			return false
		}

		change(registry)

		return cl.Update(t.Context(), registry) == nil
	}, 10*time.Second, 100*time.Millisecond)
}

func TestRegistryIsCreatedReportsItsHealthAndBecomesReady(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)

	t.Run("docker-registry upstream in the cluster", func(t *testing.T) {
		harborName := setup.harborRegistryName(t, "")
		registry := setup.newRegistry(setup.ownNamespace, "upstream", harborName)
		registry.Spec.Description = "in-cluster upstream"
		require.NoError(t, setup.cl.Create(t.Context(), registry))

		ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		assert.Equal(t, harborv1alpha1.RegistryHealthHealthy, ready.Status.Health)
		assert.NotZero(t, ready.Status.RegistryID)
		assert.Nil(t, ready.Status.CreatePending)
		awaitEvent(t, setup.cl, ready, harborv1alpha1.EventReasonHealthChanged)

		found, err := setup.admin.GetRegistry(t.Context(), ready.Status.RegistryID)
		require.NoError(t, err)
		assert.Equal(t, harborName, found.Name)
		assert.Equal(t, "docker-registry", found.Type)
		assert.Equal(t, setup.upstreamURL, found.URL)
		assert.Equal(t, "in-cluster upstream", found.Description)
	})

	t.Run("github-ghcr", func(t *testing.T) {
		harborName := setup.harborRegistryName(t, "")
		registry := setup.newRegistry(setup.ownNamespace, "ghcr", harborName)
		registry.Spec.Type = "github-ghcr"
		registry.Spec.URL = "https://ghcr.io"
		require.NoError(t, setup.cl.Create(t.Context(), registry))

		// CI has no outbound access, so only Harbor accepting the type is checked, not the upstream's health.
		ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		assert.NotEmpty(t, ready.Status.Health)

		found, err := setup.admin.GetRegistry(t.Context(), ready.Status.RegistryID)
		require.NoError(t, err)
		assert.Equal(t, "github-ghcr", found.Type)
	})
}

func TestRegistryCredentialsAreStoredAndFollowTheSecret(t *testing.T) {
	t.Parallel()

	log := &leakscan.Log{}
	setup := readyRegistrySetup(t, log, nil)
	harborName := setup.harborRegistryName(t, "")

	require.NoError(t, setup.cl.Create(t.Context(), registryCredentials(setup.ownNamespace, "upstream-creds", "first-user", "first-access-secret-value")))

	registry := setup.newRegistry(setup.ownNamespace, "upstream", harborName)
	registry.Spec.CredentialsSecretRef = &harborv1alpha1.LocalSecretReference{Name: "upstream-creds"}
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	key := client.ObjectKeyFromObject(registry)
	awaitRegistry(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	awaitHarborRegistry(t, setup.admin, harborName, func(found *harbor.Registry) bool { return found.AccessKey == "first-user" }, "Harbor stores the access key")

	// A credential changed in Harbor is put back.
	found, err := setup.admin.GetRegistryByName(t.Context(), harborName)
	require.NoError(t, err)
	require.NoError(t, setup.admin.UpdateRegistry(t.Context(), found.ID, harbor.RegistrySpec{
		Name: found.Name, URL: found.URL, Description: found.Description, Insecure: found.Insecure,
		Credential: &harbor.RegistryCredential{AccessKey: "changed-in-ui", AccessSecret: "changed-in-ui-secret"},
	}))
	awaitHarborRegistry(t, setup.admin, harborName, func(found *harbor.Registry) bool { return found.AccessKey == "first-user" }, "the credential is put back")

	// A changed Secret updates Harbor.
	require.Eventually(t, func() bool {
		secret := &corev1.Secret{}
		if err := setup.cl.Get(t.Context(), client.ObjectKey{Namespace: setup.ownNamespace, Name: "upstream-creds"}, secret); err != nil {
			return false
		}

		secret.Data["accessKey"] = []byte("second-user")
		secret.Data["accessSecret"] = []byte("second-access-secret-value")

		return setup.cl.Update(t.Context(), secret) == nil
	}, 10*time.Second, 100*time.Millisecond)
	awaitHarborRegistry(t, setup.admin, harborName, func(found *harbor.Registry) bool { return found.AccessKey == "second-user" }, "the new Secret reaches Harbor")

	secrets := map[string]string{
		"first access secret": "first-access-secret-value", "second access secret": "second-access-secret-value",
		"connection password": setup.instance.Password,
	}
	leaks, err := leakscan.Scan(t.Context(), setup.cl, log, []client.ObjectList{&harborv1alpha1.HarborRegistryList{}, &harborv1alpha1.HarborConnectionList{}}, secrets)
	require.NoError(t, err)
	assert.Empty(t, leaks, "registry credentials leaked outside Kubernetes Secrets")
}

func TestRegistryWithMissingOrIncompleteCredentialsWritesNothing(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)

	tests := []struct {
		name   string
		secret *corev1.Secret
	}{
		{name: "Secret missing"},
		{name: "accessSecret missing", secret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: setup.ownNamespace, Name: "incomplete"},
			Data:       map[string][]byte{"accessKey": []byte("user")},
		}},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harborName := setup.harborRegistryName(t, "")
			secretName := "absent"

			if test.secret != nil {
				require.NoError(t, setup.cl.Create(t.Context(), test.secret))
				secretName = test.secret.Name
			}

			registry := setup.newRegistry(setup.ownNamespace, fmt.Sprintf("registry-%d", index), harborName)
			registry.Spec.CredentialsSecretRef = &harborv1alpha1.LocalSecretReference{Name: secretName}
			require.NoError(t, setup.cl.Create(t.Context(), registry))

			awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionFalse, harborv1alpha1.ReasonCredentialsInvalid)
			registryAbsent(t, setup.admin, harborName)
		})
	}
}

func TestRegistryDriftInHarborIsPutBack(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)
	harborName := setup.harborRegistryName(t, "")

	registry := setup.newRegistry(setup.ownNamespace, "upstream", harborName)
	registry.Spec.Description = "managed"
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	require.NoError(t, setup.admin.UpdateRegistry(t.Context(), ready.Status.RegistryID, harbor.RegistrySpec{
		// Harbor checks that a new URL answers, so the drift points at the same upstream under another name.
		Name: harborName, URL: "http://upstream.upstream.svc:5000", Description: "changed in the UI", Insecure: true,
	}))

	awaitHarborRegistry(t, setup.admin, harborName, func(found *harbor.Registry) bool {
		return found.URL == setup.upstreamURL && found.Description == "managed" && !found.Insecure
	}, "url, description and insecure are put back")
}

func TestRegistryRenamedInHarborStaysWithItsOwner(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true })
	otherTeam := newNamespace(t, setup.cl, "other-team")
	ownerName := setup.harborRegistryName(t, "own")
	renamed := setup.harborRegistryName(t, "new")

	owner := setup.newRegistry(setup.ownNamespace, "owner", ownerName)
	owner.Spec.Description = "owned"
	// A long resync keeps the owner from putting its name back before the other resource has looked.
	owner.Spec.ResyncInterval = &metav1.Duration{Duration: time.Hour}
	require.NoError(t, setup.cl.Create(t.Context(), owner))
	id := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(owner), metav1.ConditionTrue, harborv1alpha1.ReasonReady).Status.RegistryID

	// Any reconcile of the owner after the rename puts its name back, so the rename waits until the owner has
	// stopped writing its status.
	lastVersion := ""

	require.Eventually(t, func() bool {
		current := &harborv1alpha1.HarborRegistry{}
		if err := setup.cl.Get(t.Context(), client.ObjectKeyFromObject(owner), current); err != nil || current.Status.Health == "" {
			return false
		}

		settled := current.ResourceVersion == lastVersion
		lastVersion = current.ResourceVersion

		return settled
	}, projectWait, time.Second, "the owner settles")

	require.NoError(t, setup.admin.UpdateRegistry(t.Context(), id, harbor.RegistrySpec{
		Name: renamed, URL: setup.upstreamURL, Description: "renamed in the UI",
	}))

	for _, adopt := range []bool{true, false} {
		t.Run(fmt.Sprintf("a resource with the new name and adopt %t gets Conflict and is deleted without deleting it", adopt), func(t *testing.T) {
			other := setup.newRegistry(otherTeam, fmt.Sprintf("other-%t", adopt), renamed)
			other.Spec.Description = "taken over"
			other.Spec.Adopt = adopt
			other.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
			require.NoError(t, setup.cl.Create(t.Context(), other))

			conflict := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(other), metav1.ConditionFalse, harborv1alpha1.ReasonConflict)
			assert.Zero(t, conflict.Status.RegistryID)

			found, err := setup.admin.GetRegistry(t.Context(), id)
			require.NoError(t, err)
			assert.Equal(t, renamed, found.Name)
			assert.Equal(t, "renamed in the UI", found.Description, "the other resource writes nothing")

			require.NoError(t, setup.cl.Delete(t.Context(), other))
			awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(other))

			_, err = setup.admin.GetRegistry(t.Context(), id)
			require.NoError(t, err, "the owner's registry still exists")
		})
	}

	t.Run("the owner puts its name back and stays Ready", func(t *testing.T) {
		updateRegistry(t, setup.cl, client.ObjectKeyFromObject(owner), func(registry *harborv1alpha1.HarborRegistry) {
			registry.Spec.ResyncInterval = &metav1.Duration{Duration: 2 * time.Second}
		})

		awaitHarborRegistry(t, setup.admin, ownerName, func(found *harbor.Registry) bool {
			return found.ID == id && found.Description == "owned"
		}, "the owner renames its registry back")

		ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(owner), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		assert.Equal(t, id, ready.Status.RegistryID)
	})
}

func TestRegistryInANamespaceTheConnectionAdmitsOnlyForProjects(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, func(connection *harborv1alpha1.HarborConnection) {
		connection.Spec.SystemAllowedNamespaces = harborv1alpha1.AllowedNamespaces{}
	})
	team := newNamespace(t, setup.cl, "team")
	harborName := setup.harborRegistryName(t, "")

	registry := setup.newRegistry(team, "upstream", harborName)
	require.NoError(t, setup.cl.Create(t.Context(), registry))

	awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)
	registryAbsent(t, setup.admin, harborName)
}

func TestRegistryDeletion(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)

	t.Run("policy Delete deletes the Harbor registry", func(t *testing.T) {
		harborName := setup.harborRegistryName(t, "")
		registry := setup.newRegistry(setup.ownNamespace, "delete", harborName)
		require.NoError(t, setup.cl.Create(t.Context(), registry))
		awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.NoError(t, setup.cl.Delete(t.Context(), registry))
		awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(registry))
		registryAbsent(t, setup.admin, harborName)
	})

	t.Run("policy DeleteWithContents deletes the Harbor registry like Delete", func(t *testing.T) {
		harborName := setup.harborRegistryName(t, "")
		registry := setup.newRegistry(setup.ownNamespace, "delete-with-contents", harborName)
		registry.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDeleteWithContents
		require.NoError(t, setup.cl.Create(t.Context(), registry))
		awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.NoError(t, setup.cl.Delete(t.Context(), registry))
		awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(registry))
		registryAbsent(t, setup.admin, harborName)
	})

	t.Run("policy Orphan keeps the Harbor registry", func(t *testing.T) {
		harborName := setup.harborRegistryName(t, "")
		registry := setup.newRegistry(setup.ownNamespace, "orphan", harborName)
		registry.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyOrphan
		require.NoError(t, setup.cl.Create(t.Context(), registry))
		awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		require.NoError(t, setup.cl.Delete(t.Context(), registry))
		awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(registry))

		_, err := setup.admin.GetRegistryByName(t.Context(), harborName)
		require.NoError(t, err, "the orphaned Harbor registry stays")
	})

	for _, policy := range []harborv1alpha1.DeletionPolicy{harborv1alpha1.DeletionPolicyDelete, harborv1alpha1.DeletionPolicyOrphan} {
		t.Run("waits for an admitted proxy-cache project under "+string(policy), func(t *testing.T) {
			projectName := harborfixture.UniqueName(t, "proxy")
			harborName := setup.harborRegistryName(t, "", projectName)
			registry := setup.newRegistry(setup.ownNamespace, "used-"+strings.ToLower(string(policy)), harborName)
			registry.Spec.DeletionPolicy = policy
			require.NoError(t, setup.cl.Create(t.Context(), registry))
			registryKey := client.ObjectKeyFromObject(registry)
			awaitRegistry(t, setup.cl, registryKey, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			project := setup.newProxyProject(setup.ownNamespace, projectName, registryKey)
			project.Name = registry.Name
			project.Spec.DeletionPolicy = harborv1alpha1.DeletionPolicyDelete
			project.Spec.Public = ptr(false)
			require.NoError(t, setup.cl.Create(t.Context(), project))
			awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			require.NoError(t, setup.cl.Delete(t.Context(), registry))
			held := awaitRegistry(t, setup.cl, registryKey, metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
			assert.Contains(t, meta.FindStatusCondition(held.Status.Conditions, harborv1alpha1.ConditionReady).Message, setup.ownNamespace+"/"+project.Name)
			assert.Contains(t, held.Finalizers, controller.RegistryFinalizer)

			// The project keeps reconciling while its registry is being deleted.
			_, err := setup.admin.GetProjectByName(t.Context(), projectName)
			require.NoError(t, err)
			require.NoError(t, setup.admin.UpdateProjectMetadata(t.Context(), projectName, harbor.ProjectSettings{Public: ptr(true)}))
			require.Eventually(t, func() bool {
				found, err := setup.admin.GetProjectByName(t.Context(), projectName)
				return err == nil && found.Settings.Public != nil && !*found.Settings.Public
			}, projectWait, 200*time.Millisecond, "the project is still reconciled")

			// Deleting the project lets the registry go.
			require.NoError(t, setup.cl.Delete(t.Context(), project))
			awaitGone(t, setup.cl, client.ObjectKeyFromObject(project), "the proxy-cache project goes")
			awaitRegistryGone(t, setup.cl, registryKey)

			_, err = setup.admin.GetRegistryByName(t.Context(), harborName)
			assert.Equal(t, policy == harborv1alpha1.DeletionPolicyDelete, harbor.IsNotFound(err), "Harbor registry deleted only under Delete, got %v", err)
		})
	}

	t.Run("Harbor refusing the delete is DeleteBlocked", func(t *testing.T) {
		projectName := harborfixture.UniqueName(t, "byhand")
		harborName := setup.harborRegistryName(t, "", projectName)
		registry := setup.newRegistry(setup.ownNamespace, "blocked", harborName)
		require.NoError(t, setup.cl.Create(t.Context(), registry))
		ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

		// A proxy-cache project made in Harbor, not by a resource, makes Harbor refuse the delete.
		_, err := setup.admin.CreateProxyCacheProject(t.Context(), projectName, harbor.ProjectSettings{}, nil, ready.Status.RegistryID)
		require.NoError(t, err)

		require.NoError(t, setup.cl.Delete(t.Context(), registry))
		blocked := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionFalse, harborv1alpha1.ReasonDeleteBlocked)
		assert.NotEmpty(t, meta.FindStatusCondition(blocked.Status.Conditions, harborv1alpha1.ConditionReady).Message, "Harbor's message is reported")

		setup.instance.DeleteProject(t.Context(), t, projectName)
		awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(registry))
		registryAbsent(t, setup.admin, harborName)
	})
}

func awaitRegistryGone(t *testing.T, cl client.Client, key client.ObjectKey) {
	t.Helper()

	require.Eventually(t, func() bool {
		return cl.Get(t.Context(), key, &harborv1alpha1.HarborRegistry{}) != nil
	}, projectWait, 200*time.Millisecond, "the HarborRegistry should be gone")
}

func TestConnectionDeletionWaitsForAdmittedRegistries(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)
	namespaces := map[string]string{"admitted": newNamespace(t, setup.cl, "admitted"), "outsider": newNamespace(t, setup.cl, "outsider")}

	// The namespace names are random, so the connection narrows its Harbor-wide admission once they exist.
	require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKeyFromObject(setup.connection), setup.connection))
	setup.connection.Spec.SystemAllowedNamespaces = harborv1alpha1.AllowedNamespaces{Names: []string{namespaces["admitted"]}}
	require.NoError(t, setup.cl.Update(t.Context(), setup.connection))
	awaitObservedGeneration(t, setup.cl, setup.connection)

	held := func(namespace string) *harborv1alpha1.HarborRegistry {
		registry := setup.newRegistry(namespace, "upstream", setup.harborRegistryName(t, "-"+namespace[:strings.LastIndex(namespace, "-")]))
		registry.Finalizers = []string{"test/hold"}
		require.NoError(t, setup.cl.Create(t.Context(), registry))

		return registry
	}

	admitted, outsider := held(namespaces["admitted"]), held(namespaces["outsider"])

	require.NoError(t, setup.cl.Delete(t.Context(), setup.connection))

	connection := awaitReady(t, setup.cl, client.ObjectKeyFromObject(setup.connection), metav1.ConditionFalse, harborv1alpha1.ReasonDependentsExist)
	message := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionReady).Message
	assert.Contains(t, message, "HarborRegistry "+namespaces["admitted"]+"/upstream")
	assert.NotContains(t, message, namespaces["outsider"]+"/upstream", "registries in namespaces the connection does not admit for Harbor-wide kinds are ignored")

	for _, registry := range []*harborv1alpha1.HarborRegistry{admitted, outsider} {
		require.NoError(t, setup.cl.Get(t.Context(), client.ObjectKeyFromObject(registry), registry))
		registry.Finalizers = nil
		require.NoError(t, setup.cl.Update(t.Context(), registry))
		require.NoError(t, setup.cl.Delete(t.Context(), registry))
	}

	require.Eventually(t, func() bool {
		var current harborv1alpha1.HarborConnection
		return client.IgnoreNotFound(setup.cl.Get(t.Context(), client.ObjectKeyFromObject(setup.connection), &current)) == nil && current.Name == ""
	}, 60*time.Second, 200*time.Millisecond, "connection should be gone")
}

// pushUpstream pushes a small random image to the plain registry in Harbor's cluster and returns its digest.
func pushUpstream(t *testing.T, repository, tag string) string {
	t.Helper()

	host := os.Getenv("UPSTREAM_REGISTRY")
	if host == "" {
		t.Skip("UPSTREAM_REGISTRY is not set: recreate the cluster with test/harbor/up.sh")
	}

	image, err := random.Image(64, 1)
	require.NoError(t, err)

	reference, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", host, repository, tag), name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(reference, image, remote.WithContext(t.Context())))

	digest, err := image.Digest()
	require.NoError(t, err)

	return digest.String()
}

func TestProxyCacheProjectPullsTheUpstreamImage(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)
	projectName := harborfixture.UniqueName(t, "proxy")
	harborName := setup.harborRegistryName(t, "", projectName)

	registry := setup.newRegistry(setup.ownNamespace, "upstream", harborName)
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	project := setup.newProxyProject(setup.ownNamespace, projectName, client.ObjectKeyFromObject(registry))
	project.Spec.ProxyCache.SpeedLimitKbps = ptr(int32(2048))
	require.NoError(t, setup.cl.Create(t.Context(), project))
	awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	found, err := setup.admin.GetProjectByName(t.Context(), projectName)
	require.NoError(t, err)
	assert.Equal(t, ready.Status.RegistryID, found.RegistryID, "the project is a proxy cache of the registry")
	require.NotNil(t, found.Settings.ProxySpeedKbps)
	assert.Equal(t, int32(2048), *found.Settings.ProxySpeedKbps)

	digest := pushUpstream(t, "pulled/image", "v1")

	reference, err := name.ParseReference(fmt.Sprintf("%s/%s/pulled/image:v1", setup.instance.Registry(t), projectName), name.Insecure)
	require.NoError(t, err)

	pulled, err := remote.Image(reference, remote.WithContext(t.Context()),
		remote.WithAuth(&authn.Basic{Username: setup.instance.Username, Password: setup.instance.Password}))
	require.NoError(t, err)

	pulledDigest, err := pulled.Digest()
	require.NoError(t, err)
	assert.Equal(t, digest, pulledDigest.String(), "Harbor returns the upstream image")
}

func TestProxyCacheProjectDriftInHarborIsPutBack(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)
	projectName := harborfixture.UniqueName(t, "proxy")
	harborName := setup.harborRegistryName(t, "", projectName)
	otherName := setup.harborRegistryName(t, "other")

	registry := setup.newRegistry(setup.ownNamespace, "upstream", harborName)
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	project := setup.newProxyProject(setup.ownNamespace, projectName, client.ObjectKeyFromObject(registry))
	project.Spec.ProxyCache.SpeedLimitKbps = ptr(int32(-1))
	require.NoError(t, setup.cl.Create(t.Context(), project))
	awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	t.Run("speed limit", func(t *testing.T) {
		require.NoError(t, setup.admin.UpdateProjectMetadata(t.Context(), projectName, harbor.ProjectSettings{ProxySpeedKbps: ptr(int32(10))}))

		require.Eventually(t, func() bool {
			found, err := setup.admin.GetProjectByName(t.Context(), projectName)
			return err == nil && found.Settings.ProxySpeedKbps != nil && *found.Settings.ProxySpeedKbps == -1
		}, projectWait, 200*time.Millisecond, "proxy_speed_kb is put back")
	})

	t.Run("registry re-created with a new ID", func(t *testing.T) {
		// Harbor keeps a proxy-cache project's registry, so the project reports it and writes nothing else.
		require.NoError(t, setup.cl.Delete(t.Context(), registry))
		require.Eventually(t, func() bool {
			current := &harborv1alpha1.HarborRegistry{}
			if err := setup.cl.Get(t.Context(), client.ObjectKeyFromObject(registry), current); err != nil {
				return apierrors.IsNotFound(err)
			}

			// The project holds the registry's deletion, so drop the finalizer as a forced deletion would.
			current.Finalizers = nil

			return setup.cl.Update(t.Context(), current) == nil
		}, projectWait, 200*time.Millisecond)
		awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(registry))

		recreated := setup.newRegistry(setup.ownNamespace, "upstream", otherName)
		require.NoError(t, setup.cl.Create(t.Context(), recreated))
		newer := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(recreated), metav1.ConditionTrue, harborv1alpha1.ReasonReady)
		require.NotEqual(t, ready.Status.RegistryID, newer.Status.RegistryID)

		refused := awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonInvalidSpec)
		condition := meta.FindStatusCondition(refused.Status.Conditions, harborv1alpha1.ConditionReady)
		require.NotNil(t, condition)
		assert.Contains(t, condition.Message, fmt.Sprintf("registry ID %d", ready.Status.RegistryID))
		assert.Contains(t, condition.Message, fmt.Sprintf("ID %d", newer.Status.RegistryID))

		// Drift in Harbor and a spec change: neither is written while Harbor keeps the old registry.
		require.NoError(t, setup.admin.UpdateProjectMetadata(t.Context(), projectName, harbor.ProjectSettings{ProxySpeedKbps: ptr(int32(20))}))

		var generation int64

		require.Eventually(t, func() bool {
			current := &harborv1alpha1.HarborProject{}
			if err := setup.cl.Get(t.Context(), client.ObjectKeyFromObject(project), current); err != nil {
				return false
			}

			current.Spec.Public = ptr(true)
			if err := setup.cl.Update(t.Context(), current); err != nil {
				return false
			}

			generation = current.Generation

			return true
		}, projectWait, 200*time.Millisecond)

		require.Eventually(t, func() bool {
			current := &harborv1alpha1.HarborProject{}
			if err := setup.cl.Get(t.Context(), client.ObjectKeyFromObject(project), current); err != nil {
				return false
			}

			condition := meta.FindStatusCondition(current.Status.Conditions, harborv1alpha1.ConditionReady)

			return condition != nil && condition.ObservedGeneration == generation && condition.Reason == harborv1alpha1.ReasonInvalidSpec
		}, projectWait, 200*time.Millisecond, "the spec change is reconciled and still reports InvalidSpec")

		found, err := setup.admin.GetProjectByName(t.Context(), projectName)
		require.NoError(t, err)
		assert.Equal(t, ready.Status.RegistryID, found.RegistryID, "Harbor keeps the project's registry")
		require.NotNil(t, found.Settings.ProxySpeedKbps)
		assert.Equal(t, int32(20), *found.Settings.ProxySpeedKbps, "proxy_speed_kb is not written while the registry differs")
		assert.False(t, found.Settings.Public != nil && *found.Settings.Public, "public is not written while the registry differs")
	})
}

func TestProxyCacheProjectWaitsForItsRegistry(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)
	projectName := harborfixture.UniqueName(t, "proxy")
	harborName := setup.harborRegistryName(t, "", projectName)

	project := setup.newProxyProject(setup.ownNamespace, projectName, client.ObjectKey{Namespace: setup.ownNamespace, Name: "later"})
	require.NoError(t, setup.cl.Create(t.Context(), project))

	waiting := awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonRegistryNotReady)
	reference := meta.FindStatusCondition(waiting.Status.Conditions, harborv1alpha1.ConditionReferenceResolved)
	require.NotNil(t, reference)
	assert.Equal(t, harborv1alpha1.ReasonRegistryNotReady, reference.Reason)

	_, err := setup.admin.GetProjectByName(t.Context(), projectName)
	assert.True(t, harbor.IsNotFound(err), "nothing is created while the registry is missing, got %v", err)

	registry := setup.newRegistry(setup.ownNamespace, "later", harborName)
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	found, err := setup.admin.GetProjectByName(t.Context(), projectName)
	require.NoError(t, err)
	assert.Equal(t, ready.Status.RegistryID, found.RegistryID)
}

func TestProxyCacheProjectInANamespaceTheRegistryDoesNotAdmit(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)
	team := newNamespace(t, setup.cl, "team")
	projectName := harborfixture.UniqueName(t, "proxy")
	harborName := setup.harborRegistryName(t, "", projectName)

	registry := setup.newRegistry(setup.ownNamespace, "upstream", harborName)
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	project := setup.newProxyProject(team, projectName, client.ObjectKeyFromObject(registry))
	require.NoError(t, setup.cl.Create(t.Context(), project))

	awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed)

	_, err := setup.admin.GetProjectByName(t.Context(), projectName)
	assert.True(t, harbor.IsNotFound(err), "nothing is created for a namespace the registry does not admit, got %v", err)

	// The registry does not wait for a project it does not admit.
	require.NoError(t, setup.cl.Delete(t.Context(), registry))
	awaitRegistryGone(t, setup.cl, client.ObjectKeyFromObject(registry))
}

func TestProxyCacheProjectWithItsRegistryInAnotherHarbor(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, nil)
	projectName := harborfixture.UniqueName(t, "proxy")

	// The registry's connection does not exist, so the operator leaves the claim written here alone.
	registry := setup.newRegistry(setup.ownNamespace, "elsewhere", "elsewhere")
	registry.Spec.ConnectionRef = harborv1alpha1.ObjectReference{Name: "other-harbor"}
	require.NoError(t, setup.cl.Create(t.Context(), registry))
	awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionFalse, harborv1alpha1.ReasonNotFound)

	require.Eventually(t, func() bool {
		current := &harborv1alpha1.HarborRegistry{}
		if err := setup.cl.Get(t.Context(), client.ObjectKeyFromObject(registry), current); err != nil {
			return false
		}

		current.Status.HarborKey = "https://other-harbor.example/registries/elsewhere"
		current.Status.RegistryID = 1

		return setup.cl.Status().Update(t.Context(), current) == nil
	}, 10*time.Second, 100*time.Millisecond)

	project := setup.newProxyProject(setup.ownNamespace, projectName, client.ObjectKeyFromObject(registry))
	require.NoError(t, setup.cl.Create(t.Context(), project))
	setup.projects = append(setup.projects, projectName)

	awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonRegistryOnOtherHarbor)

	_, err := setup.admin.GetProjectByName(t.Context(), projectName)
	assert.True(t, harbor.IsNotFound(err), "nothing is created, got %v", err)
}

func TestProxyCacheProjectAdoptionOfAProjectInTheOtherModeIsRefused(t *testing.T) {
	t.Parallel()

	setup := readyRegistrySetup(t, nil, func(connection *harborv1alpha1.HarborConnection) { connection.Spec.AllowAdoption = true })

	tests := []struct {
		name  string
		proxy bool
	}{
		{name: "normal Harbor project, proxy-cache spec", proxy: false},
		{name: "proxy-cache Harbor project, normal spec", proxy: true},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectName := harborfixture.UniqueName(t, "adopt")
			harborName := setup.harborRegistryName(t, "", projectName)

			registry := setup.newRegistry(setup.ownNamespace, fmt.Sprintf("upstream-%d", index), harborName)
			require.NoError(t, setup.cl.Create(t.Context(), registry))
			ready := awaitRegistry(t, setup.cl, client.ObjectKeyFromObject(registry), metav1.ConditionTrue, harborv1alpha1.ReasonReady)

			var registryID int64
			if test.proxy {
				registryID = ready.Status.RegistryID
			}

			_, err := setup.admin.CreateProxyCacheProject(t.Context(), projectName, harbor.ProjectSettings{Public: ptr(false)}, nil, registryID)
			require.NoError(t, err)

			project := newProject(setup.ownNamespace, projectName, setup.connection)
			project.Name = fmt.Sprintf("adopt-%d", index)
			project.Spec.Adopt = true
			project.Spec.Public = ptr(true)

			if !test.proxy {
				project.Spec.ProxyCache = &harborv1alpha1.ProxyCacheSpec{RegistryRef: harborv1alpha1.ObjectReference{Name: registry.Name}}
			}

			require.NoError(t, setup.cl.Create(t.Context(), project))

			refused := awaitProject(t, setup.cl, client.ObjectKeyFromObject(project), metav1.ConditionFalse, harborv1alpha1.ReasonInvalidSpec)
			assert.Contains(t, readyMessage(refused), "proxy-cache mode can't be changed")
			assert.Zero(t, refused.Status.ProjectID, "the project is not adopted")

			found, err := setup.admin.GetProjectByName(t.Context(), projectName)
			require.NoError(t, err)
			assert.False(t, *found.Settings.Public, "nothing is written to the Harbor project")
		})
	}
}

// awaitObservedGeneration waits until the connection controller has seen the connection's current spec.
func awaitObservedGeneration(t *testing.T, cl client.Client, connection *harborv1alpha1.HarborConnection) {
	t.Helper()

	require.Eventually(t, func() bool {
		var current harborv1alpha1.HarborConnection

		return cl.Get(t.Context(), client.ObjectKeyFromObject(connection), &current) == nil && current.Status.ObservedGeneration == current.Generation
	}, projectWait, 200*time.Millisecond, "the connection controller should see the new spec")
}
