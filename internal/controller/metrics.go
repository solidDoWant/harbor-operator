package controller

import (
	"context"
	"slices"

	"github.com/prometheus/client_golang/prometheus"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// ReasonUnknown is the reason label of a resource that has no Ready condition yet or a reason this operator does not set.
const ReasonUnknown = "Unknown"

// ConditionReasons lists every reason label value of the resources gauge.
var ConditionReasons = []string{
	harborv1alpha1.ReasonReady, harborv1alpha1.ReasonCredentialsInvalid, harborv1alpha1.ReasonUnauthorized,
	harborv1alpha1.ReasonForbidden, harborv1alpha1.ReasonNotFound, harborv1alpha1.ReasonHarborUnavailable,
	harborv1alpha1.ReasonDependentsExist, harborv1alpha1.ReasonNotAllowed, harborv1alpha1.ReasonConflict,
	harborv1alpha1.ReasonAlreadyExists, harborv1alpha1.ReasonAdoptionNotAllowed, harborv1alpha1.ReasonInvalidSpec,
	harborv1alpha1.ReasonDeleteBlocked, harborv1alpha1.ReasonProjectNotReady, harborv1alpha1.ReasonSecretConflict,
	harborv1alpha1.ReasonExpired, harborv1alpha1.ReasonReferenceResolved, harborv1alpha1.ReasonRegistryNotReady,
	harborv1alpha1.ReasonRegistryOnOtherHarbor, harborv1alpha1.ReasonProjectNotFound, harborv1alpha1.ReasonOperatorCredential,
	ReasonUnknown,
}

// RegistryHealths lists every value of the registry health label.
var RegistryHealths = []string{
	harborv1alpha1.RegistryHealthHealthy, harborv1alpha1.RegistryHealthUnhealthy, harborv1alpha1.RegistryHealthUnknown,
}

var (
	resourcesDesc = prometheus.NewDesc("harbor_operator_resources",
		"Number of resources by kind and the reason of their Ready condition.", []string{"kind", "reason"}, nil)
	registryHealthDesc = prometheus.NewDesc("harbor_operator_registry_health",
		"Number of HarborRegistry resources by upstream health.", []string{"health"}, nil)
)

// ResourceCollector exports the number of resources by kind and Ready reason, and the number of registries by health.
// It lists from the manager's cache on each scrape and exports nothing until elected is closed, so a standby replica
// stays silent. Labels are kind names and fixed enums only.
type ResourceCollector struct {
	reader  client.Reader
	elected <-chan struct{}
}

// NewResourceCollector returns a collector that reads through reader.
func NewResourceCollector(reader client.Reader, elected <-chan struct{}) *ResourceCollector {
	return &ResourceCollector{reader: reader, elected: elected}
}

// Describe implements prometheus.Collector.
func (c *ResourceCollector) Describe(descs chan<- *prometheus.Desc) {
	descs <- resourcesDesc

	descs <- registryHealthDesc
}

// Collect implements prometheus.Collector.
func (c *ResourceCollector) Collect(metrics chan<- prometheus.Metric) {
	select {
	case <-c.elected:
	default:
		return
	}

	ctx := context.Background()

	var (
		connections  harborv1alpha1.HarborConnectionList
		projects     harborv1alpha1.HarborProjectList
		registries   harborv1alpha1.HarborRegistryList
		robots       harborv1alpha1.HarborProjectRobotList
		systemRobots harborv1alpha1.HarborSystemRobotList
		retentions   harborv1alpha1.HarborRetentionPolicyList
		immutability harborv1alpha1.HarborImmutabilityPolicyList
	)

	counts := map[[2]string]int{}
	listed := func(kind string, list client.ObjectList, conditions func(item int) []metav1.Condition) {
		if err := c.reader.List(ctx, list); err != nil {
			return
		}

		for index := range apimeta.LenList(list) {
			counts[[2]string{kind, readyReason(conditions(index))}]++
		}
	}

	listed("HarborConnection", &connections, func(i int) []metav1.Condition { return connections.Items[i].Status.Conditions })
	listed("HarborProject", &projects, func(i int) []metav1.Condition { return projects.Items[i].Status.Conditions })
	listed("HarborRegistry", &registries, func(i int) []metav1.Condition { return registries.Items[i].Status.Conditions })
	listed("HarborProjectRobot", &robots, func(i int) []metav1.Condition { return robots.Items[i].Status.Conditions })
	listed("HarborSystemRobot", &systemRobots, func(i int) []metav1.Condition { return systemRobots.Items[i].Status.Conditions })
	listed("HarborRetentionPolicy", &retentions, func(i int) []metav1.Condition { return retentions.Items[i].Status.Conditions })
	listed("HarborImmutabilityPolicy", &immutability, func(i int) []metav1.Condition { return immutability.Items[i].Status.Conditions })

	for key, count := range counts {
		metrics <- prometheus.MustNewConstMetric(resourcesDesc, prometheus.GaugeValue, float64(count), key[0], key[1])
	}

	health := map[string]int{}

	for index := range registries.Items {
		value := registries.Items[index].Status.Health
		if !slices.Contains(RegistryHealths, value) {
			value = harborv1alpha1.RegistryHealthUnknown
		}

		health[value]++
	}

	for _, value := range RegistryHealths {
		metrics <- prometheus.MustNewConstMetric(registryHealthDesc, prometheus.GaugeValue, float64(health[value]), value)
	}
}

func readyReason(conditions []metav1.Condition) string {
	ready := apimeta.FindStatusCondition(conditions, harborv1alpha1.ConditionReady)
	if ready == nil || !slices.Contains(ConditionReasons, ready.Reason) {
		return ReasonUnknown
	}

	return ready.Reason
}
