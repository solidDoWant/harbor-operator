// Package secrets renders the Kubernetes Secrets that hold a robot's credentials. It never talks to Harbor or to the
// Kubernetes API.
package secrets

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

const (
	// AnnotationSecretHash holds the hex SHA-256 of the robot secret the Secret was written with.
	AnnotationSecretHash = "harbor.soliddowant.dev/secret-sha256"
	// LabelManagedBy marks Secrets the operator writes.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedByValue is the value of LabelManagedBy.
	ManagedByValue = "harbor-operator"
)

// Opaque Secret keys.
const (
	KeyUsername = "username"
	KeyPassword = "password"
	KeyRegistry = "registry"
)

// Input is everything needed to render a robot's Secrets.
type Input struct {
	// Owner is the robot resource; it must be registered in Scheme.
	Owner client.Object
	// Namespace the Secrets are written to: the owner's namespace.
	Namespace string
	// Spec is the robot's secret options.
	Spec harborv1alpha1.RobotSecretSpec
	// RegistryHost is host[:port] from the connection.
	RegistryHost string
	// HarborName is the robot's full name as Harbor returned it.
	HarborName string
	// Secret is the robot's secret.
	Secret string
	Scheme *runtime.Scheme
}

// DockerConfigJSONName returns the name of the dockerconfigjson Secret.
func DockerConfigJSONName(ownerName string, spec harborv1alpha1.RobotSecretSpec) string {
	if spec.DockerConfigJSON.Name != "" {
		return spec.DockerConfigJSON.Name
	}

	return ownerName + "-dockerconfig"
}

// OpaqueName returns the name of the opaque Secret.
func OpaqueName(ownerName string, spec harborv1alpha1.RobotSecretSpec) string {
	if spec.Opaque.Name != "" {
		return spec.Opaque.Name
	}

	return ownerName + "-credentials"
}

// DockerConfigJSONEnabled reports whether the dockerconfigjson Secret is wanted (default true).
func DockerConfigJSONEnabled(spec harborv1alpha1.RobotSecretSpec) bool {
	return spec.DockerConfigJSON.Enabled == nil || *spec.DockerConfigJSON.Enabled
}

// OpaqueEnabled reports whether the opaque Secret is wanted (default false).
func OpaqueEnabled(spec harborv1alpha1.RobotSecretSpec) bool {
	return spec.Opaque.Enabled != nil && *spec.Opaque.Enabled
}

// Hash returns the hex SHA-256 of a robot secret.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

type dockerAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

type dockerConfig struct {
	Auths map[string]dockerAuth `json:"auths"`
}

// Render returns the enabled Secrets, dockerconfigjson first.
func Render(in Input) ([]*corev1.Secret, error) {
	var out []*corev1.Secret

	if DockerConfigJSONEnabled(in.Spec) {
		config, err := json.Marshal(dockerConfig{Auths: map[string]dockerAuth{
			in.RegistryHost: {
				Username: in.HarborName,
				Password: in.Secret,
				Auth:     base64.StdEncoding.EncodeToString([]byte(in.HarborName + ":" + in.Secret)),
			},
		}})
		if err != nil {
			return nil, fmt.Errorf("encoding docker config: %w", err)
		}

		secret, err := base(in, DockerConfigJSONName(in.Owner.GetName(), in.Spec))
		if err != nil {
			return nil, err
		}

		secret.Type = corev1.SecretTypeDockerConfigJson
		secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: config}
		out = append(out, secret)
	}

	if OpaqueEnabled(in.Spec) {
		secret, err := base(in, OpaqueName(in.Owner.GetName(), in.Spec))
		if err != nil {
			return nil, err
		}

		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			KeyUsername: []byte(in.HarborName),
			KeyPassword: []byte(in.Secret),
			KeyRegistry: []byte(in.RegistryHost),
		}
		out = append(out, secret)
	}

	return out, nil
}

func base(in Input, name string) (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   in.Namespace,
			Labels:      map[string]string{LabelManagedBy: ManagedByValue},
			Annotations: map[string]string{AnnotationSecretHash: Hash(in.Secret)},
		},
	}
	if err := controllerutil.SetControllerReference(in.Owner, secret, in.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner reference: %w", err)
	}

	return secret, nil
}

// IsOwnedBy reports whether the Secret has owner as its controller, so this robot wrote it.
func IsOwnedBy(secret *corev1.Secret, owner client.Object) bool {
	ref := metav1.GetControllerOf(secret)
	return ref != nil && ref.UID != "" && ref.UID == owner.GetUID()
}
