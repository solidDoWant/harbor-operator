package harbor

import (
	"context"
	"net/url"
	"strings"

	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/systeminfo"
)

// SystemInfo is what Harbor reports about itself.
type SystemInfo struct {
	// Version is Harbor's version, for example v2.13.0.
	Version string
	// ExternalURL is Harbor's external_url as configured, not normalized.
	ExternalURL string
}

// GetSystemInfo reads Harbor's general information.
func (c *Client) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	response, err := c.api.Systeminfo.GetSystemInfo(ctx, systeminfo.NewGetSystemInfoParams())
	if err != nil {
		return nil, c.fail(err)
	}

	info := &SystemInfo{}

	if response.Payload != nil {
		if response.Payload.HarborVersion != nil {
			info.Version = *response.Payload.HarborVersion
		}

		if response.Payload.ExternalURL != nil {
			info.ExternalURL = *response.Payload.ExternalURL
		}
	}

	return info, nil
}

// NormalizeURL returns the scheme, lower-cased host and port of a URL with the
// default port removed and no path or trailing slash. It returns the empty
// string for input that is not an absolute http or https URL.
func NormalizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}

	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}

	if port != "" {
		host += ":" + port
	}

	return scheme + "://" + host
}

// HostOf returns the host[:port] of a normalized URL.
func HostOf(normalized string) string {
	parsed, err := url.Parse(normalized)
	if err != nil {
		return ""
	}

	return parsed.Host
}
