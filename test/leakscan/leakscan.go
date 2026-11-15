// Package leakscan finds secret values in the places the operator must never write them: resource statuses, events
// and its own log. Tests give it the secrets they know about and it reports every place one appears.
package leakscan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Log collects log lines. It is safe for concurrent use.
type Log struct {
	mutex sync.Mutex
	buf   bytes.Buffer
}

// Logger returns a logger at maximum verbosity that writes to the log.
func (log *Log) Logger() logr.Logger {
	return funcr.New(func(prefix, args string) {
		log.mutex.Lock()
		defer log.mutex.Unlock()

		log.buf.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{Verbosity: 100})
}

// String returns everything logged so far.
func (log *Log) String() string {
	log.mutex.Lock()
	defer log.mutex.Unlock()

	return log.buf.String()
}

// Leaks reports where each of secrets appears in text. Empty secrets are ignored. The report never contains a
// secret value.
func Leaks(source, text string, secrets map[string]string) []string {
	var found []string

	for label, value := range secrets {
		if value != "" && strings.Contains(text, value) {
			found = append(found, fmt.Sprintf("%s contains %s", source, label))
		}
	}

	return found
}

// Scan looks for secrets (label to value) in the status of every object of the given list kinds in all namespaces,
// in every event, and in the log. It returns one description per leak; none means clean. Statuses are scanned as
// JSON, so base64 or JSON-escaped copies of the value are not found; pass those forms as extra secrets.
func Scan(ctx context.Context, cl client.Client, log *Log, lists []client.ObjectList, secrets map[string]string) ([]string, error) {
	found := Leaks("operator log", log.String(), secrets)

	for _, list := range lists {
		if err := cl.List(ctx, list); err != nil {
			return nil, fmt.Errorf("list %T: %w", list, err)
		}

		items, err := meta.ExtractList(list)
		if err != nil {
			return nil, err
		}

		for _, item := range items {
			text, err := statusJSON(item)
			if err != nil {
				return nil, err
			}

			accessor, err := meta.Accessor(item)
			if err != nil {
				return nil, err
			}

			found = append(found, Leaks(fmt.Sprintf("status of %T %s/%s", item, accessor.GetNamespace(), accessor.GetName()), text, secrets)...)
		}
	}

	var events eventsv1.EventList
	if err := cl.List(ctx, &events); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}

	for _, event := range events.Items {
		text, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}

		found = append(found, Leaks(fmt.Sprintf("event %s/%s", event.Namespace, event.Name), string(text), secrets)...)
	}

	var coreEvents corev1.EventList
	if err := cl.List(ctx, &coreEvents); err != nil {
		return nil, fmt.Errorf("list core events: %w", err)
	}

	for _, event := range coreEvents.Items {
		text, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}

		found = append(found, Leaks(fmt.Sprintf("event %s/%s", event.Namespace, event.Name), string(text), secrets)...)
	}

	return found, nil
}

// statusJSON returns the JSON of the object's status field, or "" when it has none.
func statusJSON(object runtime.Object) (string, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return "", err
	}

	status, ok := content["status"]
	if !ok {
		return "", nil
	}

	text, err := json.Marshal(status)

	return string(text), err
}
