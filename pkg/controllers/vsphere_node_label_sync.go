package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	klog "k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/vsphere"
)

// SyncVSphereNodeLabels performs a single pass over all nodes and retroactively applies the
// node.openshift.io/platform-type=vsphere label to vSphere nodes that are missing it. The vSphere
// CCM only ever applies this label (via its --node-labels flag, see pkg/cloud/vsphere) when it
// initializes a node for the first time, so nodes that joined the cluster before the
// VSphereMixedNodeEnv feature gate was enabled never get labeled without this repair pass. Hybrid
// clusters can mix vSphere and bare-metal nodes, so each node's spec.providerID is checked to
// confirm it is actually a vSphere node before labeling it.
//
// This is run to completion once by the node-label-sync-job Job that the CVO installs
// (see manifests/0000_90_cloud-controller-manager-operator_00_job.yaml),
// rather than as an ongoing in-process controller.
func SyncVSphereNodeLabels(ctx context.Context, c client.Client, featureGateAccess featuregates.FeatureGateAccess) error {
	infra := &configv1.Infrastructure{}
	if err := c.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("infrastructure resource not found, skipping node label sync")
			return nil
		}
		return fmt.Errorf("failed to get infrastructure: %w", err)
	}

	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.Type != configv1.VSpherePlatformType {
		klog.V(2).Infof("platform is not vSphere, skipping node label sync")
		return nil
	}

	currentFeatureGates, err := featureGateAccess.CurrentFeatureGates()
	if err != nil {
		return fmt.Errorf("failed to get current feature gates: %w", err)
	}
	if !currentFeatureGates.Enabled(features.FeatureGateVSphereMixedNodeEnv) {
		klog.V(2).Infof("%s feature gate is disabled, skipping node label sync", features.FeatureGateVSphereMixedNodeEnv)
		return nil
	}

	nodeList := &corev1.NodeList{}
	if err := c.List(ctx, nodeList); err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	var errs []error
	for i := range nodeList.Items {
		if err := syncVSphereNodeLabel(ctx, c, &nodeList.Items[i]); err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", nodeList.Items[i].Name, err))
		}
	}

	return errors.Join(errs...)
}

func syncVSphereNodeLabel(ctx context.Context, c client.Client, node *corev1.Node) error {
	if !strings.HasPrefix(node.Spec.ProviderID, vsphere.NodeProviderIDPrefix) {
		return nil
	}

	if _, ok := node.Labels[vsphere.NodePlatformTypeLabelKey]; ok {
		return nil
	}

	patch := client.MergeFrom(node.DeepCopy())
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	node.Labels[vsphere.NodePlatformTypeLabelKey] = vsphere.NodePlatformTypeLabelValueVSphere

	if err := c.Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to patch label: %w", err)
	}

	klog.Infof("added %s=%s label to node %s", vsphere.NodePlatformTypeLabelKey, vsphere.NodePlatformTypeLabelValueVSphere, node.Name)
	return nil
}
