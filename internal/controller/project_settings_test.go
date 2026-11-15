package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

func ptr[T any](value T) *T { return &value }

func TestProjectSettingsDiffWritesOnlySetFieldsThatDiffer(t *testing.T) {
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	harborState := harbor.ProjectSettings{
		Public:                  ptr(true),
		PreventVulnerableImages: ptr(true),
		Severity:                ptr("critical"),
		ScanOnPush:              ptr(true),
		UseSystemAllowlist:      ptr(true),
		CVEAllowlist:            &harbor.CVEAllowlist{Items: []string{"CVE-2024-0002", "CVE-2024-0001"}, ExpiresAt: &expires},
	}

	for name, test := range map[string]struct {
		spec    harborv1alpha1.HarborProjectSpec
		want    harbor.ProjectSettings
		changed bool
	}{
		"empty spec writes nothing": {},
		"empty vulnerability writes nothing": {
			spec: harborv1alpha1.HarborProjectSpec{Vulnerability: &harborv1alpha1.VulnerabilitySpec{}},
		},
		"matching fields write nothing": {
			spec: harborv1alpha1.HarborProjectSpec{Public: ptr(true), Vulnerability: &harborv1alpha1.VulnerabilitySpec{
				PreventVulnerableImages: ptr(true), Severity: "critical", ScanOnPush: ptr(true), UseSystemAllowlist: ptr(true),
			}},
		},
		"public differs": {
			spec:    harborv1alpha1.HarborProjectSpec{Public: ptr(false), Vulnerability: &harborv1alpha1.VulnerabilitySpec{Severity: "critical"}},
			want:    harbor.ProjectSettings{Public: ptr(false)},
			changed: true,
		},
		"vulnerability fields differ": {
			spec: harborv1alpha1.HarborProjectSpec{Vulnerability: &harborv1alpha1.VulnerabilitySpec{
				PreventVulnerableImages: ptr(false), Severity: "low", ScanOnPush: ptr(false), UseSystemAllowlist: ptr(false),
			}},
			want: harbor.ProjectSettings{
				PreventVulnerableImages: ptr(false), Severity: ptr("low"), ScanOnPush: ptr(false), UseSystemAllowlist: ptr(false),
			},
			changed: true,
		},
		"allowlist in another order matches": {
			spec: harborv1alpha1.HarborProjectSpec{Vulnerability: &harborv1alpha1.VulnerabilitySpec{CVEAllowlist: &harborv1alpha1.CVEAllowlist{
				Items: []string{"CVE-2024-0001", "CVE-2024-0002"}, ExpiresAt: &metav1.Time{Time: expires},
			}}},
			// The project's own allowlist turns off reuse of the system one, which Harbor has on.
			want:    harbor.ProjectSettings{UseSystemAllowlist: ptr(false)},
			changed: true,
		},
		"allowlist items differ": {
			spec: harborv1alpha1.HarborProjectSpec{Vulnerability: &harborv1alpha1.VulnerabilitySpec{CVEAllowlist: &harborv1alpha1.CVEAllowlist{
				Items: []string{"CVE-2024-0001"}, ExpiresAt: &metav1.Time{Time: expires},
			}}},
			want: harbor.ProjectSettings{
				UseSystemAllowlist: ptr(false),
				CVEAllowlist:       &harbor.CVEAllowlist{Items: []string{"CVE-2024-0001"}, ExpiresAt: &expires},
			},
			changed: true,
		},
		"allowlist expiry removed": {
			spec: harborv1alpha1.HarborProjectSpec{Vulnerability: &harborv1alpha1.VulnerabilitySpec{CVEAllowlist: &harborv1alpha1.CVEAllowlist{
				Items: []string{"CVE-2024-0001", "CVE-2024-0002"},
			}}},
			want: harbor.ProjectSettings{
				UseSystemAllowlist: ptr(false),
				CVEAllowlist:       &harbor.CVEAllowlist{Items: []string{"CVE-2024-0001", "CVE-2024-0002"}},
			},
			changed: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, changed := projectSettingsDiff(desiredProjectSettings(&test.spec), harborState)
			assert.Equal(t, test.want, got)
			assert.Equal(t, test.changed, changed)
		})
	}
}

func TestDesiredStorageQuota(t *testing.T) {
	for name, test := range map[string]struct {
		spec  harborv1alpha1.HarborProjectSpec
		bytes int64
		set   bool
	}{
		"unset leaves the quota alone": {},
		"quantity in bytes":            {spec: harborv1alpha1.HarborProjectSpec{StorageQuota: ptr(resource.MustParse("5Gi"))}, bytes: 5 << 30, set: true},
		"unlimited is -1":              {spec: harborv1alpha1.HarborProjectSpec{UnlimitedStorage: true}, bytes: -1, set: true},
	} {
		t.Run(name, func(t *testing.T) {
			bytes, set := desiredStorageQuota(&test.spec)
			assert.Equal(t, test.bytes, bytes)
			assert.Equal(t, test.set, set)
		})
	}
}
