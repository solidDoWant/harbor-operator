package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestProjectRetryBackoffIsCappedAtTenMinutes(t *testing.T) {
	limiter := backoffLimiter((&HarborProjectReconciler{}).MaxBackoff)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "project"}}

	var delays []time.Duration
	for range 40 {
		delays = append(delays, limiter.When(request))
	}

	assert.Equal(t, 5*time.Second, delays[0], "the first retry waits 5 seconds, so a Harbor that is down is not asked in a tight loop")
	assert.Equal(t, 10*time.Second, delays[1], "the delay doubles")

	for index, delay := range delays {
		assert.LessOrEqual(t, delay, 10*time.Minute, "retry %d", index)
	}

	assert.Equal(t, 5*time.Minute, delays[len(delays)-1], "a project that keeps failing is retried every 5 minutes")
}
