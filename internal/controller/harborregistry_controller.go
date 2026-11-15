package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
	"github.com/solidDoWant/harbor-operator/internal/ownership"
)

const (
	// RegistryFinalizer keeps a HarborRegistry until its Harbor registry has been dealt with.
	RegistryFinalizer = "harbor.soliddowant.dev/registry"

	// RegistryCredentialsSecretIndex finds the registries that read a Secret. Its value is "namespace/name".
	RegistryCredentialsSecretIndex = "spec.credentialsSecretRef"
	// RegistryConnectionIndex finds the registries that use a connection. Its value is "namespace/name".
	RegistryConnectionIndex = "spec.connectionRef"
	// ProjectRegistryIndex finds the proxy-cache projects that use a registry. Its value is "namespace/name", the
	// namespace defaulting to the project's own.
	ProjectRegistryIndex = "spec.proxyCache.registryRef"

	registryAccessKey    = "accessKey"
	registryAccessSecret = "accessSecret"
)

// HarborRegistryReconciler makes a Harbor registry endpoint match its HarborRegistry.
type HarborRegistryReconciler struct {
	client.Client
	// Clients is the shared cache of Harbor clients.
	Clients *harbor.ClientCache
	// Recorder emits events on the resource.
	Recorder events.EventRecorder
	// MaxBackoff caps the retry delay of a failing reconcile; tests lower it. Zero means 5 minutes.
	MaxBackoff time.Duration
	// Wakeups receives the connection when a Harbor call fails as unavailable. Nil sends nothing.
	Wakeups ConnectionWakeups

	// lastWrite remembers, per resource, when every field and the credential were last written and which version of
	// the credentials Secret was sent. Harbor never returns the credential, so this is how a resync or a changed
	// Secret is noticed. It holds no secret.
	lastWrite sync.Map
}

type registryWrite struct {
	at            time.Time
	secretVersion string
}

// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborregistries,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups=harbor.soliddowant.dev,resources=harborprojects,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// SetupWithManager registers the reconciler with its watches: connections, credential Secrets, namespaces, the
// proxy-cache projects that use a registry and the other claimants of the same Harbor registry.
func (r *HarborRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&harborv1alpha1.HarborRegistry{}).
		Watches(&harborv1alpha1.HarborConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			return r.registryRequests(ctx, client.MatchingFields{RegistryConnectionIndex: client.ObjectKeyFromObject(object).String()})
		})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			return r.registryRequests(ctx, client.MatchingFields{RegistryCredentialsSecretIndex: client.ObjectKeyFromObject(object).String()})
		})).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			return r.registryRequests(ctx, client.InNamespace(object.GetName()))
		})).
		Watches(&harborv1alpha1.HarborProject{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []reconcile.Request {
			project := object.(*harborv1alpha1.HarborProject)
			if project.Spec.ProxyCache == nil {
				return nil
			}

			return []reconcile.Request{{NamespacedName: projectRegistryKey(project)}}
		})).
		Watches(&harborv1alpha1.HarborRegistry{}, handler.EnqueueRequestsFromMapFunc(r.otherClaimants)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4, RateLimiter: backoffLimiter(r.MaxBackoff)}).
		Named("harborregistry").
		Complete(r)
}

func (r *HarborRegistryReconciler) otherClaimants(ctx context.Context, object client.Object) []reconcile.Request {
	registry := object.(*harborv1alpha1.HarborRegistry)

	key := registry.Status.HarborKey
	if key == "" {
		return nil
	}

	self := client.ObjectKeyFromObject(object)
	requests := r.registryRequests(ctx, client.MatchingFields{ownership.HarborKeyField: key})

	// Claimants that found the same registry under another name, after a rename in Harbor.
	if identity, ok := ownership.RegistryIdentity(key); ok && registry.Status.RegistryID != 0 {
		requests = append(requests, r.registryRequests(ctx, client.MatchingFields{
			ownership.RegistryIDField: ownership.RegistryIDKey(identity, registry.Status.RegistryID),
		})...)
	}

	others := requests[:0]
	for _, request := range requests {
		if request.NamespacedName != self {
			others = append(others, request)
		}
	}

	return others
}

