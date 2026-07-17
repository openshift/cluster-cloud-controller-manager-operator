package controllers

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The default set of status change reasons.
const (
	ReasonAsExpected          = "AsExpected"
	ReasonInitializing        = "Initializing"
	ReasonSyncing             = "SyncingResources"
	ReasonSyncFailed          = "SyncingFailed"
	ReasonPlatformTechPreview = "PlatformTechPreview"
)

const (
	clusterOperatorName        = "cloud-controller-manager"
	operatorVersionKey         = "operator"
	defaultManagementNamespace = "openshift-cloud-controller-manager-operator"
)

const (
	releaseVersionEnvVariableName = "RELEASE_VERSION"
	unknownVersionValue           = "unknown"
)

type ClusterOperatorStatusClient struct {
	client.Client
	Recorder         record.EventRecorder
	Clock            clock.PassiveClock
	ManagedNamespace string
	ReleaseVersion   string
}

// setStatusDegraded sets the Degraded condition to True, with the given reason and
// message, and sets the upgradeable condition.  It does not modify any existing
// Available or Progressing conditions.
func (r *ClusterOperatorStatusClient) setStatusDegraded(ctx context.Context, reconcileErr error, overrides []configv1.ClusterOperatorStatusCondition) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		klog.Errorf("Failed to get or create Cluster Operator: %v", err)
		return err
	}

	desiredVersions := []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	currentVersions := co.Status.Versions

	var message string
	if !reflect.DeepEqual(desiredVersions, currentVersions) {
		message = fmt.Sprintf("Failed when progressing towards %s because %e", printOperandVersions(desiredVersions), reconcileErr)
	} else {
		message = fmt.Sprintf("Failed to resync for %s because %e", printOperandVersions(desiredVersions), reconcileErr)
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(configv1.OperatorDegraded, configv1.ConditionTrue,
			ReasonSyncFailed, message),
		newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionFalse, ReasonAsExpected, ""),
	}

	r.Recorder.Eventf(co, corev1.EventTypeWarning, "Status degraded", reconcileErr.Error())
	klog.V(2).Infof("Syncing status: degraded: %s", message)
	return r.syncStatus(ctx, co, conds, overrides)
}

// setStatusProgressing sets the Progressing condition to True, with the given
// reason and message, and sets the upgradeable condition to True.  It does not
// modify any existing Available or Degraded conditions.
func (r *ClusterOperatorStatusClient) setStatusProgressing(ctx context.Context, overrides []configv1.ClusterOperatorStatusCondition) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		klog.Errorf("Failed to get or create Cluster Operator: %v", err)
		return err
	}

	desiredVersions := []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	currentVersions := co.Status.Versions

	var message, reason string
	if !reflect.DeepEqual(desiredVersions, currentVersions) {
		message = fmt.Sprintf("Progressing towards %s", printOperandVersions(desiredVersions))
		klog.V(2).Infof("Syncing status: %s", message)
		r.Recorder.Eventf(co, corev1.EventTypeNormal, "Status upgrade", message)
		reason = ReasonSyncing
	} else {
		klog.V(2).Info("Syncing status: re-syncing")
		reason = ReasonAsExpected
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionTrue, reason, message),
		newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
	}

	return r.syncStatus(ctx, co, conds, overrides)
}

func (r *ClusterOperatorStatusClient) ensureStatusProgressing(ctx context.Context, overrides []configv1.ClusterOperatorStatusCondition) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}
	progressing := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
	upgradeable := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorUpgradeable)
	if progressing != nil && progressing.Status == configv1.ConditionTrue &&
		upgradeable != nil && upgradeable.Status == configv1.ConditionTrue {
		return nil
	}
	return r.setStatusProgressing(ctx, overrides)
}

// setStatusAvailable sets the Available condition to True, with the given reason
// and message, and sets both the Progressing and Degraded conditions to False.
func (r *ClusterOperatorStatusClient) setStatusAvailable(ctx context.Context, overrides []configv1.ClusterOperatorStatusCondition) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(configv1.OperatorAvailable, configv1.ConditionTrue, ReasonAsExpected,
			fmt.Sprintf("Cluster Cloud Controller Manager Operator is available at %s", r.ReleaseVersion)),
		newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionFalse, ReasonAsExpected, ""),
		newClusterOperatorStatusCondition(configv1.OperatorDegraded, configv1.ConditionFalse, ReasonAsExpected, ""),
		newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	klog.V(2).Info("Syncing status: available")
	return r.syncStatus(ctx, co, conds, overrides)
}

