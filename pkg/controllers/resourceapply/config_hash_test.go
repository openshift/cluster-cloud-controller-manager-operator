package resourceapply

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gmg "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

func getOperatorConfigForAzureStack() config.OperatorConfig {
	return config.OperatorConfig{
		ManagedNamespace: "openshift-cloud-controller-manager",
		ImagesReference: config.ImagesReference{
			CloudControllerManagerOperator: "op",
			CloudControllerManagerAzure:    "foo",
			CloudNodeManagerAzure:          "bar",
		},
		PlatformStatus: &configv1.PlatformStatus{
			Type: configv1.AzurePlatformType,
			Azure: &configv1.AzurePlatformStatus{
				CloudName: configv1.AzureStackCloud,
			},
		},
		InfrastructureName: "my-cool-cluster-777",
	}
}

func getVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "foo", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "secret"},
			},
		},
		{
			Name: "bar", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "secret2"},
			},
		},
		{
			Name: "baz", VolumeSource: corev1.VolumeSource{
				// same secret as for foo to check that it won't be added into sources twice
				Secret: &corev1.SecretVolumeSource{SecretName: "secret"},
			},
		},
		{
			Name: "fizz", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "configmap"},
				},
			},
		},
		{
			Name: "fuzz", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "configmap2"},
				},
			},
		},
	}
}

func getContainers() []corev1.Container {
	return []corev1.Container{
		{
			Name: "foo", EnvFrom: []corev1.EnvFromSource{
				{
					ConfigMapRef: &corev1.ConfigMapEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "envConfigMap"},
					},
				},
			},
		},
		{
			Name: "bar", EnvFrom: []corev1.EnvFromSource{
				{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "envSecret"},
					},
				},
			},
		},
		{
			Name: "fizz", Env: []corev1.EnvVar{
				{
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "envConfigMap2"},
						},
					},
				},
			},
		},
		{
			Name: "fizz",
			Env: []corev1.EnvVar{
				{
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "envSecret2"},
						},
					},
				},
			},
		},
	}
}

func TestCollectDependantConfigs(t *testing.T) {
	tcs := []struct {
		name               string
		podTemplate        *corev1.PodTemplateSpec
		expectedSecrets    []string
		expectedConfigMaps []string
	}{
		{
			name:               "nil pod template spec",
			podTemplate:        nil,
			expectedSecrets:    []string{},
			expectedConfigMaps: []string{},
		},
		{
			name: "Empty pod template spec",
			podTemplate: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{},
			},
			expectedSecrets:    []string{},
			expectedConfigMaps: []string{},
		},
		{
			name: "secret and config map volumes",
			podTemplate: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: getVolumes(),
				},
			},
			expectedSecrets:    []string{"secret", "secret2"},
			expectedConfigMaps: []string{"configmap", "configmap2"},
		},
		{
			name: "container secrets and configmaps",
			podTemplate: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: getContainers(),
				},
			},
			expectedSecrets:    []string{"envSecret", "envSecret2"},
			expectedConfigMaps: []string{"envConfigMap", "envConfigMap2"},
		},
		{
			name: "initContainer secrets and configmaps",
			podTemplate: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					InitContainers: getContainers(),
				},
			},
			expectedSecrets:    []string{"envSecret", "envSecret2"},
			expectedConfigMaps: []string{"envConfigMap", "envConfigMap2"},
		},
		{
			name: "everything",
			podTemplate: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers:     getContainers()[:2],
					InitContainers: getContainers()[2:],
					Volumes:        getVolumes(),
				},
			},
			expectedSecrets:    []string{"envSecret", "envSecret2", "secret", "secret2"},
			expectedConfigMaps: []string{"configmap", "configmap2", "envConfigMap", "envConfigMap2"},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			g := gmg.NewWithT(t)
			emptySpecSources := collectRelatedConfigSources(tc.podTemplate)
			g.Expect(sets.List(emptySpecSources.Secrets)).To(gmg.Equal(tc.expectedSecrets))
			g.Expect(sets.List(emptySpecSources.ConfigMaps)).To(gmg.Equal(tc.expectedConfigMaps))
		})
	}

	t.Run("Test related config collection from Azure Stack manifests", func(t *testing.T) {
		g := gmg.NewWithT(t)
		resources, err := cloud.GetResources(getOperatorConfigForAzureStack())
		g.Expect(err).ToNot(gmg.HaveOccurred())

		for _, resource := range resources {
			switch r := resource.(type) {
			case *appsv1.Deployment:
				sources := collectRelatedConfigSources(&r.Spec.Template)
				g.Expect(sets.List(sources.Secrets)).To(gmg.BeComparableTo([]string{"azure-cloud-credentials"}))
				g.Expect(sets.List(sources.ConfigMaps)).To(gmg.BeComparableTo([]string{"ccm-trusted-ca", "cloud-conf"}))
			case *appsv1.DaemonSet:
				sources := collectRelatedConfigSources(&r.Spec.Template)
				g.Expect(sets.List(sources.Secrets)).To(gmg.BeComparableTo([]string{"azure-cloud-credentials"}))
				g.Expect(sets.List(sources.ConfigMaps)).To(gmg.BeComparableTo([]string{"ccm-trusted-ca", "cloud-conf"}))
			}
		}
	})
}

func TestCalculateConfigsHash(t *testing.T) {
	sources := configSources{
		ConfigMaps: sets.New[string]("configmap"),
		Secrets:    sets.New[string]("secret"),
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "test",
		},
		Data: map[string]string{
			"foo": "bar",
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secret",
			Namespace: "test",
		},
		Data: map[string][]byte{
			"foo": []byte("bar"),
		},
		Type: "Opaque",
	}

	t.Run("no resources found", func(t *testing.T) {
		g := gmg.NewWithT(t)

		fakeClient := fake.NewClientBuilder().Build()

		hash, err := calculateRelatedConfigsHash(context.TODO(), fakeClient, "test", sources)
		g.Expect(hash).To(gmg.Equal(""))
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(err.Error()).To(gmg.ContainSubstring("configmaps \"configmap\" not found"))
		g.Expect(err.Error()).To(gmg.ContainSubstring("secrets \"secret\" not found"))
	})

	t.Run("calculate hash", func(t *testing.T) {
		g := gmg.NewWithT(t)

		fakeClient := fake.NewClientBuilder().WithObjects(configMap, secret).Build()

		hash, err := calculateRelatedConfigsHash(context.TODO(), fakeClient, "test", sources)
		g.Expect(hash).To(gmg.Equal("c7f9345a2f1d730784440ab608460066f1c6f5af4662de2a5ff61e1cd81d5bad"))
		g.Expect(err).NotTo(gmg.HaveOccurred())
	})

	t.Run("calculate hash with empty sources", func(t *testing.T) {
		g := gmg.NewWithT(t)

		fakeClient := fake.NewClientBuilder().Build()

		hash, err := calculateRelatedConfigsHash(context.TODO(), fakeClient, "test", configSources{})
		g.Expect(hash).To(gmg.Equal("100444e91862dd77d7ebe29f050c1e9a7f357c771e1a7b7650aae27e6a3a031d"))
		g.Expect(err).NotTo(gmg.HaveOccurred())
	})
}