func (r *HarborRegistryReconciler) registryRequests(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	var registries harborv1alpha1.HarborRegistryList
	if err := r.List(ctx, &registries, options...); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(registries.Items))
	for index := range registries.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&registries.Items[index])})
	}

	return requests
}

// registryName is the registry's name in Harbor.
func registryName(registry *harborv1alpha1.HarborRegistry) string {
	if registry.Spec.Name != "" {
		return registry.Spec.Name
	}

	return registry.Name
}

func registryConnectionKey(registry *harborv1alpha1.HarborRegistry) types.NamespacedName {
	namespace := registry.Spec.ConnectionRef.Namespace
	if namespace == "" {
		namespace = registry.Namespace
	}

	return types.NamespacedName{Namespace: namespace, Name: registry.Spec.ConnectionRef.Name}
}

// projectRegistryKey is the registry a proxy-cache project references.
func projectRegistryKey(project *harborv1alpha1.HarborProject) types.NamespacedName {
	namespace := project.Spec.ProxyCache.RegistryRef.Namespace
	if namespace == "" {
		namespace = project.Namespace
	}

	return types.NamespacedName{Namespace: namespace, Name: project.Spec.ProxyCache.RegistryRef.Name}
}

// registryReconcile carries what one reconcile has resolved.
type registryReconcile struct {
	registry      *harborv1alpha1.HarborRegistry
	connection    *harborv1alpha1.HarborConnection
	harbor        *harbor.Client
	identity      string
	resync        time.Duration
	credential    *harbor.RegistryCredential
	secretVersion string
}

// Reconcile makes the Harbor registry match the resource and records the result in status.
func (r *HarborRegistryReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	registry := &harborv1alpha1.HarborRegistry{}
	if err := r.Get(ctx, request.NamespacedName, registry); err != nil {
		if apierrors.IsNotFound(err) {
			r.lastWrite.Delete(request.NamespacedName)
		}

		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !registry.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, registry)
	}

	if controllerutil.AddFinalizer(registry, RegistryFinalizer) {
		if err := r.Update(ctx, registry); err != nil {
			return reconcile.Result{}, err
		}
	}

	state, result, err := r.resolve(ctx, registry)
	if state == nil {
		return result, err
	}

	return r.sync(ctx, state)
}

// systemAdmitted reports whether the connection's systemAllowedNamespaces admits the registry's namespace.
func (r *HarborRegistryReconciler) systemAdmitted(ctx context.Context, connection *harborv1alpha1.HarborConnection, namespace string) (bool, error) {
	return access.Admits(ctx, r.Client, connection.Spec.SystemAllowedNamespaces, connection.Namespace, namespace)
}

