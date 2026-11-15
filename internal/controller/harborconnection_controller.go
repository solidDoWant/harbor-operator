// Package controller holds one reconciler per custom resource kind.
package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

const (
	// ConnectionFinalizer keeps a HarborConnection while resources use it.
	ConnectionFinalizer = "harbor.soliddowant.dev/connection"

	// ConnectionCredentialsSecretIndex finds the connections that read a Secret. Its value is "namespace/name".
	ConnectionCredentialsSecretIndex = "spec.credentialsSecretRef"
	// ProjectConnectionIndex finds the projects that use a connection. Its value is "namespace/name".
	ProjectConnectionIndex = "spec.connectionRef"

	defaultResyncInterval = 10 * time.Minute
	maxListedDependents   = 5
)

// HarborConnectionReconciler checks that a Harbor accepts a connection's credentials and records what Harbor reports.
type HarborConnectionReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborconnections,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborconnections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborconnections/finalizers,verbs=update
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects,verbs=get;list;watch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojectrobots,verbs=get;list;watch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborimmutabilitypolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborregistries,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// IndexFields registers the field indexes the connection and project reconcilers need. Call it once per manager.
func IndexFields(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &harborv1alpha1.HarborConnection{}, ConnectionCredentialsSecretIndex, func(object client.Object) []string {
		connection := object.(*harborv1alpha1.HarborConnection)
		return []string{connection.Namespace + "/" + connection.Spec.CredentialsSecretRef.Name}
	}); err != nil {
		return err
	}

	if err := indexer.IndexField(ctx, &harborv1alpha1.HarborProject{}, ProjectConnectionIndex, func(object client.Object) []string {
		return []string{connectionKey(object.(*harborv1alpha1.HarborProject)).String()}
	}); err != nil {
		return err
	}

	if err := indexer.IndexField(ctx, &harborv1alpha1.HarborProject{}, ProjectRegistryIndex, func(object client.Object) []string {
		project := object.(*harborv1alpha1.HarborProject)
		if project.Spec.ProxyCache == nil {
			return nil
		}

		return []string{projectRegistryKey(project).String()}
	}); err != nil {
		return err
	}

	if err := indexer.IndexField(ctx, &harborv1alpha1.HarborRegistry{}, RegistryConnectionIndex, func(object client.Object) []string {
		return []string{registryConnectionKey(object.(*harborv1alpha1.HarborRegistry)).String()}
	}); err != nil {
		return err
	}

	if err := indexer.IndexField(ctx, &harborv1alpha1.HarborRegistry{}, RegistryCredentialsSecretIndex, func(object client.Object) []string {
		registry := object.(*harborv1alpha1.HarborRegistry)
		if registry.Spec.CredentialsSecretRef == nil {
			return nil
		}

		return []string{registry.Namespace + "/" + registry.Spec.CredentialsSecretRef.Name}
	}); err != nil {
		return err
	}

	if err := indexer.IndexField(ctx, &harborv1alpha1.HarborProjectRobot{}, RobotProjectIndex, func(object client.Object) []string {
		return []string{robotProjectKey(object.(*harborv1alpha1.HarborProjectRobot)).String()}
	}); err != nil {
		return err
	}

	return indexer.IndexField(ctx, &harborv1alpha1.HarborImmutabilityPolicy{}, ImmutabilityProjectIndex, func(object client.Object) []string {
		return []string{policyProjectKey(object.(*harborv1alpha1.HarborImmutabilityPolicy)).String()}
	})
}

func connectionKey(project *harborv1alpha1.HarborProject) types.NamespacedName {
	namespace := project.Spec.ConnectionRef.Namespace
	if namespace == "" {
		namespace = project.Namespace
	}

	return types.NamespacedName{Namespace: namespace, Name: project.Spec.ConnectionRef.Name}
}

