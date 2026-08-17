package controllers

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	nodeLabelSyncJobActiveDeadlineSeconds int64 = 300
	nodeLabelSyncJobBackoffLimit          int32 = 6
)

// NodeLabelSyncJobReconciler ensures the node-label-sync Job exists on vSphere clusters with the
// VSphereMixedNodeEnv feature gate enabled, creating it if it's missing. It never updates or
// deletes the Job once created: the Job's pod template is immutable, so this mirrors the
// "backfill once" semantics that a CVO-installed manifest would get from the
// release.openshift.io/create-only annotation, without going through CVO's manifest-apply path.
type NodeLabelSyncJobReconciler struct {
	client.Client
	// Namespace is where the node-label-sync Job is created. Set to controllers.OperatorNamespace
	// in production; tests may point it at a dedicated test namespace.
	Namespace string
	// Image is the container image stamped onto the Job, read from the OPERATOR_IMAGE env
	// variable, which carries the same resolved pullspec as the running operator image.
	Image string
	// ReleaseVersion is stamped onto the Job's RELEASE_VERSION env variable.
	ReleaseVersion    string
	FeatureGateAccess featuregates.FeatureGateAccess
}

func (r *NodeLabelSyncJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("infrastructure resource not found, skipping node-label-sync Job creation")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get infrastructure: %w", err)
	}

	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.Type != configv1.VSpherePlatformType {
		klog.V(2).Infof("platform is not vSphere, skipping node-label-sync Job creation")
		return ctrl.Result{}, nil
	}

	if r.FeatureGateAccess == nil {
		return ctrl.Result{}, reconcile.TerminalError(fmt.Errorf("FeatureGateAccess is not configured"))
	}

	currentFeatureGates, err := r.FeatureGateAccess.CurrentFeatureGates()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get current feature gates: %w", err)
	}
	if !currentFeatureGates.Enabled(features.FeatureGateVSphereMixedNodeEnv) {
		klog.V(2).Infof("%s feature gate is disabled, skipping node-label-sync Job creation", features.FeatureGateVSphereMixedNodeEnv)
		return ctrl.Result{}, nil
	}

	existing := &batchv1.Job{}
	err = r.Get(ctx, client.ObjectKey{Name: nodeLabelSyncJobName, Namespace: r.Namespace}, existing)
	if err == nil {
		klog.V(2).Infof("node-label-sync Job already exists, leaving it alone")
		return ctrl.Result{}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed to get node-label-sync Job: %w", err)
	}

	job := r.buildNodeLabelSyncJob()
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("failed to create node-label-sync Job: %w", err)
	}

	klog.Infof("created node-label-sync Job")
	return ctrl.Result{}, nil
}

func (r *NodeLabelSyncJobReconciler) buildNodeLabelSyncJob() *batchv1.Job {
	activeDeadlineSeconds := nodeLabelSyncJobActiveDeadlineSeconds
	backoffLimit := nodeLabelSyncJobBackoffLimit
	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := true
	hostPathDirectory := corev1.HostPathDirectory

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeLabelSyncJobName,
			Namespace: r.Namespace,
			Labels: map[string]string{
				"k8s-app": nodeLabelSyncJobName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"k8s-app": nodeLabelSyncJobName,
					},
					Annotations: map[string]string{
						"target.workload.openshift.io/management": `{"effect": "PreferredDuringScheduling"}`,
						"openshift.io/required-scc":               "hostaccess",
					},
				},
				Spec: corev1.PodSpec{
					PriorityClassName:  "system-node-critical",
					ServiceAccountName: "cluster-cloud-controller-manager",
					RestartPolicy:      corev1.RestartPolicyOnFailure,
					// This Job may run before pod networking (CNI/OVN service routing) is functional
					// on a given node -- e.g. right after a control-plane node reboots during an
					// upgrade. hostNetwork plus sourcing /etc/kubernetes/apiserver-url.env (mirroring
					// the operator Deployment) lets the container reach the API server directly
					// instead of through the "kubernetes" Service ClusterIP, which requires
					// kube-proxy/OVN to already be wired up. That file is only populated on
					// control-plane nodes, hence the master nodeSelector/tolerations.
					HostNetwork: true,
					NodeSelector: map[string]string{
						"node-role.kubernetes.io/master": "",
					},
					Tolerations: []corev1.Toleration{
						{Key: "node-role.kubernetes.io/master", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
						{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: int64Ptr(120)},
						{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: int64Ptr(120)},
						{Key: "node.cloudprovider.kubernetes.io/uninitialized", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
						// CNI relies on CCM to fill in IP information on Node objects.
						// Therefore we must schedule before the CNI can mark the Node as ready.
						{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
					},
					Containers: []corev1.Container{
						{
							Name:  nodeLabelSyncJobName,
							Image: r.Image,
							Command: []string{
								"/bin/bash",
								"-c",
								`#!/bin/bash
set -o allexport
if [[ -f /etc/kubernetes/apiserver-url.env ]]; then
  source /etc/kubernetes/apiserver-url.env
else
  URL_ONLY_KUBECONFIG=/etc/kubernetes/kubeconfig
fi
exec /node-label-sync-job
`,
							},
							Env: []corev1.EnvVar{
								{Name: "RELEASE_VERSION", Value: r.ReleaseVersion},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("50Mi"),
								},
							},
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
							// runAsNonRoot is deliberately NOT set here: this image (like the
							// operator's own Deployment container, which also runs unguarded) has no
							// USER directive in its Dockerfile and defaults to root, and the
							// hostaccess SCC does not reliably assign a non-root UID for this pod.
							// Setting runAsNonRoot: true without a matching runAsUser makes the
							// kubelet refuse to start the container at all ("container has
							// runAsNonRoot and image will run as root").
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "host-etc-kube", MountPath: "/etc/kubernetes", ReadOnly: true},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "host-etc-kube",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc/kubernetes",
									Type: &hostPathDirectory,
								},
							},
						},
					},
				},
			},
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeLabelSyncJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	build := ctrl.NewControllerManagedBy(mgr).
		Named("NodeLabelSyncJobController").
		For(
			&batchv1.Job{},
			builder.WithPredicates(nodeLabelSyncJobPredicate(r.Namespace)),
		).
		Watches(
			&configv1.Infrastructure{},
			handler.EnqueueRequestsFromMapFunc(toNodeLabelSyncJob(r.Namespace)),
			builder.WithPredicates(infrastructurePredicates()),
		).
		Watches(
			&configv1.FeatureGate{},
			handler.EnqueueRequestsFromMapFunc(toNodeLabelSyncJob(r.Namespace)),
			builder.WithPredicates(featureGatePredicates()),
		)

	return build.Complete(r)
}

func int64Ptr(v int64) *int64 {
	return &v
}