// resolve finds and admits the connection, reads the credentials, gets a Harbor client and checks ownership. It
// returns nil state when the reconcile ends here, with the result to return.
func (r *HarborRegistryReconciler) resolve(ctx context.Context, registry *harborv1alpha1.HarborRegistry) (*registryReconcile, reconcile.Result, error) {
	resync := defaultResyncInterval
	if registry.Spec.ResyncInterval != nil && registry.Spec.ResyncInterval.Duration > 0 {
		resync = registry.Spec.ResyncInterval.Duration
	}

	stop := func(reason, message string) (*registryReconcile, reconcile.Result, error) {
		return nil, reconcile.Result{RequeueAfter: resync}, r.setReady(ctx, registry, metav1.ConditionFalse, reason, message)
	}

	key := registryConnectionKey(registry)
	connection := &harborv1alpha1.HarborConnection{}

	if err := r.Get(ctx, key, connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}

		message := fmt.Sprintf("HarborConnection %s not found", key)
		r.setReferenceResolved(registry, metav1.ConditionFalse, harborv1alpha1.ReasonNotFound, message)

		return stop(harborv1alpha1.ReasonNotFound, message)
	}

	if registry.Spec.ResyncInterval == nil {
		resync = resyncInterval(connection)
	}

	admitted, err := r.systemAdmitted(ctx, connection, registry.Namespace)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if !admitted {
		message := fmt.Sprintf("HarborConnection %s does not admit namespace %q for Harbor-wide objects", key, registry.Namespace)
		r.setReferenceResolved(registry, metav1.ConditionFalse, harborv1alpha1.ReasonNotAllowed, message)

		return stop(harborv1alpha1.ReasonNotAllowed, message)
	}

	r.setReferenceResolved(registry, metav1.ConditionTrue, harborv1alpha1.ReasonReferenceResolved, "HarborConnection "+key.String()+" admits the namespace")

	credential, secretVersion, message := r.readCredential(ctx, registry)
	if message != "" {
		return stop(harborv1alpha1.ReasonCredentialsInvalid, message)
	}

	identity := connection.Status.HarborIdentity
	if ready := meta.FindStatusCondition(connection.Status.Conditions, harborv1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionTrue || identity == "" {
		reason := harborv1alpha1.ReasonHarborUnavailable
		if ready != nil && ready.Status == metav1.ConditionFalse {
			reason = ready.Reason
		}

		return stop(reason, fmt.Sprintf("HarborConnection %s is not ready", key))
	}

	harborClient, reason, message := clientFor(ctx, r.Client, r.Clients, connection)
	if harborClient == nil {
		return stop(reason, message)
	}

	harborKey := ownership.RegistryKey(identity, registryName(registry))
	if registry.Status.HarborKey != harborKey {
		// An ID recorded for another Harbor identity names nothing in this one.
		registry.Status.RegistryID = 0
		registry.Status.HarborKey = harborKey

		if err := r.updateStatus(ctx, registry); err != nil {
			return nil, reconcile.Result{}, err
		}
	}

	owner, err := ownership.RegistryOwner(ctx, r.Client, harborKey, r.admitted)
	if err != nil {
		return nil, reconcile.Result{}, err
	}

	if owner == nil {
		// The cache does not show this resource's claim yet.
		return nil, reconcile.Result{RequeueAfter: claimantCacheDelay}, nil
	}

	if owner.UID != registry.UID {
		return stop(harborv1alpha1.ReasonConflict, fmt.Sprintf("Harbor registry %q is owned by HarborRegistry %s/%s", registryName(registry), owner.Namespace, owner.Name))
	}

	if registry.Status.RegistryID != 0 {
		other, err := r.otherIDOwner(ctx, registry, identity, registry.Status.RegistryID)
		if err != nil {
			return nil, reconcile.Result{}, err
		}

		if other != nil {
			return stop(harborv1alpha1.ReasonConflict, idConflictMessage(registry.Status.RegistryID, other))
		}
	}

	return &registryReconcile{
		registry: registry, connection: connection, harbor: harborClient, identity: identity, resync: resync,
		credential: credential, secretVersion: secretVersion,
	}, reconcile.Result{}, nil
}

// otherIDOwner returns the HarborRegistry other than registry that owns the Harbor registry with the given ID, or nil.
// Harbor can rename a registry, so the owner may record it under a name other than registry's.
func (r *HarborRegistryReconciler) otherIDOwner(ctx context.Context, registry *harborv1alpha1.HarborRegistry, identity string, id int64) (*harborv1alpha1.HarborRegistry, error) {
	owner, err := ownership.RegistryIDOwner(ctx, r.Client, identity, id, r.admitted)
	if err != nil || owner == nil || owner.UID == registry.UID {
		return nil, err
	}

	return owner, nil
}

func idConflictMessage(id int64, owner *harborv1alpha1.HarborRegistry) string {
	return fmt.Sprintf("Harbor registry with ID %d is owned by HarborRegistry %s/%s", id, owner.Namespace, owner.Name)
}

