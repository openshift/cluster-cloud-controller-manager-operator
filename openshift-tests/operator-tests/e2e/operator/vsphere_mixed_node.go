package operator

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/operator-tests/e2e/common"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
)

const (
	// featureGateVSphereMixedNodeEnv is the name of the feature gate
	// that enables platform-type node labels on vSphere clusters.
	//
	// Future improvement: Use typed constant from github.com/openshift/api/features
	// when available: features.FeatureGateVSphereMixedNodeEnv
	featureGateVSphereMixedNodeEnv = "VSphereMixedNodeEnv"

	// vSphereCCMNamespace is the namespace where the vSphere cloud-controller-manager is deployed
	vSphereCCMNamespace = "openshift-cloud-controller-manager"

	// vSphereCCMDeploymentName is the name of the vSphere cloud-controller-manager deployment
	vSphereCCMDeploymentName = "vsphere-cloud-controller-manager"

	// vSpherePlatformTypeLabel is the label applied to nodes when VSphereMixedNodeEnv is enabled
	vSpherePlatformTypeLabel = "node.openshift.io/platform-type"

	// vSpherePlatformTypeLabelValue is the expected value of the platform-type label
	vSpherePlatformTypeLabelValue = "vsphere"

	// vSphereNodeLabelsParam is the expected parameter in the cloud-controller-manager pod
	vSphereNodeLabelsParam = "--node-labels"

	// clientName is the name of the kube client we will create during testing
	clientName = "cluster-cloud-controller-manager-operator-e2e"
)

