package controller

import (
	"slices"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// desiredProjectSettings maps the fields a project spec sets to Harbor settings. A field the spec leaves unset stays
// nil, so it is never written.
func desiredProjectSettings(spec *harborv1alpha1.HarborProjectSpec) harbor.ProjectSettings {
	settings := harbor.ProjectSettings{Public: spec.Public}

	if spec.ProxyCache != nil && spec.ProxyCache.SpeedLimitKbps != nil {
		speed := *spec.ProxyCache.SpeedLimitKbps
		settings.ProxySpeedKbps = &speed
	}

	vulnerability := spec.Vulnerability
	if vulnerability == nil {
		return settings
	}

	settings.PreventVulnerableImages = vulnerability.PreventVulnerableImages
	settings.ScanOnPush = vulnerability.ScanOnPush
	settings.UseSystemAllowlist = vulnerability.UseSystemAllowlist

	if vulnerability.Severity != "" {
		severity := vulnerability.Severity
		settings.Severity = &severity
	}

	if allowlist := vulnerability.CVEAllowlist; allowlist != nil {
		// The project's own allowlist only applies when the system one is not reused.
		reuse := false
		settings.UseSystemAllowlist = &reuse
		settings.CVEAllowlist = &harbor.CVEAllowlist{Items: slices.Clone(allowlist.Items)}

		if allowlist.ExpiresAt != nil {
			expires := allowlist.ExpiresAt.UTC()
			settings.CVEAllowlist.ExpiresAt = &expires
		}
	}

	return settings
}

// projectSettingsDiff returns the desired settings that differ from Harbor's current ones, and whether there are any.
// Fields that already match, and fields not desired, are nil.
func projectSettingsDiff(desired, current harbor.ProjectSettings) (harbor.ProjectSettings, bool) {
	var diff harbor.ProjectSettings

	changed := false
	pick := func(want, have *bool) *bool {
		if want == nil || (have != nil && *have == *want) {
			return nil
		}

		changed = true

		return want
	}

	diff.Public = pick(desired.Public, current.Public)
	diff.PreventVulnerableImages = pick(desired.PreventVulnerableImages, current.PreventVulnerableImages)
	diff.ScanOnPush = pick(desired.ScanOnPush, current.ScanOnPush)
	diff.UseSystemAllowlist = pick(desired.UseSystemAllowlist, current.UseSystemAllowlist)

	if desired.Severity != nil && (current.Severity == nil || *current.Severity != *desired.Severity) {
		diff.Severity, changed = desired.Severity, true
	}

	if desired.ProxySpeedKbps != nil && (current.ProxySpeedKbps == nil || *current.ProxySpeedKbps != *desired.ProxySpeedKbps) {
		diff.ProxySpeedKbps, changed = desired.ProxySpeedKbps, true
	}

	if desired.CVEAllowlist != nil && !sameAllowlist(desired.CVEAllowlist, current.CVEAllowlist) {
		diff.CVEAllowlist, changed = desired.CVEAllowlist, true
	}

	return diff, changed
}

// sameAllowlist compares the CVE IDs as a set and the expiry to the second, which is what Harbor stores.
func sameAllowlist(desired, current *harbor.CVEAllowlist) bool {
	if current == nil {
		return false
	}

	want, have := slices.Clone(desired.Items), slices.Clone(current.Items)
	slices.Sort(want)
	slices.Sort(have)

	if !slices.Equal(slices.Compact(want), slices.Compact(have)) {
		return false
	}

	switch {
	case desired.ExpiresAt == nil || current.ExpiresAt == nil:
		return desired.ExpiresAt == nil && current.ExpiresAt == nil
	default:
		return desired.ExpiresAt.Unix() == current.ExpiresAt.Unix()
	}
}

// desiredStorageQuota returns the storage limit in bytes the spec sets, or false when it leaves the quota alone.
func desiredStorageQuota(spec *harborv1alpha1.HarborProjectSpec) (int64, bool) {
	switch {
	case spec.UnlimitedStorage:
		return harbor.UnlimitedQuota, true
	case spec.StorageQuota != nil:
		return spec.StorageQuota.Value(), true
	default:
		return 0, false
	}
}