// readCredential reads the credentials Secret the spec names. It returns a nil credential when the spec names none,
// and a message when the Secret is missing or incomplete. The message never contains the Secret's values.
func (r *HarborRegistryReconciler) readCredential(ctx context.Context, registry *harborv1alpha1.HarborRegistry) (*harbor.RegistryCredential, string, string) {
	reference := registry.Spec.CredentialsSecretRef
	if reference == nil {
		return nil, "", ""
	}

	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: registry.Namespace, Name: reference.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", fmt.Sprintf("credentials Secret %q not found", reference.Name)
		}

		return nil, "", fmt.Sprintf("reading credentials Secret %q failed", reference.Name)
	}

	for _, key := range []string{registryAccessKey, registryAccessSecret} {
		if len(secret.Data[key]) == 0 {
			return nil, "", fmt.Sprintf("credentials Secret %q has no key %q", reference.Name, key)
		}
	}

	return &harbor.RegistryCredential{
		AccessKey: string(secret.Data[registryAccessKey]), AccessSecret: string(secret.Data[registryAccessSecret]),
	}, secret.ResourceVersion, ""
}

// admitted reports whether a claimant's connection admits the claimant's namespace for Harbor-wide objects.
func (r *HarborRegistryReconciler) admitted(ctx context.Context, claimant *harborv1alpha1.HarborRegistry) (bool, error) {
	connection := &harborv1alpha1.HarborConnection{}
	if err := r.Get(ctx, registryConnectionKey(claimant), connection); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return r.systemAdmitted(ctx, connection, claimant.Namespace)
}

// desiredRegistry is the registry the spec asks for.
func desiredRegistry(state *registryReconcile) harbor.RegistrySpec {
	spec := &state.registry.Spec

	return harbor.RegistrySpec{
		Name: registryName(state.registry), Type: spec.Type, URL: spec.URL, Description: spec.Description,
		Insecure: spec.Insecure, Credential: state.credential,
	}
}

// sync finds or creates the Harbor registry, then makes it match the spec.
func (r *HarborRegistryReconciler) sync(ctx context.Context, state *registryReconcile) (reconcile.Result, error) {
	registry := state.registry
	name := registryName(registry)
	recreate := false

	if registry.Status.RegistryID != 0 {
		// Harbor can rename a registry, so a different name is drift, not a sign that the ID names another registry.
		found, err := state.harbor.GetRegistry(ctx, registry.Status.RegistryID)

		switch {
		case harbor.IsNotFound(err):
			recreate = true
		case err != nil:
			return r.harborFailed(ctx, state, err)
		default:
			return r.update(ctx, state, found, false)
		}

		registry.Status.RegistryID = 0
		if err := r.updateStatus(ctx, registry); err != nil {
			return reconcile.Result{}, err
		}
	}

	found, err := state.harbor.GetRegistryByName(ctx, name)

	switch {
	case harbor.IsNotFound(err):
		return r.create(ctx, state, recreate)
	case err != nil:
		return r.harborFailed(ctx, state, err)
	}

	// A registry renamed in Harbor is still its owner's, whatever name the owner records.
	other, err := r.otherIDOwner(ctx, registry, state.identity, found.ID)
	if err != nil {
		return reconcile.Result{}, err
	}

	if other != nil {
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, registry, metav1.ConditionFalse, harborv1alpha1.ReasonConflict, idConflictMessage(found.ID, other))
	}

	ownCreate := isOwnCreate(registry.Status.CreatePending, found.CreationTime)

	switch {
	case ownCreate:
		// The registry was created by this resource just before the reconcile was interrupted.
	case !registry.Spec.Adopt:
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, registry, metav1.ConditionFalse, harborv1alpha1.ReasonAlreadyExists,
			fmt.Sprintf("Harbor registry %q already exists; set adopt: true to take it over", name))
	case !state.connection.Spec.AllowAdoption:
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, registry, metav1.ConditionFalse, harborv1alpha1.ReasonAdoptionNotAllowed,
			fmt.Sprintf("HarborConnection %s does not allow adoption", client.ObjectKeyFromObject(state.connection)))
	}

	if err := r.recordID(ctx, registry, found.ID); err != nil {
		return reconcile.Result{}, err
	}

	if !ownCreate {
		r.Recorder.Eventf(registry, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonAdopted, "Adopt", "Adopted the existing Harbor registry %q", name)
	}

	// An adopted registry's credential is unknown, so it is written now.
	return r.update(ctx, state, found, true)
}