// clearCloudControllerOwnerCondition clears the CloudControllerOwner condition. This condition
// is not used for OpenShift version 4.16 and later as all cloud controllers are external by
// default, and cannot be rolled back to in-tree.
func (r *CloudOperatorReconciler) clearCloudControllerOwnerCondition(ctx context.Context) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	if co.Status.Conditions == nil {
		// no condtions, nothing to do.
		return nil
	}

	if v1helpers.FindStatusCondition(co.Status.Conditions, cloudControllerOwnershipCondition) == nil {
		// condition is not present, nothing to do.
		return nil
	}

	// if we get here, that means the condition exists and we want to remove it
	v1helpers.RemoveStatusCondition(&co.Status.Conditions, cloudControllerOwnershipCondition)
	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	klog.V(2).Info("Removing CloudControllerOwner condition")
	return r.syncStatus(ctx, co, nil, nil)
}

func printOperandVersions(versions []configv1.OperandVersion) string {
	versionsOutput := []string{}
	for _, operand := range versions {
		versionsOutput = append(versionsOutput, fmt.Sprintf("%s: %s", operand.Name, operand.Version))
	}
	return strings.Join(versionsOutput, ", ")
}

func newClusterOperatorStatusCondition(conditionType configv1.ClusterStatusConditionType,
	conditionStatus configv1.ConditionStatus, reason string,
	message string) configv1.ClusterOperatorStatusCondition {
	return configv1.ClusterOperatorStatusCondition{
		Type:               conditionType,
		Status:             conditionStatus,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

func (r *ClusterOperatorStatusClient) getOrCreateClusterOperator(ctx context.Context) (*configv1.ClusterOperator, error) {
	co := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterOperatorName,
		},
		Status: configv1.ClusterOperatorStatus{},
	}
	err := r.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)
	if errors.IsNotFound(err) {
		klog.Infof("ClusterOperator does not exist, creating a new one.")

		err = r.Create(ctx, co)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster operator: %v", err)
		}
		return co, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get clusterOperator %q: %v", clusterOperatorName, err)
	}
	return co, nil
}

// relatedObjectsStatic returns the platform-agnostic relatedObjects that must
// stay in sync with the ClusterOperator CVO seed manifest.
func (r *ClusterOperatorStatusClient) relatedObjectsStatic() []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: configv1.GroupName, Resource: "clusteroperators", Name: clusterOperatorName},
		{Group: "", Resource: "namespaces", Name: defaultManagementNamespace},
		{Group: "", Resource: "namespaces", Name: r.ManagedNamespace},
		// Operator and provider ClusterRoles / ClusterRoleBindings.
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "system:openshift:operator:" + clusterOperatorName},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: clusterOperatorName},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "cloud-node-manager"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "system:openshift:operator:" + clusterOperatorName},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: clusterOperatorName},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "cloud-node-manager"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "system:openshift:scc:hostaccess:cloud-controller-manager-operator"},
		// Foreign-namespace Roles / RoleBindings (not covered by CCCMO namespace relatedObjects).
		{Group: "rbac.authorization.k8s.io", Resource: "roles", Name: "cluster-cloud-controller-manager", Namespace: "openshift-config"},
		{Group: "rbac.authorization.k8s.io", Resource: "roles", Name: "cluster-cloud-controller-manager", Namespace: "openshift-config-managed"},
		{Group: "rbac.authorization.k8s.io", Resource: "roles", Name: "cluster-cloud-controller-manager", Namespace: "kube-system"},
		{Group: "rbac.authorization.k8s.io", Resource: "roles", Name: "cloud-controller-manager", Namespace: "kube-system"},
		{Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: "cluster-cloud-controller-manager", Namespace: "openshift-config"},
		{Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: "cluster-cloud-controller-manager", Namespace: "openshift-config-managed"},
		{Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: "cluster-cloud-controller-manager", Namespace: "kube-system"},
		{Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: "cloud-controller-manager", Namespace: "kube-system"},
		{Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: "cloud-controller-manager:apiserver-authentication-reader", Namespace: "kube-system"},
	}
}

