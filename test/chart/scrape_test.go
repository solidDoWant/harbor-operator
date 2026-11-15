package chart_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestScrapeObjectsAndMetricsReaderAreOptIn(t *testing.T) {
	t.Parallel()

	list, err := renderAll(t)
	require.NoError(t, err)

	for _, doc := range list {
		assert.NotContains(t, string(doc), "metrics-reader")
		assert.NotContains(t, string(doc), `"kind":"VMServiceScrape"`)
		assert.NotContains(t, string(doc), `"kind":"ServiceMonitor"`)
	}
}

func TestMetricsReaderBindsListedServiceAccountsToGetMetrics(t *testing.T) {
	t.Parallel()

	list, err := renderAll(t,
		"metrics.reader.enabled=true",
		"metrics.reader.subjects[0].namespace=monitoring", "metrics.reader.subjects[0].name=scraper",
		"metrics.reader.subjects[1].namespace=other", "metrics.reader.subjects[1].name=agent")
	require.NoError(t, err)

	var (
		role    rbacv1.ClusterRole
		binding rbacv1.ClusterRoleBinding
	)

	for _, doc := range list {
		var meta struct {
			Kind string `json:"kind"`
		}

		require.NoError(t, yaml.Unmarshal(doc, &meta))

		switch meta.Kind {
		case "ClusterRole":
			var candidate rbacv1.ClusterRole
			require.NoError(t, yaml.Unmarshal(doc, &candidate))

			if len(candidate.Rules) == 1 && len(candidate.Rules[0].NonResourceURLs) > 0 {
				role = candidate
			}
		case "ClusterRoleBinding":
			var candidate rbacv1.ClusterRoleBinding
			require.NoError(t, yaml.Unmarshal(doc, &candidate))

			if strings.HasSuffix(candidate.Name, "-metrics-reader") {
				binding = candidate
			}
		}
	}

	require.Len(t, role.Rules, 1)
	assert.Equal(t, []string{"/metrics"}, role.Rules[0].NonResourceURLs)
	assert.Equal(t, []string{"get"}, role.Rules[0].Verbs)
	assert.Equal(t, role.Name, binding.RoleRef.Name)
	assert.Equal(t, "ClusterRole", binding.RoleRef.Kind)
	assert.Equal(t, []rbacv1.Subject{
		{Kind: "ServiceAccount", Namespace: "monitoring", Name: "scraper"},
		{Kind: "ServiceAccount", Namespace: "other", Name: "agent"},
	}, binding.Subjects)
}

func TestScrapeObjectsTargetTheMetricsServiceOverHTTPS(t *testing.T) {
	t.Parallel()

	for _, kind := range []struct{ kind, apiVersion, set string }{
		{"VMServiceScrape", "operator.victoriametrics.com/v1beta1", "metrics.vmServiceScrape"},
		{"ServiceMonitor", "monitoring.coreos.com/v1", "metrics.serviceMonitor"},
	} {
		t.Run(kind.kind, func(t *testing.T) {
			t.Parallel()

			docs, err := render(t, kind.set+".enabled=true")
			require.NoError(t, err)

			var object struct {
				APIVersion string `json:"apiVersion"`
				Spec       struct {
					Endpoints []struct {
						Port            string `json:"port"`
						Scheme          string `json:"scheme"`
						BearerTokenFile string `json:"bearerTokenFile"`
						TLSConfig       struct {
							InsecureSkipVerify bool   `json:"insecureSkipVerify"`
							CAFile             string `json:"caFile"`
						} `json:"tlsConfig"`
					} `json:"endpoints"`
				} `json:"spec"`
			}

			require.NoError(t, yaml.Unmarshal(docs[kind.kind], &object))
			assert.Equal(t, kind.apiVersion, object.APIVersion)
			require.Len(t, object.Spec.Endpoints, 1)

			endpoint := object.Spec.Endpoints[0]
			assert.Equal(t, "metrics", endpoint.Port)
			assert.Equal(t, "https", endpoint.Scheme)
			assert.Equal(t, "/var/run/secrets/kubernetes.io/serviceaccount/token", endpoint.BearerTokenFile)
			assert.True(t, endpoint.TLSConfig.InsecureSkipVerify)
			assert.Empty(t, endpoint.TLSConfig.CAFile)

			docs, err = render(t, kind.set+".enabled=true", kind.set+".caFile=/ca/ca.crt")
			require.NoError(t, err)

			object.Spec.Endpoints = nil
			require.NoError(t, yaml.Unmarshal(docs[kind.kind], &object))
			assert.False(t, object.Spec.Endpoints[0].TLSConfig.InsecureSkipVerify)
			assert.Equal(t, "/ca/ca.crt", object.Spec.Endpoints[0].TLSConfig.CAFile)
		})
	}
}

func TestScrapeOptionsNeedSecureMetrics(t *testing.T) {
	t.Parallel()

	for _, option := range []string{"metrics.reader.enabled", "metrics.vmServiceScrape.enabled", "metrics.serviceMonitor.enabled"} {
		_, err := render(t, option+"=true", "metrics.secure=false")
		require.ErrorContains(t, err, "metrics.secure", option)
	}
}
