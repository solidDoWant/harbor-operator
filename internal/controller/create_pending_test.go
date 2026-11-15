package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsOwnCreate(t *testing.T) {
	marker := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, test := range []struct {
		name    string
		pending *metav1.Time
		created time.Time
		want    bool
	}{
		{name: "no marker", created: marker, want: false},
		{name: "created after the marker", pending: &metav1.Time{Time: marker}, created: marker.Add(time.Second), want: true},
		{name: "created exactly two minutes before the marker", pending: &metav1.Time{Time: marker}, created: marker.Add(-2 * time.Minute), want: true},
		{name: "created just over two minutes before the marker", pending: &metav1.Time{Time: marker}, created: marker.Add(-2*time.Minute - time.Second), want: false},
		{name: "creation time unknown", pending: &metav1.Time{Time: marker}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, isOwnCreate(test.pending, test.created))
		})
	}
}