// SetupWithManager registers the reconciler with its watches: the credential Secret and the projects and registries
// that use the connection.
func (r *HarborConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborConnection{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.connectionsForSecret)).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: connectionKey(object.(*harborv1alpha1.HarborProject))}}
		})).
		Watches(&harborv1alpha1.HarborRegistry{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: registryConnectionKey(object.(*harborv1alpha1.HarborRegistry))}}
		})).
		Complete(r)
}

func (r *HarborConnectionReconciler) connectionsForSecret(ctx context.Context, object client.Object) []reconcile.Request {
	var connections harborv1alpha1.HarborConnectionList
	if err := r.List(ctx, &connections, client.MatchingFields{ConnectionCredentialsSecretIndex: object.GetNamespace() + "/" + object.GetName()}); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(connections.Items))
	for index := range connections.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&connections.Items[index])})
	}

	return requests
}

// Reconcile validates the connection's credentials against Harbor and records the result in status.
func (r *HarborConnectionReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var connection harborv1alpha1.HarborConnection
	if err := r.Get(ctx, request.NamespacedName, &connection); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !connection.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &connection)
	}

	if controllerutil.AddFinalizer(&connection, ConnectionFinalizer) {
		if err := r.Update(ctx, &connection); err != nil {
			return reconcile.Result{}, err
		}
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, &connection)
	if harborClient == nil {
		// A fix to the Secret or the spec triggers a new reconcile through the watch.
		return reconcile.Result{}, r.setNotReady(ctx, &connection, reason, message)
	}

	info, err := r.check(ctx, harborClient)
	if err != nil {
		reason, retry := harbor.MapError(err)
		if updateErr := r.setNotReady(ctx, &connection, reason, err.Error()); updateErr != nil {
			return reconcile.Result{}, updateErr
		}

		if !retry {
			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, err
	}

	identity := harbor.NormalizeURL(info.ExternalURL)
	if identity == "" {
		return reconcile.Result{RequeueAfter: resyncInterval(&connection)}, r.setNotReady(ctx, &connection, harborv1alpha1.ReasonHarborUnavailable, "Harbor reports no usable external_url")
	}

	connection.Status.HarborVersion = info.Version
	connection.Status.HarborIdentity = identity

	connection.Status.RegistryHost = connection.Spec.RegistryHost
	if connection.Status.RegistryHost == "" {
		connection.Status.RegistryHost = harbor.HostOf(identity)
	}

	if err := r.setCondition(ctx, &connection, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor accepted the credentials"); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: resyncInterval(&connection)}, nil
}

func resyncInterval(connection *harborv1alpha1.HarborConnection) time.Duration {
	if interval := connection.Spec.Defaults.ResyncInterval; interval != nil && interval.Duration > 0 {
		return interval.Duration
	}

	return defaultResyncInterval
}

// clientFor returns a Harbor client for the connection, or a reason and message when the credentials cannot give one.
func clientFor(ctx context.Context, reader client.Reader, clients *harbor.ClientCache, connection *harborv1alpha1.HarborConnection) (*harbor.Client, string, string) {
	secretName := connection.Spec.CredentialsSecretRef.Name

	var secret corev1.Secret
	if err := reader.Get(ctx, client.ObjectKey{Namespace: connection.Namespace, Name: secretName}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, harborv1alpha1.ReasonCredentialsInvalid, fmt.Sprintf("credentials Secret %q not found", secretName)
		}

		return nil, harborv1alpha1.ReasonCredentialsInvalid, fmt.Sprintf("reading credentials Secret %q failed", secretName)
	}

	for _, key := range []string{"username", "password"} {
		if len(secret.Data[key]) == 0 {
			return nil, harborv1alpha1.ReasonCredentialsInvalid, fmt.Sprintf("credentials Secret %q has no key %q", secretName, key)
		}
	}

	built, err := clients.Get(ctx, harbor.Connection{
		UID: connection.UID, Generation: connection.Generation, Namespace: connection.Namespace,
		URL: connection.Spec.URL, CredentialsSecret: secretName,
		CABundle: connection.Spec.CABundle, InsecureSkipVerify: connection.Spec.InsecureSkipVerify,
	})
	if err != nil {
		return nil, harborv1alpha1.ReasonInvalidSpec, err.Error()
	}

	return built, "", ""
}

