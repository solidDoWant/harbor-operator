package access_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/access"
	"github.com/solidDoWant/harbor-operator/internal/scheme"
)

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestAdmits(t *testing.T) {
	runtimeScheme, err := scheme.New()
	require.NoError(t, err)

	reader := fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(
		namespace("owner", nil),
		namespace("listed", nil),
		namespace("ci", map[string]string{"team": "ci", "tier": "build"}),
		namespace("prod", map[string]string{"team": "prod"}),
		namespace("unlabelled", nil),
	).Build()

	tests := map[string]struct {
		allowed     v1alpha1.AllowedNamespaces
		candidate   string
		reader      client.Reader
		want        bool
		assertError require.ErrorAssertionFunc
	}{
		"own namespace with nothing allowed": {candidate: "owner", want: true},
		"own namespace is admitted without reading it": {
			candidate: "owner",
			reader:    failingReader{},
			want:      true,
		},
		"other namespace with nothing allowed": {candidate: "ci", want: false},
		"listed name": {
			allowed:   v1alpha1.AllowedNamespaces{Names: []string{"other", "listed"}},
			candidate: "listed",
			want:      true,
		},
		"listed name is admitted without reading it": {
			allowed:   v1alpha1.AllowedNamespaces{Names: []string{"listed"}},
			candidate: "listed",
			reader:    failingReader{},
			want:      true,
		},
		"name not in the list and nil selector": {
			allowed:   v1alpha1.AllowedNamespaces{Names: []string{"listed"}},
			candidate: "ci",
			want:      false,
		},
		"nil selector admits nothing more": {
			allowed:   v1alpha1.AllowedNamespaces{Selector: nil},
			candidate: "unlabelled",
			want:      false,
		},
		"empty selector matches every namespace": {
			allowed:   v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{}},
			candidate: "unlabelled",
			want:      true,
		},
		"matchLabels matches": {
			allowed:   v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "ci"}}},
			candidate: "ci",
			want:      true,
		},
		"matchLabels does not match": {
			allowed:   v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "ci"}}},
			candidate: "prod",
			want:      false,
		},
		"matchLabels needs every label": {
			allowed: v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"team": "ci", "tier": "deploy"},
			}},
			candidate: "ci",
			want:      false,
		},
		"matchExpressions In matches": {
			allowed: v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "team", Operator: metav1.LabelSelectorOpIn, Values: []string{"ci", "qa"}},
			}}},
			candidate: "ci",
			want:      true,
		},
		"matchExpressions NotIn excludes": {
			allowed: v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "team", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"prod"}},
			}}},
			candidate: "prod",
			want:      false,
		},
		"matchExpressions Exists on an unlabelled namespace": {
			allowed: v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "team", Operator: metav1.LabelSelectorOpExists},
			}}},
			candidate: "unlabelled",
			want:      false,
		},
		"matchExpressions DoesNotExist on an unlabelled namespace": {
			allowed: v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "team", Operator: metav1.LabelSelectorOpDoesNotExist},
			}}},
			candidate: "unlabelled",
			want:      true,
		},
		"selector and a missing namespace": {
			allowed:   v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{}},
			candidate: "gone",
			want:      false,
		},
		"invalid selector": {
			allowed: v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "team", Operator: "Sometimes"},
			}}},
			candidate:   "ci",
			want:        false,
			assertError: require.Error,
		},
		"read error is returned": {
			allowed:     v1alpha1.AllowedNamespaces{Selector: &metav1.LabelSelector{}},
			candidate:   "ci",
			reader:      failingReader{},
			want:        false,
			assertError: require.Error,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if test.assertError == nil {
				test.assertError = require.NoError
			}

			testReader := test.reader
			if testReader == nil {
				testReader = reader
			}

			admitted, err := access.Admits(t.Context(), testReader, test.allowed, "owner", test.candidate)
			test.assertError(t, err)
			assert.Equal(t, test.want, admitted)
		})
	}
}

// failingReader fails every read, so a test using it proves Admits decided without reading the API.
type failingReader struct{}

func (failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("read not expected")
}

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("read not expected")
}