// create creates the Harbor registry, marking the resource first so that an interrupted create is recognised as its
// own.
func (r *HarborRegistryReconciler) create(ctx context.Context, state *registryReconcile, recreate bool) (reconcile.Result, error) {
	registry := state.registry
	name := registryName(registry)

	if registry.Status.CreatePending == nil {
		registry.Status.CreatePending = &metav1.Time{Time: time.Now().UTC().Truncate(time.Second)}

		if err := r.updateStatus(ctx, registry); err != nil {
			return reconcile.Result{}, err
		}
	}

	created, err := state.harbor.CreateRegistry(ctx, desiredRegistry(state))
	if err != nil {
		// Harbor refused, so nothing was created: a registry made later by someone else must not pass for this one.
		if harbor.IsRefused(err) {
			registry.Status.CreatePending = nil
		}

		return r.harborFailed(ctx, state, err)
	}

	if err := r.recordID(ctx, registry, created.ID); err != nil {
		return reconcile.Result{}, err
	}

	r.rememberWrite(registry, state.secretVersion)

	if recreate {
		r.Recorder.Eventf(registry, nil, corev1.EventTypeNormal, harborv1alpha1.EventReasonRecreated, "Create", "Created Harbor registry %q again after it was deleted in Harbor", name)
	} else {
		recordCreated(r.Recorder, registry, "registry", name)
	}

	return r.checkHealth(ctx, state, created.ID)
}

// recordID writes the registry ID to status and clears the create marker.
func (r *HarborRegistryReconciler) recordID(ctx context.Context, registry *harborv1alpha1.HarborRegistry, id int64) error {
	registry.Status.RegistryID = id
	registry.Status.CreatePending = nil

	return r.updateStatus(ctx, registry)
}

func (r *HarborRegistryReconciler) rememberWrite(registry *harborv1alpha1.HarborRegistry, secretVersion string) {
	r.lastWrite.Store(client.ObjectKeyFromObject(registry), registryWrite{at: time.Now(), secretVersion: secretVersion})
}

// writeDue reports whether every field and the credential should be written although the readable fields match: a
// resync is due, or the credentials Secret changed since the last write.
func (r *HarborRegistryReconciler) writeDue(state *registryReconcile) bool {
	value, ok := r.lastWrite.Load(client.ObjectKeyFromObject(state.registry))
	if !ok {
		return true
	}

	last := value.(registryWrite)

	return time.Since(last.at) >= state.resync || last.secretVersion != state.secretVersion
}

// update writes the registry when a readable field differs, or when a resync is due, and then checks its health.
func (r *HarborRegistryReconciler) update(ctx context.Context, state *registryReconcile, current *harbor.Registry, force bool) (reconcile.Result, error) {
	desired := desiredRegistry(state)
	differs := current.Name != desired.Name || current.URL != desired.URL || current.Description != desired.Description || current.Insecure != desired.Insecure
	due := force || r.writeDue(state)

	// Without a credential in the spec, a write that changes no readable field has nothing to put back.
	if differs || (due && desired.Credential != nil) {
		if err := state.harbor.UpdateRegistry(ctx, current.ID, desired); err != nil {
			return r.harborFailed(ctx, state, err)
		}

		if differs {
			recordUpdated(r.Recorder, state.registry, "registry", current.Name,
				fieldIf(current.Name != desired.Name, "name"), fieldIf(current.URL != desired.URL, "url"),
				fieldIf(current.Description != desired.Description, "description"), fieldIf(current.Insecure != desired.Insecure, "insecure"))
		}
	}

	if !differs && !due && state.registry.Status.Health != "" {
		return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, state.registry, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor registry matches the spec")
	}

	r.rememberWrite(state.registry, state.secretVersion)

	return r.checkHealth(ctx, state, current.ID)
}