func (r *HarborConnectionReconciler) check(ctx context.Context, harborClient *harbor.Client) (*harbor.SystemInfo, error) {
	info, err := harborClient.GetSystemInfo(ctx)
	if err != nil {
		return nil, err
	}

	if err := harborClient.CheckCredentials(ctx); err != nil {
		return nil, err
	}

	return info, nil
}

func (r *HarborConnectionReconciler) setNotReady(ctx context.Context, connection *harborv1alpha1.HarborConnection, reason, message string) error {
	return r.setCondition(ctx, connection, metav1.ConditionFalse, reason, message)
}

func (r *HarborConnectionReconciler) setCondition(ctx context.Context, connection *harborv1alpha1.HarborConnection, status metav1.ConditionStatus, reason, message string) error {
	connection.Status.ObservedGeneration = connection.Generation
	meta.SetStatusCondition(&connection.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: connection.Generation,
	})

	return r.Status().Update(ctx, connection)
}

// finalize keeps the finalizer while a project or registry that the connection admits still uses it.
func (r *HarborConnectionReconciler) finalize(ctx context.Context, connection *harborv1alpha1.HarborConnection) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(connection, ConnectionFinalizer) {
		return reconcile.Result{}, nil
	}

	dependents, err := r.dependents(ctx, connection)
	if err != nil {
		return reconcile.Result{}, err
	}

	if len(dependents) > 0 {
		listed := dependents
		suffix := ""

		if len(listed) > maxListedDependents {
			listed, suffix = listed[:maxListedDependents], fmt.Sprintf(" and %d more", len(dependents)-maxListedDependents)
		}

		message := "still used by " + strings.Join(listed, ", ") + suffix

		// A deleted dependent triggers a new reconcile through the project and registry watches.
		return reconcile.Result{}, r.setNotReady(ctx, connection, harborv1alpha1.ReasonDependentsExist, message)
	}

	r.Clients.Forget(connection.UID)
	controllerutil.RemoveFinalizer(connection, ConnectionFinalizer)

	return reconcile.Result{}, client.IgnoreNotFound(r.Update(ctx, connection))
}

// dependents lists "Kind namespace/name" of the projects that use the connection from a namespace its allowedNamespaces
// admits, and of the registries that use it from a namespace its systemAllowedNamespaces admits.
func (r *HarborConnectionReconciler) dependents(ctx context.Context, connection *harborv1alpha1.HarborConnection) ([]string, error) {
	key := client.ObjectKeyFromObject(connection).String()

	var projects harborv1alpha1.HarborProjectList
	if err := r.List(ctx, &projects, client.MatchingFields{ProjectConnectionIndex: key}); err != nil {
		return nil, err
	}

	var registries harborv1alpha1.HarborRegistryList
	if err := r.List(ctx, &registries, client.MatchingFields{RegistryConnectionIndex: key}); err != nil {
		return nil, err
	}

	var names []string

	for index := range projects.Items {
		project := &projects.Items[index]

		admitted, err := access.Admits(ctx, r.Client, connection.Spec.AllowedNamespaces, connection.Namespace, project.Namespace)
		if err != nil {
			return nil, err
		}

		if admitted {
			names = append(names, "HarborProject "+client.ObjectKeyFromObject(project).String())
		}
	}

	for index := range registries.Items {
		registry := &registries.Items[index]

		admitted, err := access.Admits(ctx, r.Client, connection.Spec.SystemAllowedNamespaces, connection.Namespace, registry.Namespace)
		if err != nil {
			return nil, err
		}

		if admitted {
			names = append(names, "HarborRegistry "+client.ObjectKeyFromObject(registry).String())
		}
	}

	return names, nil
}