// relatedObjectsForPlatform returns cluster-scoped / foreign-NS objects from
// platform asset YAMLs. Managed-namespace Deployments/DaemonSets/Roles are omitted
// because namespace relatedObjects already gather those.
func relatedObjectsForPlatform(platform configv1.PlatformType) []configv1.ObjectReference {
	switch platform {
	case configv1.AWSPlatformType:
		return []configv1.ObjectReference{
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: "openshift-cloud-controller-manager-cloud-provider-aws"},
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: "openshift-cloud-controller-manager-cloud-provider-aws"},
		}
	case configv1.AzurePlatformType:
		return []configv1.ObjectReference{
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "azure-cloud-controller-manager"},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "cloud-controller-manager:azure-cloud-controller-manager"},
			{Group: "rbac.authorization.k8s.io", Resource: "roles", Name: "azure-cloud-provider", Namespace: "kube-system"},
			{Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: "azure-cloud-provider:azure-cloud-provider", Namespace: "kube-system"},
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: "openshift-cloud-controller-manager-cloud-provider-azure-node-admission"},
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: "openshift-cloud-controller-manager-cloud-provider-azure-node-admission"},
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: "azure-load-balancer-tcp-idle-timeout-annotation-validation-policy"},
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: "azure-load-balancer-tcp-idle-timeout-validation-annotation-binding"},
		}
	case configv1.GCPPlatformType:
		return []configv1.ObjectReference{
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "gcp-cloud-controller-manager"},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "gcp-cloud-controller-manager:cloud-provider"},
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: "network-tier-annotation-validation-policy"},
			{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: "network-tier-annotation-binding"},
		}
	case configv1.NutanixPlatformType:
		return []configv1.ObjectReference{
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "nutanix-cloud-controller-manager"},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "nutanix-cloud-controller-manager:nutanix-cloud-controller-manager"},
		}
	case configv1.VSpherePlatformType:
		return []configv1.ObjectReference{
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "vsphere-cloud-controller-manager"},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "vsphere-cloud-controller-manager:vsphere-cloud-controller-manager"},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "vsphere-cloud-controller-manager:cloud-controller-manager"},
		}
	case configv1.OpenStackPlatformType:
		return []configv1.ObjectReference{
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "openstack-cloud-controller-manager"},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "openstack-cloud-controller-manager"},
			{Group: "", Resource: "serviceaccounts", Name: "cloud-controller-manager", Namespace: "kube-system"},
		}
	default:
		return nil
	}
}

// relatedObjects returns the static relatedObjects plus any platform-specific
// objects derived from the Infrastructure platform type. If Infrastructure is
// missing or cannot be read, only the static core is returned.
func (r *ClusterOperatorStatusClient) relatedObjects(ctx context.Context) []configv1.ObjectReference {
	objs := r.relatedObjectsStatic()

	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); err != nil {
		return objs
	}
	if infra.Status.PlatformStatus == nil {
		return objs
	}

	return append(objs, relatedObjectsForPlatform(infra.Status.PlatformStatus.Type)...)
}

// syncStatus applies the new condition to the ClusterOperator object.
func (r *ClusterOperatorStatusClient) syncStatus(ctx context.Context, co *configv1.ClusterOperator, conds, overrides []configv1.ClusterOperatorStatusCondition) error {
	for _, c := range conds {
		v1helpers.SetStatusCondition(&co.Status.Conditions, c, r.Clock)
	}

	// These overrides came from the operator controller and override anything set by the setAvaialble, setProgressing, or setDegraded methods.
	for _, c := range overrides {
		v1helpers.SetStatusCondition(&co.Status.Conditions, c, r.Clock)
	}

	related := r.relatedObjects(ctx)
	if !equality.Semantic.DeepEqual(co.Status.RelatedObjects, related) {
		co.Status.RelatedObjects = related
	}

	return r.Status().Update(ctx, co)
}

// GetReleaseVersion gets the release version string from the env
func GetReleaseVersion() string {
	releaseVersion := os.Getenv(releaseVersionEnvVariableName)
	if len(releaseVersion) == 0 {
		releaseVersion = unknownVersionValue
		klog.Infof("%s environment variable is missing, defaulting to %q", releaseVersionEnvVariableName, unknownVersionValue)
	}
	return releaseVersion
}
