//go:build integration

package controller_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// laggingRobotCache is a client that records every robot status the controller writes and can serve one recorded
// status back on a later read. It reproduces a cache that has not yet seen the controller's last writes when the next
// reconcile starts.
type laggingRobotCache struct {
	client.Client

	mutex   sync.Mutex
	written []*harborv1alpha1.HarborProjectRobot
	stale   *harborv1alpha1.HarborProjectRobot
	// reconcile is the reconcile whose reads were seen last; served counts the reconciles started since the stale
	// status was served, the one it was served to included.
	reconcile types.UID
	served    int
}

func (lagging *laggingRobotCache) Status() client.SubResourceWriter {
	return &laggingRobotStatus{SubResourceWriter: lagging.Client.Status(), lagging: lagging}
}

func (lagging *laggingRobotCache) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	robot, ok := object.(*harborv1alpha1.HarborProjectRobot)

	lagging.mutex.Lock()
	defer lagging.mutex.Unlock()

	// Only the read that starts a reconcile gets the stale status, as a lagging cache would serve it.
	reconcile := controller.ReconcileIDFromContext(ctx)
	if !ok || reconcile == "" || reconcile == lagging.reconcile {
		return lagging.Client.Get(ctx, key, object, options...)
	}

	lagging.reconcile = reconcile

	switch {
	case lagging.stale != nil && client.ObjectKeyFromObject(lagging.stale) == key:
		lagging.stale.DeepCopyInto(robot)
		lagging.stale = nil
		lagging.served = 1

		return nil
	case lagging.served > 0:
		lagging.served++
	}

	return lagging.Client.Get(ctx, key, object, options...)
}

// lagTo makes the next read of the robot return the last written status for which match holds, as stored then.
func (lagging *laggingRobotCache) lagTo(t *testing.T, match func(*harborv1alpha1.HarborProjectRobot) bool) {
	t.Helper()

	lagging.mutex.Lock()
	defer lagging.mutex.Unlock()

	for index := len(lagging.written) - 1; index >= 0; index-- {
		if match(lagging.written[index]) {
			lagging.stale = lagging.written[index].DeepCopy()

			return
		}
	}

	require.Fail(t, "no recorded robot status matches")
}

// reconcilesSinceStale is how many reconciles started since the stale status was served, that one included.
func (lagging *laggingRobotCache) reconcilesSinceStale() int {
	lagging.mutex.Lock()
	defer lagging.mutex.Unlock()

	return lagging.served
}

type laggingRobotStatus struct {
	client.SubResourceWriter

	lagging *laggingRobotCache
}

func (status *laggingRobotStatus) Update(ctx context.Context, object client.Object, options ...client.SubResourceUpdateOption) error {
	if err := status.SubResourceWriter.Update(ctx, object, options...); err != nil {
		return err
	}

	if robot, ok := object.(*harborv1alpha1.HarborProjectRobot); ok {
		status.lagging.mutex.Lock()
		status.lagging.written = append(status.lagging.written, robot.DeepCopy())
		status.lagging.mutex.Unlock()
	}

	return nil
}

func TestRobotReplacedWithASystemRobotConnectionIsNotReplacedAgainFromAStaleCache(t *testing.T) {
	t.Parallel()

	lagging := &laggingRobotCache{}
	setup := readyRobotProjectWith(t, operatorOptions{wrapRobotClient: func(cached client.Client) client.Client {
		lagging.Client = cached

		return lagging
	}}, true, nil, nil)

	robot := ciRobot(setup.project.Namespace, setup.project)
	robot.Spec.Secret.Rotate = "first"
	require.NoError(t, setup.cl.Create(t.Context(), robot))
	key := client.ObjectKeyFromObject(robot)
	ready := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)

	_, old := dockerCredentials(t, setup.cl, setup.project.Namespace, "ci-dockerconfig", setup.instance.Registry(t))
	updateRobot(t, setup.cl, key, func(robot *harborv1alpha1.HarborProjectRobot) { robot.Spec.Secret.Rotate = "second" })

	username, password := awaitNewDockerPassword(t, setup, "ci-dockerconfig", old)

	replaced := &harborv1alpha1.HarborProjectRobot{}

	require.Eventually(t, func() bool {
		return setup.cl.Get(t.Context(), key, replaced) == nil && replaced.Status.RobotID != ready.Status.RobotID &&
			replaced.Status.CreatePending == nil
	}, projectWait, 200*time.Millisecond, "waiting for the replace to finish")

	// The next reconcile reads the robot as it was just after the replacement was recorded, before the create marker
	// was cleared: it sees an unfinished create.
	lagging.lagTo(t, func(written *harborv1alpha1.HarborProjectRobot) bool {
		return written.Status.RobotID == replaced.Status.RobotID && written.Status.CreatePending != nil
	})

	// Reconciles of one robot never overlap, so once the next one starts the stale one has finished.
	require.Eventually(t, func() bool { return lagging.reconcilesSinceStale() >= 2 }, projectWait, 100*time.Millisecond,
		"waiting for the reconcile after the stale read")

	_, err := setup.admin.GetRobot(t.Context(), replaced.Status.RobotID)
	require.NoError(t, err, "the robot whose secret the Secret holds still exists")

	current := awaitRobot(t, setup.cl, key, metav1.ConditionTrue, harborv1alpha1.ReasonReady)
	assert.Equal(t, replaced.Status.RobotID, current.Status.RobotID, "the robot is not replaced again")

	_, now := dockerCredentials(t, setup.cl, setup.project.Namespace, "ci-dockerconfig", setup.instance.Registry(t))
	assert.Equal(t, password, now)
	require.NoError(t, push(t, setup.instance, username, now, setup.project.Spec.Name, "app"), "the Secret's credentials work")
}
