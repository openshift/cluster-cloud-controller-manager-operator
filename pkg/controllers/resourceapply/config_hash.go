package resourceapply

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const configHashAnnotation = "operator.openshift.io/config-hash"

type configSources struct {
	ConfigMaps sets.Set[string]
	Secrets    sets.Set[string]
}

// collectRelatedConfigSources looks into pod template spec for secret or config map references.
// Currently, checks volumes and env vars for each container,
// returns configSources structure which contains sets of config maps and secrets names.
func collectRelatedConfigSources(spec *corev1.PodTemplateSpec) configSources {
	sources := configSources{
		ConfigMaps: sets.Set[string]{},
		Secrets:    sets.Set[string]{},
	}

	if spec == nil {
		return sources
	}

	for _, volume := range spec.Spec.Volumes {
		if volume.ConfigMap != nil {
			sources.ConfigMaps.Insert(volume.ConfigMap.Name)
		}
		if volume.Secret != nil {
			sources.Secrets.Insert(volume.Secret.SecretName)
		}
	}

	for _, initContainer := range spec.Spec.InitContainers {
		collectRelatedConfigsFromContainer(&initContainer, &sources)
	}

	for _, container := range spec.Spec.Containers {
		collectRelatedConfigsFromContainer(&container, &sources)
	}

	return sources
}

// collectRelatedConfigsFromContainer collects related configs names into passed configSources instance.
// Looks into env and envVar of the passed container spec and populates configSources with configmaps and secrets names.
func collectRelatedConfigsFromContainer(container *corev1.Container, sources *configSources) {
	for _, envVar := range container.EnvFrom {
		if envVar.ConfigMapRef != nil {
			sources.ConfigMaps.Insert(envVar.ConfigMapRef.Name)
		}
		if envVar.SecretRef != nil {
			sources.Secrets.Insert(envVar.SecretRef.Name)
		}
	}
	for _, envVar := range container.Env {
		if envVar.ValueFrom == nil {
			continue
		}
		if envVar.ValueFrom.ConfigMapKeyRef != nil {
			sources.ConfigMaps.Insert(envVar.ValueFrom.ConfigMapKeyRef.Name)
		}
		if envVar.ValueFrom.SecretKeyRef != nil {
			sources.Secrets.Insert(envVar.ValueFrom.SecretKeyRef.Name)
		}
	}
}

// calculateRelatedConfigsHash calculates configmaps and secrets content hash.
// Returns error in case object was not found or error during object request occured.
func calculateRelatedConfigsHash(ctx context.Context, cl runtimeclient.Client, ns string, source configSources) (string, error) {
	hashSource := struct {
		ConfigMaps map[string]map[string]string `json:"configMaps"`
		Secrets    map[string]map[string][]byte `json:"secrets"`
	}{
		ConfigMaps: make(map[string]map[string]string),
		Secrets:    make(map[string]map[string][]byte),
	}

	var errList []error

	for _, cm := range source.ConfigMaps.UnsortedList() {
		obj := &corev1.ConfigMap{}
		if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: cm}, obj); err != nil {
			errList = append(errList, err)
		} else {
			hashSource.ConfigMaps[cm] = obj.Data
		}
	}

	for _, secret := range source.Secrets.UnsortedList() {
		obj := &corev1.Secret{}
		if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: secret}, obj); err != nil {
			errList = append(errList, err)
		} else {
			hashSource.Secrets[secret] = obj.Data
		}
	}

	if len(errList) > 0 {
		return "", errors.NewAggregate(errList)
	}

	hashSourceBytes, err := json.Marshal(hashSource)
	if err != nil {
		return "", fmt.Errorf("unable to marshal dependant config content into JSON: %v", err)
	}
	hashBytes := sha256.Sum256(hashSourceBytes)
	return fmt.Sprintf("%x", hashBytes), nil
}

// annotatePodSpecWithRelatedConfigsHash annotates pod template spec with a hash of related config maps and secrets content.
func annotatePodSpecWithRelatedConfigsHash(ctx context.Context, cl runtimeclient.Client, ns string, spec *corev1.PodTemplateSpec) error {
	sources := collectRelatedConfigSources(spec)
	hash, err := calculateRelatedConfigsHash(ctx, cl, ns, sources)
	if err != nil {
		return fmt.Errorf("error calculating configuration hash: %w", err)
	}
	if spec.Annotations == nil {
		spec.Annotations = map[string]string{}
	}
	spec.Annotations[configHashAnnotation] = hash
	return nil
}
