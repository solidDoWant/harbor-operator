package controller

import (
	"time"

	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

const (
	// minBackoff is the first retry delay of a failing reconcile. A Harbor that is down is asked again no sooner.
	minBackoff = 5 * time.Second
	// defaultMaxBackoff caps the retry delay of a failing reconcile, so a resource waiting on Harbor (down, or
	// blocking a deletion) is checked again at least this often.
	defaultMaxBackoff = 5 * time.Minute
)

// backoffLimiter retries a failing item with exponential backoff from minBackoff up to maxDelay (defaultMaxBackoff
// when zero), with the same overall rate limit as controller-runtime's default. Every controller uses it, so a Harbor
// that is down gets at most one call per resource per backoff step.
func backoffLimiter(maxDelay time.Duration) workqueue.TypedRateLimiter[reconcile.Request] {
	if maxDelay <= 0 {
		maxDelay = defaultMaxBackoff
	}

	return workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](minBackoff, maxDelay),
		&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
}

// ConnectionWakeups asks the HarborConnection controller to check a connection again now. A resource whose Harbor
// call failed as unavailable sends its connection here, so the connection reports HarborUnavailable promptly and its
// other resources stop calling Harbor until it is ready again. A nil value sends nothing.
type ConnectionWakeups chan event.GenericEvent

// NewConnectionWakeups returns a buffered channel to share between the HarborConnection controller and the
// controllers of the resources that use connections.
func NewConnectionWakeups() ConnectionWakeups {
	return make(ConnectionWakeups, 256)
}

// wakeIfUnavailable sends the connection when reason says Harbor is unavailable. It never blocks: when the buffer is
// full, wakeups are already pending and the connection is checked anyway.
func (w ConnectionWakeups) wakeIfUnavailable(connection types.NamespacedName, reason string) {
	if w == nil || reason != harborv1alpha1.ReasonHarborUnavailable || connection.Name == "" {
		return
	}

	object := &harborv1alpha1.HarborConnection{ObjectMeta: metav1.ObjectMeta{Namespace: connection.Namespace, Name: connection.Name}}

	select {
	case w <- event.GenericEvent{Object: object}:
	default:
	}
}
