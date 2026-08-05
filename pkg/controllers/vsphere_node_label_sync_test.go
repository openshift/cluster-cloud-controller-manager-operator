package controllers

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/vsphere"
)

func newTestInfrastructure(platformType configv1.PlatformType) *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: infrastructureResourceName},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{Type: platformType},
		},
	}
}

func newTestNode(name, providerID string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: providerID},
	}
}

func TestSyncVSphereNodeLabels(t *testing.T) {
	vsphereNodeMissingLabel := newTestNode("vsphere-missing-label", "vsphere://4210e24f-8828-9387-8b6d-d4ebab373827", nil)
	vsphereNodeAlreadyLabeled := newTestNode("vsphere-already-labeled", "vsphere://another-uuid", map[string]string{
		vsphere.NodePlatformTypeLabelKey: vsphere.NodePlatformTypeLabelValueVSphere,
	})
	vsphereNodeWithOtherLabelValue := newTestNode("vsphere-other-label-value", "vsphere://yet-another-uuid", map[string]string{
		vsphere.NodePlatformTypeLabelKey: "some-other-value",
	})
	bareMetalNode := newTestNode("bare-metal", "baremetalhost://some-id", nil)
	noProviderIDNode := newTestNode("no-provider-id", "", nil)

	testCases := []struct {
		name               string
		platformType       configv1.PlatformType
		featureGateEnabled bool
		nodes              []*corev1.Node
		expectLabeled      []string
		expectNotLabeled   []string
	}{
		{
			name:               "labels vsphere node missing the label when gate enabled on vSphere platform",
			platformType:       configv1.VSpherePlatformType,
			featureGateEnabled: true,
			nodes:              []*corev1.Node{vsphereNodeMissingLabel, vsphereNodeAlreadyLabeled, vsphereNodeWithOtherLabelValue, bareMetalNode, noProviderIDNode},
			expectLabeled:      []string{"vsphere-missing-label", "vsphere-already-labeled"},
			expectNotLabeled:   []string{"bare-metal", "no-provider-id", "vsphere-other-label-value"},
		},
		{
			name:               "does nothing when feature gate is disabled",
			platformType:       configv1.VSpherePlatformType,
			featureGateEnabled: false,
			nodes:              []*corev1.Node{vsphereNodeMissingLabel},
			expectNotLabeled:   []string{"vsphere-missing-label"},
		},
		{
			name:               "does nothing on non-vSphere platforms",
			platformType:       configv1.AWSPlatformType,
			featureGateEnabled: true,
			nodes:              []*corev1.Node{vsphereNodeMissingLabel},
			expectNotLabeled:   []string{"vsphere-missing-label"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{newTestInfrastructure(tc.platformType)}
			for _, n := range tc.nodes {
				objs = append(objs, n.DeepCopy())
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()

			var featureGateAccess featuregates.FeatureGateAccess
			if tc.featureGateEnabled {
				featureGateAccess = featuregates.NewHardcodedFeatureGateAccess(
					[]configv1.FeatureGateName{features.FeatureGateVSphereMixedNodeEnv}, nil)
			} else {
				featureGateAccess = featuregates.NewHardcodedFeatureGateAccess(nil,
					[]configv1.FeatureGateName{features.FeatureGateVSphereMixedNodeEnv})
			}

			require.NoError(t, SyncVSphereNodeLabels(context.Background(), fakeClient, featureGateAccess))

			for _, name := range tc.expectLabeled {
				node := &corev1.Node{}
				require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: name}, node))
				assert.Equal(t, vsphere.NodePlatformTypeLabelValueVSphere, node.Labels[vsphere.NodePlatformTypeLabelKey], "node %s should be labeled", name)
			}

			for _, name := range tc.expectNotLabeled {
				node := &corev1.Node{}
				require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: name}, node))
				assert.NotEqual(t, vsphere.NodePlatformTypeLabelValueVSphere, node.Labels[vsphere.NodePlatformTypeLabelKey], "node %s should not be labeled", name)

				if name == vsphereNodeWithOtherLabelValue.Name {
					assert.Equal(t, "some-other-value", node.Labels[vsphere.NodePlatformTypeLabelKey], "node %s's existing platform-type label should be left untouched", name)
				}
			}
		})
	}
}
