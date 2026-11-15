package harbor_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

func TestNormalizeURL(t *testing.T) {
	for input, expected := range map[string]string{
		"https://Harbor.Example.com/":     "https://harbor.example.com",
		"https://harbor.example.com:443":  "https://harbor.example.com",
		"http://harbor.example.com:80/":   "http://harbor.example.com",
		"https://harbor.example.com:8443": "https://harbor.example.com:8443",
		"http://harbor.example.com:443":   "http://harbor.example.com:443",
		"https://harbor.example.com":      "https://harbor.example.com",
		"ftp://harbor.example.com":        "",
		"not a url":                       "",
		"":                                "",
	} {
		assert.Equal(t, expected, harbor.NormalizeURL(input), input)
	}
}

func TestHostOf(t *testing.T) {
	assert.Equal(t, "harbor.example.com:8443", harbor.HostOf("https://harbor.example.com:8443"))
	assert.Equal(t, "harbor.example.com", harbor.HostOf("https://harbor.example.com"))
}
