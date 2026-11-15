package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestProjectRetryBackoffIsCappedAtTenMinutes(t *testing.T) {
	limiter := (&HarborProjectReconciler{}).rateLimiter()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "project"}}

	var delays []time.Duration
	for range 40 {
		delays = append(delays, limiter.When(request))
	}

	assert.Less(t, delays[0], time.Second, "the first retry comes quickly")
	assert.Less(t, delays[16], 10*time.Minute, "the delay still grows after 16 failures")

	for index, delay := range delays {
		assert.LessOrEqual(t, delay, 10*time.Minute, "retry %d", index)
	}

	assert.Equal(t, 10*time.Minute, delays[len(delays)-1], "a project that keeps failing is retried every 10 minutes")
}
