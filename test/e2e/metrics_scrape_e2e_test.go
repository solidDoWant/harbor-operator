//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/test/harborfixture"
)

const (
	operatorNamespace = "harbor-operator-system"
	// The metrics Service of the release test/e2e/up.sh installs.
	metricsURL = "https://harbor-operator-metrics." + operatorNamespace + ".svc:8443/metrics"
	// Pinned by digest; quay.io publishes the curl project's image.
	curlImage = "quay.io/curl/curl:8.11.1@sha256:2db4e6a8fd6a0e4d0db5828b2722cf6db15c3005178a4c65588b903e4784ba11"
)

// scrape runs curl in a pod with the service account's token and returns the
// HTTP status line and the body.
func scrape(t *testing.T, serviceAccount string) (string, string) {
	t.Helper()

	cl, clientset := kubeClients(t)
	ctx := t.Context()

	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: serviceAccount}}
	if err := cl.Create(ctx, account); err != nil {
		require.True(t, strings.Contains(err.Error(), "already exists"), "create service account: %v", err)
	}

	t.Cleanup(func() { _ = cl.Delete(context.Background(), account) })

	token, err := clientset.CoreV1().ServiceAccounts(operatorNamespace).CreateToken(ctx, serviceAccount,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr(int64(600))}}, metav1.CreateOptions{})
	require.NoError(t, err)

	podName := fmt.Sprintf("scrape-%s-%d", serviceAccount, time.Now().UnixNano())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: podName},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  "curl",
				Image: curlImage,
				Env:   []corev1.EnvVar{{Name: "TOKEN", Value: token.Status.Token}},
				// -k: the serving certificate is self-signed. -i prints the status line.
				Command: []string{"sh", "-c", `curl -sik -H "Authorization: Bearer $TOKEN" ` + metricsURL},
			}},
		},
	}
	require.NoError(t, cl.Create(ctx, pod))
	t.Cleanup(func() { _ = cl.Delete(context.Background(), pod) })

	require.Eventually(t, func() bool {
		current, err := clientset.CoreV1().Pods(operatorNamespace).Get(ctx, podName, metav1.GetOptions{})

		return err == nil && (current.Status.Phase == corev1.PodSucceeded || current.Status.Phase == corev1.PodFailed)
	}, 3*time.Minute, time.Second, "waiting for the scrape pod to finish")

	logs, err := clientset.CoreV1().Pods(operatorNamespace).GetLogs(podName, &corev1.PodLogOptions{}).Stream(ctx)
	require.NoError(t, err)

	defer func() { _ = logs.Close() }()

	output, err := io.ReadAll(logs)
	require.NoError(t, err)

	status, body, _ := strings.Cut(string(output), "\n")

	return strings.TrimSpace(status), body
}

func TestMetricsEndpointServesListedServiceAccountsOnly(t *testing.T) {
	// The resource gauge has a series only when a resource exists.
	cl := kubeClient(t)
	namespace := harborfixture.UniqueName(t, "scrape")

	require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	t.Cleanup(func() {
		_ = cl.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})
	require.NoError(t, cl.Create(t.Context(), &harborv1alpha1.HarborConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "harbor"},
		Spec: harborv1alpha1.HarborConnectionSpec{
			URL:                  "http://harbor.invalid",
			CredentialsSecretRef: harborv1alpha1.LocalSecretReference{Name: "missing"},
		},
	}))

	t.Run("listed service account", func(t *testing.T) {
		status, body := scrape(t, "metrics-reader-e2e")
		assert.Contains(t, status, " 200")
		assert.Contains(t, body, "harbor_operator_resources")
	})

	t.Run("unlisted service account", func(t *testing.T) {
		status, body := scrape(t, "metrics-other-e2e")
		assert.Contains(t, status, " 403")
		assert.NotContains(t, body, "harbor_operator_resources")
	})
}