// TestVSphereMixedNodeEnv validates the VSphereMixedNodeEnv feature gate functionality.
//
// This test suite validates that the vSphere cloud-controller-manager is properly configured
// with the --node-labels parameter and that nodes have the platform-type label applied
// when the VSphereMixedNodeEnv feature gate is enabled.
//
// All tests automatically skip if the VSphereMixedNodeEnv feature gate is not enabled.
var _ = Describe("[OCPFeatureGate:VSphereMixedNodeEnv][platform:vsphere][Suite:openshift/conformance/parallel] vSphere hybrid environment", Label("vSphere", "Conformance"), func() {
	var (
		err        error
		kubeClient *kubernetes.Clientset
		kubeConfig *rest.Config
	)

	BeforeEach(func() {
		// Get kube client
		kubeConfig, err = common.NewClientConfigForTest()
		if err != nil {
			Fail(fmt.Sprintf("Failed to get kubeconfig: %v", err))
		}
		kubeClient = kubernetes.NewForConfigOrDie(rest.AddUserAgent(kubeConfig, clientName))
		Expect(kubeClient).NotTo(BeNil())
	})

	// Validates that the vSphere cloud-controller-manager pod has the --node-labels parameter
	// configured with the expected platform-type label when the VSphereMixedNodeEnv feature gate is enabled.
	//
	// Prerequisites:
	//   - VSphereMixedNodeEnv feature gate is enabled
	//   - vSphere cloud-controller-manager deployment exists
	//
	// Expected Results:
	//   - At least one cloud-controller-manager pod is running
	//   - Pod contains the --node-labels parameter
	//   - The --node-labels parameter includes "node.openshift.io/platform-type=vsphere"
	//   - The test must fail if the feature gate is enabled and the pod does not have the expected parameter
	//   - The test must skip if the feature gate is not enabled
	It("should have --node-labels parameter in cloud-controller-manager pod", ginkgo.Informing(), func(ctx context.Context) {
		By("Getting cloud-controller-manager pods")
		GinkgoWriter.Printf("Looking for pods in namespace %s with deployment %s\n", vSphereCCMNamespace, vSphereCCMDeploymentName)
		podList, err := kubeClient.CoreV1().Pods(vSphereCCMNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("k8s-app=%s", vSphereCCMDeploymentName),
		})
		framework.ExpectNoError(err, "failed to list cloud-controller-manager pods")
		Expect(podList.Items).NotTo(BeEmpty(), "no cloud-controller-manager pods found")

		By("Checking if at least one pod has the --node-labels parameter")
		var foundPodWithNodeLabels bool
		var podWithNodeLabels *v1.Pod

		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Status.Phase != v1.PodRunning {
				GinkgoWriter.Printf("Skipping pod %s in phase %s\n", pod.Name, pod.Status.Phase)
				continue
			}

			for _, container := range pod.Spec.Containers {
				// Check command and args for the --node-labels parameter
				commandAndArgs := append(container.Command, container.Args...)
				commandStr := strings.Join(commandAndArgs, " ")

				if strings.Contains(commandStr, vSphereNodeLabelsParam) {
					foundPodWithNodeLabels = true
					podWithNodeLabels = pod
					GinkgoWriter.Printf("Found pod %s with %s parameter\n", pod.Name, vSphereNodeLabelsParam)
					break
				}
			}

			if foundPodWithNodeLabels {
				break
			}
		}

		Expect(foundPodWithNodeLabels).To(BeTrue(),
			fmt.Sprintf("%s parameter must be present in cloud-controller-manager pod when %s feature gate is enabled",
				vSphereNodeLabelsParam, featureGateVSphereMixedNodeEnv))

		By("Verifying the --node-labels parameter contains the expected platform-type label")
		var foundExpectedLabel bool
		expectedLabel := fmt.Sprintf("%s=%s", vSpherePlatformTypeLabel, vSpherePlatformTypeLabelValue)

		for _, container := range podWithNodeLabels.Spec.Containers {
			commandAndArgs := append(container.Command, container.Args...)
			commandStr := strings.Join(commandAndArgs, " ")

			if strings.Contains(commandStr, expectedLabel) {
				foundExpectedLabel = true
				GinkgoWriter.Printf("Found expected label %s in pod %s\n", expectedLabel, podWithNodeLabels.Name)
				break
			}

			// Also check environment variables for ADDITIONAL_NODE_LABELS
			for _, env := range container.Env {
				if env.Name == "ADDITIONAL_NODE_LABELS" && strings.Contains(env.Value, expectedLabel) {
					foundExpectedLabel = true
					GinkgoWriter.Printf("Found expected label %s in ADDITIONAL_NODE_LABELS env var in pod %s\n", expectedLabel, podWithNodeLabels.Name)
					break
				}
			}

			if foundExpectedLabel {
				break
			}
		}

		Expect(foundExpectedLabel).To(BeTrue(),
			fmt.Sprintf("%s parameter must contain %s when %s feature gate is enabled",
				vSphereNodeLabelsParam, expectedLabel, featureGateVSphereMixedNodeEnv))

		GinkgoWriter.Printf("Successfully validated %s parameter in cloud-controller-manager pod\n", vSphereNodeLabelsParam)
	})

	// Validates that vSphere nodes have the platform-type label applied when the
	// VSphereMixedNodeEnv feature gate is enabled.
	//
	// In a mixed-node environment, not all nodes will be vSphere nodes. This test verifies
	// that at least the expected number of control plane and worker nodes have the label.
	//
	// Prerequisites:
	//   - VSphereMixedNodeEnv feature gate is enabled
	//   - Cluster has control plane and worker nodes
	//
	// Expected Results:
	//   - ALL control plane nodes have node.openshift.io/platform-type=vsphere
	//   - At least 1 worker node has node.openshift.io/platform-type=vsphere
	//   - The label value is set to "vsphere"
	//   - The test must fail if any control plane node is missing the label
	//   - The test must fail if no worker nodes have the label
	//   - The test must skip if the feature gate is not enabled
	//   - The test works in both hybrid and normal vSphere environments
	It("should apply platform-type label to nodes", ginkgo.Informing(), func(ctx context.Context) {
		By("Getting ready nodes")
		nodeList, err := e2enode.GetReadyNodesIncludingTainted(ctx, kubeClient)
		framework.ExpectNoError(err, "failed to get ready nodes")

		Expect(nodeList.Items).NotTo(BeEmpty(), "no ready nodes found")
		GinkgoWriter.Printf("Found %d ready nodes\n", len(nodeList.Items))

		By("Categorizing nodes by role and checking platform-type label")
		GinkgoWriter.Printf("Checking for label %s on control plane and worker nodes\n", vSpherePlatformTypeLabel)

		var controlPlaneNodesWithLabel []string
		var controlPlaneNodesWithoutLabel []string
		var workerNodesWithLabel []string
		var workerNodesWithoutLabel []string

		for _, node := range nodeList.Items {
			// Determine node role
			isControlPlane := false
			isWorker := false

			if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
				isControlPlane = true
			} else if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
				isControlPlane = true
			} else if _, ok := node.Labels["node-role.kubernetes.io/worker"]; ok {
				isWorker = true
			}

			// Check for platform-type label
			labelValue, hasLabel := node.Labels[vSpherePlatformTypeLabel]

			if hasLabel {
				if labelValue != vSpherePlatformTypeLabelValue {
					framework.Failf("node %s has %s label but with incorrect value: expected %s, got %s",
						node.Name, vSpherePlatformTypeLabel, vSpherePlatformTypeLabelValue, labelValue)
				}

				if isControlPlane {
					controlPlaneNodesWithLabel = append(controlPlaneNodesWithLabel, node.Name)
					GinkgoWriter.Printf("Control plane node %s has %s=%s label\n",
						node.Name, vSpherePlatformTypeLabel, labelValue)
				} else if isWorker {
					workerNodesWithLabel = append(workerNodesWithLabel, node.Name)
					GinkgoWriter.Printf("Worker node %s has %s=%s label\n",
						node.Name, vSpherePlatformTypeLabel, labelValue)
				}
			} else {
				if isControlPlane {
					controlPlaneNodesWithoutLabel = append(controlPlaneNodesWithoutLabel, node.Name)
					GinkgoWriter.Printf("Control plane node %s does not have %s label\n",
						node.Name, vSpherePlatformTypeLabel)
				} else if isWorker {
					workerNodesWithoutLabel = append(workerNodesWithoutLabel, node.Name)
					GinkgoWriter.Printf("Worker node %s does not have %s label\n",
						node.Name, vSpherePlatformTypeLabel)
				}
			}
		}

		By("Verifying labeled nodes")
		GinkgoWriter.Printf("Control plane nodes with label: %d, without label: %d\n",
			len(controlPlaneNodesWithLabel), len(controlPlaneNodesWithoutLabel))
		GinkgoWriter.Printf("Worker nodes with label: %d, without label: %d\n",
			len(workerNodesWithLabel), len(workerNodesWithoutLabel))

		// Verify ALL control plane nodes have the label (works for both hybrid and normal environments)
		Expect(controlPlaneNodesWithoutLabel).To(BeEmpty(),
			fmt.Sprintf("Expected all control plane nodes to have %s=%s label, but %d nodes are missing it. "+
				"Nodes with label: %v, nodes without label: %v",
				vSpherePlatformTypeLabel, vSpherePlatformTypeLabelValue,
				len(controlPlaneNodesWithoutLabel), controlPlaneNodesWithLabel, controlPlaneNodesWithoutLabel))

		// Verify at least 1 worker node has the label (works for both hybrid and normal environments)
		Expect(len(workerNodesWithLabel)).To(BeNumerically(">=", 1),
			fmt.Sprintf("Expected at least 1 worker node with %s=%s label, found %d. "+
				"Nodes with label: %v, nodes without label: %v",
				vSpherePlatformTypeLabel, vSpherePlatformTypeLabelValue,
				len(workerNodesWithLabel), workerNodesWithLabel, workerNodesWithoutLabel))

		GinkgoWriter.Printf("Successfully validated %s label on all %d control plane nodes and %d worker nodes\n",
			vSpherePlatformTypeLabel, len(controlPlaneNodesWithLabel), len(workerNodesWithLabel))
	})
})
