package chart_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metricsql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// exportedMetrics lists the metrics a dashboard query may use: the operator's own, the controller-runtime
// ones the panels name, and the Go process metrics.
var exportedMetrics = map[string]bool{
	"harbor_operator_resources":                 true,
	"harbor_operator_harbor_requests_seconds":   true,
	"harbor_operator_drift_corrections_total":   true,
	"harbor_operator_registry_health":           true,
	"controller_runtime_reconcile_total":        true,
	"controller_runtime_reconcile_errors_total": true,
	"controller_runtime_reconcile_time_seconds": true,
	"workqueue_depth":                           true,
	"process_resident_memory_bytes":             true,
	"process_start_time_seconds":                true,
}

type dashboardJSON struct {
	Panels []struct {
		Title   string `json:"title"`
		Targets []struct {
			Expr string `json:"expr"`
		} `json:"targets"`
	} `json:"panels"`
	Templating struct {
		List []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Query any    `json:"query"`
		} `json:"list"`
	} `json:"templating"`
}

var grafanaVariable = regexp.MustCompile(`\$(__rate_interval|__range|__interval)`)

func readDashboard(t *testing.T) (dashboardJSON, []byte) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(chartDir, "dashboards", "harbor-operator.json"))
	require.NoError(t, err)

	var dashboard dashboardJSON
	require.NoError(t, json.Unmarshal(raw, &dashboard))

	return dashboard, raw
}

func TestDashboardQueriesUseOnlyExportedMetrics(t *testing.T) {
	t.Parallel()

	dashboard, _ := readDashboard(t)
	require.NotEmpty(t, dashboard.Panels)

	queries := 0

	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			queries++

			// Grafana substitutes its variables before the query runs; use a duration the parser accepts.
			expr := grafanaVariable.ReplaceAllString(target.Expr, "5m")
			parsed, err := metricsql.Parse(strings.ReplaceAll(expr, `"$namespace"`, `"ns"`))
			require.NoError(t, err, "panel %q: %s", panel.Title, target.Expr)

			metricsql.VisitAll(parsed, func(node metricsql.Expr) {
				selector, ok := node.(*metricsql.MetricExpr)
				if !ok {
					return
				}

				name := ""

				for _, filters := range selector.LabelFilterss {
					for _, filter := range filters {
						if filter.Label == "__name__" {
							name = filter.Value
						}
					}
				}

				base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")
				assert.True(t, exportedMetrics[name] || exportedMetrics[base], "panel %q uses %q, which the operator does not export", panel.Title, name)
			})
		}
	}

	assert.GreaterOrEqual(t, queries, 10)
}

func TestDashboardHasDatasourceAndNamespaceVariables(t *testing.T) {
	t.Parallel()

	dashboard, _ := readDashboard(t)

	types := map[string]string{}
	for _, variable := range dashboard.Templating.List {
		types[variable.Name] = variable.Type
	}

	assert.Equal(t, "datasource", types["datasource"])
	assert.Equal(t, "query", types["namespace"])
}

func TestDashboardOptionsAreOffByDefault(t *testing.T) {
	t.Parallel()

	docs, err := render(t)
	require.NoError(t, err)
	assert.NotContains(t, docs, "ConfigMap")
	assert.NotContains(t, docs, "GrafanaDashboard")
}

func TestDashboardConfigMapCarriesTheJSONAndSidecarLabel(t *testing.T) {
	t.Parallel()

	docs, err := render(t, "grafanaDashboard.configMap.enabled=true", "grafanaDashboard.configMap.labels.team=ops")
	require.NoError(t, err)

	var configMap corev1.ConfigMap
	require.NoError(t, yaml.Unmarshal(docs["ConfigMap"], &configMap))
	assert.Equal(t, "1", configMap.Labels["grafana_dashboard"])
	assert.Equal(t, "ops", configMap.Labels["team"])

	_, raw := readDashboard(t)
	assert.JSONEq(t, string(raw), configMap.Data["harbor-operator.json"])
	assert.NotContains(t, docs, "GrafanaDashboard")
}

func TestGrafanaOperatorDashboardCarriesTheJSONAndInstanceSelector(t *testing.T) {
	t.Parallel()

	docs, err := render(t, "grafanaDashboard.grafanaOperator.enabled=true", "grafanaDashboard.grafanaOperator.instanceSelector.matchLabels.dashboards=mine")
	require.NoError(t, err)

	var dashboard struct {
		APIVersion string `json:"apiVersion"`
		Spec       struct {
			InstanceSelector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"instanceSelector"`
			JSON string `json:"json"`
		} `json:"spec"`
	}

	require.NoError(t, yaml.Unmarshal(docs["GrafanaDashboard"], &dashboard))
	assert.Equal(t, "grafana.integreatly.org/v1beta1", dashboard.APIVersion)
	assert.Equal(t, "mine", dashboard.Spec.InstanceSelector.MatchLabels["dashboards"])

	_, raw := readDashboard(t)
	assert.JSONEq(t, string(raw), dashboard.Spec.JSON)
	assert.NotContains(t, docs, "ConfigMap")
}
