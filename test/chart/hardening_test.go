// Package chart_test renders the Helm chart with `helm template` and checks what it produces.
package chart_test

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

const chartDir = "../../deploy/charts/harbor-operator"

// render runs helm template and returns the manifest documents by kind. When
// a kind appears more than once, the last document wins; use renderAll then.
func render(t *testing.T, sets ...string) (map[string][]byte, error) {
	t.Helper()

	list, err := renderAll(t, sets...)
	if err != nil {
		return nil, err
	}

	docs := map[string][]byte{}

	for _, doc := range list {
		var meta struct {
			Kind string `json:"kind"`
		}

		require.NoError(t, yaml.Unmarshal(doc, &meta))
		docs[meta.Kind] = doc
	}

	return docs, nil
}

// renderAll runs helm template and returns every manifest document as JSON.
func renderAll(t *testing.T, sets ...string) ([][]byte, error) {
	t.Helper()

	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}

	args := []string{"template", "harbor-operator", chartDir, "--namespace", "harbor-operator"}
	for _, set := range sets {
		args = append(args, "--set", set)
	}

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), helm, args...)

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New(stderr.String())
	}

	var docs [][]byte

	for _, doc := range strings.Split(stdout.String(), "\n---\n") {
		jsonDoc, err := yaml.YAMLToJSON([]byte(doc))
		require.NoError(t, err)

		if string(jsonDoc) == "null" {
			continue
		}

		docs = append(docs, jsonDoc)
	}

	return docs, nil
}

func TestDefaultPodIsHardened(t *testing.T) {
	t.Parallel()

	docs, err := render(t)
	require.NoError(t, err)

	var deployment appsv1.Deployment
	require.NoError(t, yaml.Unmarshal(docs["Deployment"], &deployment))

	podSpec := deployment.Spec.Template.Spec
	require.NotNil(t, podSpec.SecurityContext.RunAsNonRoot)
	assert.True(t, *podSpec.SecurityContext.RunAsNonRoot)
	assert.Equal(t, "RuntimeDefault", string(podSpec.SecurityContext.SeccompProfile.Type))

	container := podSpec.Containers[0]
	require.NotNil(t, container.SecurityContext.ReadOnlyRootFilesystem)
	assert.True(t, *container.SecurityContext.ReadOnlyRootFilesystem)
	require.NotNil(t, container.SecurityContext.AllowPrivilegeEscalation)
	assert.False(t, *container.SecurityContext.AllowPrivilegeEscalation)
	assert.Equal(t, "ALL", string(container.SecurityContext.Capabilities.Drop[0]))

	memory := resource.MustParse("128Mi")
	assert.True(t, memory.Equal(container.Resources.Requests["memory"]))
	assert.True(t, memory.Equal(container.Resources.Limits["memory"]))

	cpu := resource.MustParse("10m")
	assert.True(t, cpu.Equal(container.Resources.Requests["cpu"]))
	_, hasCPULimit := container.Resources.Limits["cpu"]
	assert.False(t, hasCPULimit)
}

func TestNetworkPolicyAndBudgetAreOptIn(t *testing.T) {
	t.Parallel()

	docs, err := render(t)
	require.NoError(t, err)
	assert.NotContains(t, docs, "NetworkPolicy")
	assert.NotContains(t, docs, "PodDisruptionBudget")
}

func TestNetworkPolicyLimitsEgress(t *testing.T) {
	t.Parallel()

	docs, err := render(t, "networkPolicy.enabled=true", "networkPolicy.apiServer.cidrs={10.0.0.1/32}")
	require.NoError(t, err)

	var policy networkingv1.NetworkPolicy
	require.NoError(t, yaml.Unmarshal(docs["NetworkPolicy"], &policy))

	assert.ElementsMatch(t,
		[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, policy.Spec.PolicyTypes)

	require.Len(t, policy.Spec.Ingress, 1)

	var ingressPorts []int32
	for _, port := range policy.Spec.Ingress[0].Ports {
		ingressPorts = append(ingressPorts, port.Port.IntVal)
	}

	assert.ElementsMatch(t, []int32{8443, 8081}, ingressPorts)

	// API server, Harbor and DNS: nothing else.
	require.Len(t, policy.Spec.Egress, 3)
	require.Len(t, policy.Spec.Egress[0].To, 1)
	assert.Equal(t, "10.0.0.1/32", policy.Spec.Egress[0].To[0].IPBlock.CIDR)
	require.Len(t, policy.Spec.Egress[1].Ports, 1)
	assert.Equal(t, int32(443), policy.Spec.Egress[1].Ports[0].Port.IntVal)
	dns := policy.Spec.Egress[2]
	require.Len(t, dns.To, 1)
	assert.Equal(t, "kube-dns", dns.To[0].PodSelector.MatchLabels["k8s-app"])
	assert.Len(t, dns.Ports, 2)
}

func TestNetworkPolicyRestrictsHarborToGivenPeers(t *testing.T) {
	t.Parallel()

	docs, err := render(t, "networkPolicy.enabled=true", "networkPolicy.apiServer.cidrs={10.0.0.1/32}",
		"networkPolicy.harborEgress.peers[0].ipBlock.cidr=192.0.2.0/24")
	require.NoError(t, err)

	var policy networkingv1.NetworkPolicy
	require.NoError(t, yaml.Unmarshal(docs["NetworkPolicy"], &policy))
	require.Len(t, policy.Spec.Egress[1].To, 1)
	assert.Equal(t, "192.0.2.0/24", policy.Spec.Egress[1].To[0].IPBlock.CIDR)
}

func TestNetworkPolicyNeedsAPIServerCIDRs(t *testing.T) {
	t.Parallel()

	_, err := render(t, "networkPolicy.enabled=true")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "networkPolicy.apiServer.cidrs")
}

func TestPodDisruptionBudget(t *testing.T) {
	t.Parallel()

	docs, err := render(t, "podDisruptionBudget.enabled=true")
	require.NoError(t, err)

	var budget policyv1.PodDisruptionBudget
	require.NoError(t, yaml.Unmarshal(docs["PodDisruptionBudget"], &budget))
	require.NotNil(t, budget.Spec.MaxUnavailable)
	assert.Equal(t, int32(1), budget.Spec.MaxUnavailable.IntVal)
	assert.Equal(t, "harbor-operator", budget.Spec.Selector.MatchLabels["app.kubernetes.io/name"])
}
