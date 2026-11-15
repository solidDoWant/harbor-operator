package harbor

import (
	"strconv"
	"time"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
)

// ProjectSettings is the part of a Harbor project the operator manages. A nil
// field is not managed: it is left out of requests, and when read from Harbor
// every field is set.
type ProjectSettings struct {
	Public                  *bool
	PreventVulnerableImages *bool
	// Severity is one of none, low, medium, high, critical.
	Severity           *string
	ScanOnPush         *bool
	UseSystemAllowlist *bool
	// CVEAllowlist replaces the project's own allowlist when set.
	CVEAllowlist *CVEAllowlist
	// ProxySpeedKbps is a proxy-cache project's pull speed limit; -1 is unlimited.
	ProxySpeedKbps *int32
}

// CVEAllowlist is a project's list of CVE IDs allowed despite the severity gate.
type CVEAllowlist struct {
	Items     []string
	ExpiresAt *time.Time
}

// Project is a project as Harbor reports it.
type Project struct {
	ID       int64
	Name     string
	Settings ProjectSettings
	// RegistryID is the upstream registry of a proxy-cache project, 0 for a normal project.
	RegistryID int64
	// CreationTime is when Harbor created the project. Zero when Harbor did not report it.
	CreationTime time.Time
	// RetentionID is the ID of the project's tag retention policy, or 0 when it has none.
	RetentionID int64
}

func boolString(value *bool) *string {
	if value == nil {
		return nil
	}

	text := strconv.FormatBool(*value)

	return &text
}

func stringBool(value *string) *bool {
	if value == nil {
		return nil
	}

	parsed, err := strconv.ParseBool(*value)
	if err != nil {
		return nil
	}

	return &parsed
}

func stringInt32(value *string) *int32 {
	if value == nil {
		return nil
	}

	parsed, err := strconv.ParseInt(*value, 10, 32)
	if err != nil {
		return nil
	}

	result := int32(parsed)

	return &result
}

// metadata builds the Harbor metadata for the managed fields.
func (s ProjectSettings) metadata() *models.ProjectMetadata {
	metadata := &models.ProjectMetadata{
		PreventVul:           boolString(s.PreventVulnerableImages),
		Severity:             s.Severity,
		AutoScan:             boolString(s.ScanOnPush),
		ReuseSysCVEAllowlist: boolString(s.UseSystemAllowlist),
	}
	if s.Public != nil {
		metadata.Public = strconv.FormatBool(*s.Public)
	}

	if s.ProxySpeedKbps != nil {
		speed := strconv.FormatInt(int64(*s.ProxySpeedKbps), 10)
		metadata.ProxySpeedKb = &speed
	}

	return metadata
}

func (a *CVEAllowlist) toModel() *models.CVEAllowlist {
	if a == nil {
		return nil
	}

	model := &models.CVEAllowlist{Items: make([]*models.CVEAllowlistItem, 0, len(a.Items))}
	for _, item := range a.Items {
		model.Items = append(model.Items, &models.CVEAllowlistItem{CVEID: item})
	}

	if a.ExpiresAt != nil {
		expires := a.ExpiresAt.Unix()
		model.ExpiresAt = &expires
	}

	return model
}

func allowlistFromModel(model *models.CVEAllowlist) *CVEAllowlist {
	allowlist := &CVEAllowlist{Items: []string{}}

	if model == nil {
		return allowlist
	}

	for _, item := range model.Items {
		if item != nil {
			allowlist.Items = append(allowlist.Items, item.CVEID)
		}
	}

	if model.ExpiresAt != nil && *model.ExpiresAt != 0 {
		expires := time.Unix(*model.ExpiresAt, 0).UTC()
		allowlist.ExpiresAt = &expires
	}

	return allowlist
}

func projectFromModel(model *models.Project) *Project {
	project := &Project{
		ID: int64(model.ProjectID), Name: model.Name, RegistryID: model.RegistryID, CreationTime: time.Time(model.CreationTime).UTC(),
	}

	metadata := model.Metadata
	if metadata == nil {
		metadata = &models.ProjectMetadata{}
	}

	public := stringBool(&metadata.Public)
	if public == nil {
		public = new(bool)
	}

	if metadata.RetentionID != nil {
		if id, err := strconv.ParseInt(*metadata.RetentionID, 10, 64); err == nil {
			project.RetentionID = id
		}
	}

	project.Settings = ProjectSettings{
		Public:                  public,
		PreventVulnerableImages: stringBool(metadata.PreventVul),
		Severity:                metadata.Severity,
		ScanOnPush:              stringBool(metadata.AutoScan),
		UseSystemAllowlist:      stringBool(metadata.ReuseSysCVEAllowlist),
		CVEAllowlist:            allowlistFromModel(model.CVEAllowlist),
		ProxySpeedKbps:          stringInt32(metadata.ProxySpeedKb),
	}

	return project
}