// checkHealth pings the upstream through Harbor, records the result in status.health and reports Ready. The health
// never changes Ready, and Harbor's ping message is never copied into events.
func (r *HarborRegistryReconciler) checkHealth(ctx context.Context, state *registryReconcile, id int64) (reconcile.Result, error) {
	registry := state.registry
	health := harborv1alpha1.RegistryHealthHealthy

	if err := state.harbor.PingRegistry(ctx, id); err != nil {
		switch {
		case harbor.IsUpstreamFailure(err):
			health = harborv1alpha1.RegistryHealthUnhealthy
		case isAuthFailure(err):
			return r.harborFailed(ctx, state, err)
		default:
			health = harborv1alpha1.RegistryHealthUnknown
		}
	}

	if registry.Status.Health != health {
		eventType := corev1.EventTypeNormal
		if health == harborv1alpha1.RegistryHealthUnhealthy {
			eventType = corev1.EventTypeWarning
		}

		registry.Status.Health = health
		r.Recorder.Eventf(registry, nil, eventType, harborv1alpha1.EventReasonHealthChanged, "Ping", "Upstream registry health is %s", health)
	}

	return reconcile.Result{RequeueAfter: state.resync}, r.setReady(ctx, registry, metav1.ConditionTrue, harborv1alpha1.ReasonReady, "Harbor registry matches the spec")
}

// isAuthFailure reports whether Harbor refused the operator's own credentials or permissions.
func isAuthFailure(err error) bool {
	reason, _ := harbor.MapError(err)

	return reason == harborv1alpha1.ReasonUnauthorized || reason == harborv1alpha1.ReasonForbidden
}

// harborFailed reports a Harbor error. A spec Harbor rejects waits for a spec change; anything else retries with
// backoff.
func (r *HarborRegistryReconciler) harborFailed(ctx context.Context, state *registryReconcile, err error) (reconcile.Result, error) {
	reason, retry := harbor.MapError(err)
	if state.connection != nil {
		r.Wakeups.wakeIfUnavailable(client.ObjectKeyFromObject(state.connection), reason)
	}

	if statusErr := r.setReady(ctx, state.registry, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	if !retry {
		return reconcile.Result{}, nil
	}

	return reconcile.Result{}, err
}

func (r *HarborRegistryReconciler) setReferenceResolved(registry *harborv1alpha1.HarborRegistry, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReferenceResolved, Status: status, Reason: reason, Message: message, ObservedGeneration: registry.Generation,
	})
}

// setReady records the Ready condition and emits an event when it changes.
func (r *HarborRegistryReconciler) setReady(ctx context.Context, registry *harborv1alpha1.HarborRegistry, status metav1.ConditionStatus, reason, message string) error {
	previous := meta.FindStatusCondition(registry.Status.Conditions, harborv1alpha1.ConditionReady)
	changed := previous == nil || previous.Status != status || previous.Reason != reason || previous.Message != message

	registry.Status.ObservedGeneration = registry.Generation
	meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
		Type: harborv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: registry.Generation,
	})

	if err := r.updateStatus(ctx, registry); err != nil {
		return err
	}

	if changed {
		eventType := corev1.EventTypeNormal
		if status != metav1.ConditionTrue {
			eventType = corev1.EventTypeWarning
		}

		r.Recorder.Eventf(registry, nil, eventType, reason, "Reconcile", "%s", message)
	}

	return nil
}

// updateStatus writes status only when it differs from what the API server has.
func (r *HarborRegistryReconciler) updateStatus(ctx context.Context, registry *harborv1alpha1.HarborRegistry) error {
	stored := &harborv1alpha1.HarborRegistry{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(registry), stored); err == nil &&
		stored.ResourceVersion == registry.ResourceVersion && apiequality.Semantic.DeepEqual(stored.Status, registry.Status) {
		return nil
	}

	return r.Status().Update(ctx, registry)
}
