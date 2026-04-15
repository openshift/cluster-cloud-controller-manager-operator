package e2e

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eservice "k8s.io/kubernetes/test/e2e/framework/service"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	e2eTestPrefixLoadBalancer = "[cloud-provider-aws-e2e-openshift] loadbalancer"

	// featureGateAWSServiceLBNetworkSecurityGroup is the name of the feature gate
	// that enables managed security groups for Network Load Balancers.
	//
	// Future improvement: Use typed constant from github.com/openshift/api/features
	// when available: features.FeatureGateAWSServiceLBNetworkSecurityGroup
	featureGateAWSServiceLBNetworkSecurityGroup = "AWSServiceLBNetworkSecurityGroup"

	annotationLBType           = "service.beta.kubernetes.io/aws-load-balancer-type"
	annotationLBSecurityGroups = "service.beta.kubernetes.io/aws-load-balancer-security-groups"

	cloudConfigNamespace = "openshift-cloud-controller-manager"
	cloudConfigName      = "cloud-conf"
	cloudConfigKey       = "cloud.conf"
)

// TestAWSServiceLBNetworkSecurityGroup validates the AWSServiceLBNetworkSecurityGroup feature gate functionality.
//
// This test suite validates that Network Load Balancers (NLB) are properly configured with security groups
// when the AWSServiceLBNetworkSecurityGroup feature gate is enabled. This feature allows the cloud controller
// to manage security groups for NLB services, improving security posture and reducing manual configuration.
//
// All tests automatically skip if the AWSServiceLBNetworkSecurityGroup feature gate is not enabled.
var _ = Describe(fmt.Sprintf("%s NLB [OCPFeatureGate:%s]", e2eTestPrefixLoadBalancer, featureGateAWSServiceLBNetworkSecurityGroup), func() {
	f := framework.NewDefaultFramework("cloud-provider-aws")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	var cs clientset.Interface
	var ns *v1.Namespace

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
	})

	// Checker function to verify if the feature gate is enabled for the group of tests for feature AWSServiceLBNetworkSecurityGroup.
	isNLBFeatureEnabled := func(ctx context.Context) {
		By(fmt.Sprintf("checking if %s feature gate is enabled", featureGateAWSServiceLBNetworkSecurityGroup))
		featureEnabled, err := isFeatureEnabled(ctx, featureGateAWSServiceLBNetworkSecurityGroup)
		framework.ExpectNoError(err, fmt.Sprintf("failed to check if %s feature is enabled", featureGateAWSServiceLBNetworkSecurityGroup))
		if !featureEnabled {
			Skip(fmt.Sprintf("%s feature gate is not enabled", featureGateAWSServiceLBNetworkSecurityGroup))
		}
	}

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should have NLBSecurityGroupMode with 'Managed' value in cloud-config
	//
	// Validates that the cloud controller manager's configuration contains the proper NLBSecurityGroupMode setting
	// when the AWSServiceLBNetworkSecurityGroup feature gate is enabled.
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//
	// Expected Results:
	//   - ConfigMap exists and contains cloud.conf key
	//   - Configuration includes: NLBSecurityGroupMode set to 'Managed'
	//	 - The test must fail if the feature gate is enabled and the configuration does not include NLBSecurityGroupMode set to 'Managed'
	//   - The test must skip if the feature gate is not enabled
	It("should have NLBSecurityGroupMode with 'Managed value in cloud-config", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("getting cloud-config ConfigMap from openshift-cloud-controller-manager namespace")
		cm, err := cs.CoreV1().ConfigMaps(cloudConfigNamespace).Get(ctx, cloudConfigName, metav1.GetOptions{})
		framework.ExpectNoError(err, "failed to get cloud-config ConfigMap")

		By("checking if cloud.conf key exists in ConfigMap")
		cloudConf, exists := cm.Data[cloudConfigKey]
		Expect(exists).To(BeTrue(), "cloud.conf key not found in ConfigMap")

		By("verifying NLBSecurityGroupMode is present in cloud config")
		Expect(cloudConf).To(ContainSubstring("NLBSecurityGroupMode"),
			"NLBSecurityGroupMode must be present in cloud-config when feature gate is enabled")

		By("verifying NLBSecurityGroupMode is set to Managed")
		Expect(cloudConf).To(MatchRegexp(`NLBSecurityGroupMode\s*=\s*Managed`),
			"NLBSecurityGroupMode must be set to 'Managed' in cloud-config when feature gate is enabled")

		framework.Logf("Successfully validated cloud-config contains NLBSecurityGroupMode = Managed")
	})

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should create NLB service with security group attached
	//
	// Creates a new Service type loadBalancer Network Load Balancer (NLB) and validates that security groups are
	// automatically attached to the NLB when the AWSServiceLBNetworkSecurityGroup feature is enabled.
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//
	// Expected Results:
	//   - Service type loadBalancer Network Load Balancer (NLB) is created successfully
	//   - Backend pods start and become ready
	//   - Load balancer has one or more security groups attached when NLBSecurityGroupMode = Managed
	//   - The test must fail if the feature gate is enabled and the NLB does not have security groups attached
	//   - The test must skip if the feature gate is not enabled
	It("should create NLB service with security group attached", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("creatomg required AWS clients")
		elbClient, err := createAWSClientLoadBalancer(ctx)
		framework.ExpectNoError(err, "failed to create AWS ELB client")

		By("creating test service and deployment configuration")
		serviceName := "nlb-sg-crt"
		_, jig, err := createServiceNLB(ctx, cs, ns, serviceName, map[int32]int32{80: 8080})
		framework.ExpectNoError(err, "failed to create NLB service load balancer")

		foundLB, lbDNS, err := getNLBMetaFromName(ctx, cs, ns, serviceName, elbClient)
		framework.ExpectNoError(err, "failed to get NLB metadata")
		Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

		DeferCleanup(func(cleanupCtx context.Context) {
			err := deleteServiceAndWaitForLoadBalancerDeletion(cleanupCtx, jig, lbDNS)
			framework.ExpectNoError(err, "failed to delete service and wait for load balancer deletion")
		})

		By("verifying security groups are attached to the NLB")
		Expect(len(foundLB.SecurityGroups)).To(BeNumerically(">", 0),
			"load balancer should have security groups attached when NLBSecurityGroupMode = Managed")

		framework.Logf("Successfully validated that load balancer has %d security group(s) attached", len(foundLB.SecurityGroups))
		for i, sg := range foundLB.SecurityGroups {
			framework.Logf("  Security Group %d: %s", i+1, sg)
		}
	})

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should have security groups attached to default ingress controller NLB
	//
	// Validates that the default OpenShift ingress controller's Service type loadBalancer Network Load Balancer (NLB) has security groups
	// attached when the AWSServiceLBNetworkSecurityGroup feature is enabled and the router uses NLB type.
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//   - The default ingress controller is using NLB type
	//
	// Expected Result:
	//   - Default router service exists and is of type LoadBalancer
	//   - Service uses NLB type (service.beta.kubernetes.io/aws-load-balancer-type: nlb)
	//   - Load balancer is in Active state
	//   - Load balancer has one or more security groups attached
	//
	// Note: Skips if the default ingress controller is not using NLB type
	It("should have security groups attached to default ingress controller NLB", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("creatomg required AWS clients")
		elbClient, err := createAWSClientLoadBalancer(ctx)
		framework.ExpectNoError(err, "failed to create AWS ELB client")

		By("getting default ingress controller service")
		ingressNamespace := "openshift-ingress"
		ingressServiceName := "router-default"

		var svc *v1.Service
		err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			s, err := cs.CoreV1().Services(ingressNamespace).Get(ctx, ingressServiceName, metav1.GetOptions{})
			if err != nil {
				framework.Logf("Failed to get service %s/%s: %v", ingressNamespace, ingressServiceName, err)
				return false, nil
			}
			svc = s
			return true, nil
		})
		framework.ExpectNoError(err, "failed to get default ingress controller service")

		By("verifying service is of type LoadBalancer")
		Expect(svc.Spec.Type).To(Equal(v1.ServiceTypeLoadBalancer),
			"default ingress controller service should be of type LoadBalancer")

		By("checking if service has LoadBalancer ingress hostname")
		Expect(len(svc.Status.LoadBalancer.Ingress)).To(BeNumerically(">", 0),
			"no ingress entry found in LoadBalancer status")

		lbDNS := svc.Status.LoadBalancer.Ingress[0].Hostname
		Expect(lbDNS).NotTo(BeEmpty(), "LoadBalancer hostname should not be empty")
		framework.Logf("Default ingress controller load balancer DNS: %s", lbDNS)

		By("checking if the service is NLB type")
		lbType, hasAnnotation := svc.Annotations[annotationLBType]
		if !hasAnnotation || lbType != "nlb" {
			Skip("Default ingress controller is not using NLB type, skipping test")
		}
		framework.Logf("Default ingress controller is using NLB type")

		var foundLB *elbv2types.LoadBalancer
		err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true, func(pollCtx context.Context) (bool, error) {
			lb, err := getAWSLoadBalancerFromDNSName(pollCtx, elbClient, lbDNS)
			if err != nil {
				framework.Logf("Failed to find load balancer with DNS %s: %v", lbDNS, err)
				return false, nil
			}
			if lb != nil && lb.State != nil && lb.State.Code == elbv2types.LoadBalancerStateEnumActive {
				foundLB = lb
				return true, nil
			}
			if lb == nil {
				framework.Logf("Load balancer %s not returned yet", lbDNS)
				return false, nil
			}
			framework.Logf("Load balancer not yet active, current state: %v", lb.State)
			return false, nil
		})
		framework.ExpectNoError(err, "failed to find active load balancer")
		Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

		By("verifying security groups are attached to the default ingress NLB")
		Expect(len(foundLB.SecurityGroups)).To(BeNumerically(">", 0),
			"default ingress load balancer should have security groups attached when NLBSecurityGroupMode = Managed")

		framework.Logf("Successfully validated that default ingress load balancer has %d security group(s) attached", len(foundLB.SecurityGroups))
		for i, sg := range foundLB.SecurityGroups {
			framework.Logf("  Security Group %d: %s", i+1, sg)
		}
	})

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should keep security groups attached after service update
	//
	// Creates a Service type loadBalancer Network Load Balancer (NLB), modifies the service specification,
	// and validates that security groups remain attached after the update.
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//
	// Expected Results:
	//   - Service type loadBalancer Network Load Balancer (NLB) is created successfully
	//   - Load balancer has security groups attached before update
	//   - Service can be updated (ports, session affinity, etc.)
	//   - Load balancer still has security groups attached after update
	//   - Security group rules are updated to include the new port 443
	//   - The test must fail if security groups are removed after service update
	It("should update security group rules when service is updated", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("creatomg required AWS clients")
		ec2Client, err := createAWSClientEC2(ctx)
		framework.ExpectNoError(err, "failed to create AWS EC2 client")

		elbClient, err := createAWSClientLoadBalancer(ctx)
		framework.ExpectNoError(err, "failed to create AWS ELB client")

		By("creating test service and deployment configuration")
		serviceName := "nlb-sg-upd"
		_, jig, err := createServiceNLB(ctx, cs, ns, serviceName, map[int32]int32{80: 8080})
		framework.ExpectNoError(err, "failed to create NLB service load balancer")

		foundLB, lbDNS, err := getNLBMetaFromName(ctx, cs, ns, serviceName, elbClient)
		framework.ExpectNoError(err, "failed to get NLB metadata")
		Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

		DeferCleanup(func(cleanupCtx context.Context) {
			err := deleteServiceAndWaitForLoadBalancerDeletion(cleanupCtx, jig, lbDNS)
			framework.ExpectNoError(err, "failed to delete service and wait for load balancer deletion")
		})

		By("verifying security groups are attached before update")
		Expect(len(foundLB.SecurityGroups)).To(BeNumerically(">", 0),
			"load balancer should have security groups attached before update")
		framework.Logf("Load balancer has %d security group(s) attached before update", len(foundLB.SecurityGroups))

		By("getting security group rules")
		originalSGRules, err := getAWSSecurityGroupRules(ctx, ec2Client, foundLB.SecurityGroups)
		framework.ExpectNoError(err, "failed to get security group rules")

		By("updating service: adding a new port")
		_, err = jig.UpdateService(ctx, func(s *v1.Service) {
			s.Spec.Ports = append(s.Spec.Ports, v1.ServicePort{
				Name:       "https",
				Protocol:   v1.ProtocolTCP,
				Port:       443,
				TargetPort: intstr.FromInt(8443),
			})
		})
		framework.ExpectNoError(err, "failed to update service")
		framework.Logf("Service updated successfully")

		By("waiting for security group rules to include the new port 443")
		Eventually(ctx, func(ctx context.Context) ([]int32, error) {
			foundLB, err = getAWSLoadBalancerFromDNSName(ctx, elbClient, lbDNS)
			if err != nil {
				framework.Logf("Error finding load balancer: %v", err)
				return nil, err
			}
			if foundLB == nil {
				framework.Logf("Load balancer not found yet")
				return nil, fmt.Errorf("load balancer not found yet")
			}
			if len(foundLB.SecurityGroups) == 0 {
				framework.Logf("Load balancer has no security groups attached")
				return nil, fmt.Errorf("load balancer has no security groups attached")
			}

			currentSGRules, err := getAWSSecurityGroupRules(ctx, ec2Client, foundLB.SecurityGroups)
			if err != nil {
				framework.Logf("failed to get security group rules to calculate the diff: %v", err)
				return nil, err
			}
			if len(originalSGRules) >= len(currentSGRules) {
				framework.Logf("Security group rules count did not changed: original=%d current=%d",
					len(originalSGRules), len(currentSGRules))
				return nil, fmt.Errorf("security group rules count did not changed")
			}

			// We want the security group have the rules for both ports 80 and 443.
			requiredPorts := map[int32]bool{
				80:  false,
				443: false,
			}
			for _, rule := range currentSGRules {
				if rule.ToPort != nil {
					requiredPorts[*rule.ToPort] = true
				}
			}
			for port, covered := range requiredPorts {
				if !covered {
					framework.Logf("Security group rules do not yet have rule for port %d", port)
					return nil, fmt.Errorf("security group rules do not yet have rule for port %d", port)
				}
			}
			framework.Logf("All security groups have rules for both ports 80 and 443")
			return []int32{80, 443}, nil
		}).WithTimeout(2*time.Minute).WithPolling(10*time.Second).Should(SatisfyAll(
			ContainElement(int32(80)),
			ContainElement(int32(443)),
		), "security groups should have rules for both ports 80 and 443 after service update")
	})

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should cleanup security groups when service is deleted
	//
	// Creates a Service type loadBalancer Network Load Balancer (NLB), captures the attached security group IDs,
	// deletes the service, and validates that the managed security groups are properly cleaned up.
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//
	// Expected Results:
	//   - Service type loadBalancer Network Load Balancer (NLB) is created successfully
	//   - Load balancer has security groups attached
	//   - After service deletion, load balancer is removed
	//   - Managed security groups are cleaned up (deleted or detached)
	//   - The test must fail if managed security groups are not cleaned up
	//   - The test must skip if the feature gate is not enabled
	It("should cleanup security groups when service is deleted", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("creatomg required AWS clients")
		ec2Client, err := createAWSClientEC2(ctx)
		framework.ExpectNoError(err, "failed to create AWS EC2 client")

		elbClient, err := createAWSClientLoadBalancer(ctx)
		framework.ExpectNoError(err, "failed to create AWS ELB client")

		By("creating test service and deployment configuration")
		serviceName := "nlb-sg-cleanup-test"

		_, jig, err := createServiceNLB(ctx, cs, ns, serviceName, map[int32]int32{80: 8080})
		framework.ExpectNoError(err, "failed to create NLB service load balancer")

		foundLB, lbDNS, err := getNLBMetaFromName(ctx, cs, ns, serviceName, elbClient)
		framework.ExpectNoError(err, "failed to get NLB metadata")
		Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

		By("verifying and capturing security groups")
		Expect(len(foundLB.SecurityGroups)).To(BeNumerically(">", 0),
			"load balancer should have security groups attached")

		securityGroupIDs := foundLB.SecurityGroups
		framework.Logf("Load balancer has %d security group(s) attached", len(securityGroupIDs))
		for i, sg := range securityGroupIDs {
			framework.Logf("  Security Group %d: %s", i+1, sg)
		}

		By("verifying security groups exist before deletion")
		for _, sgID := range securityGroupIDs {
			exists, err := securityGroupExists(ctx, ec2Client, sgID)
			framework.ExpectNoError(err, "failed to check security group %s", sgID)
			Expect(exists).To(BeTrue(), "security group %s should exist before deletion", sgID)
		}

		err = deleteServiceAndWaitForLoadBalancerDeletion(ctx, jig, lbDNS)
		framework.ExpectNoError(err, "failed to delete service and wait for load balancer deletion")

		By("verifying managed security groups are cleaned up")
		// Poll for security group cleanup with timeout (AWS cleanup can take time)
		err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
			allDeleted := true
			for _, sgID := range securityGroupIDs {
				exists, err := securityGroupExists(ctx, ec2Client, sgID)
				if err != nil {
					framework.Logf("Error checking security group %s: %v", sgID, err)
					return false, err
				}
				if exists {
					framework.Logf("Security group %s still exists, waiting for cleanup...", sgID)
					allDeleted = false
				} else {
					framework.Logf("Security group %s was successfully cleaned up", sgID)
				}
			}
			return allDeleted, nil
		})
		framework.ExpectNoError(err, "all managed security groups should be cleaned up after service deletion")
		framework.Logf("Successfully validated that all managed security groups were cleaned up")
	})

	// TODO Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should validate NLB with backend pods is reachable
	//
	// Creates a Service type loadBalancer Network Load Balancer (NLB) with backend pods and validates
	// end-to-end connectivity through the NLB with security groups attached.

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should have correct security group rules for service ports
	//
	// Creates a Service type loadBalancer Network Load Balancer (NLB) and validates that the attached
	// security group has the correct ingress rules matching the service port specifications.
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//
	// Expected Results:
	//   - Service type loadBalancer Network Load Balancer (NLB) is created successfully
	//   - Load balancer has security groups attached
	//   - Security group ingress rules match the service port specifications
	//   - Security group rules allow traffic for all defined service ports
	//   - The test must fail if security group rules don't match service ports
	//   - The test must skip if the feature gate is not enabled
	It("should have correct security group rules for service ports", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("creatomg required AWS clients")
		ec2Client, err := createAWSClientEC2(ctx)
		framework.ExpectNoError(err, "failed to create AWS EC2 client")

		elbClient, err := createAWSClientLoadBalancer(ctx)
		framework.ExpectNoError(err, "failed to create AWS ELB client")

		By("creating test service and deployment configuration")
		serviceName := "nlb-sg-rules-test"
		svc, jig, err := createServiceNLB(ctx, cs, ns, serviceName, map[int32]int32{80: 8080, 443: 8443})
		framework.ExpectNoError(err, "failed to create NLB service load balancer")

		By("extracting load balancer DNS name")
		Expect(len(svc.Status.LoadBalancer.Ingress)).To(BeNumerically(">", 0),
			"no ingress entry found in LoadBalancer status")
		lbDNS := svc.Status.LoadBalancer.Ingress[0].Hostname
		framework.Logf("Load balancer DNS: %s", lbDNS)

		foundLB, lbDNS, err := getNLBMetaFromName(ctx, cs, ns, serviceName, elbClient)
		framework.ExpectNoError(err, "failed to get NLB metadata")
		Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

		DeferCleanup(func(cleanupCtx context.Context) {
			err := deleteServiceAndWaitForLoadBalancerDeletion(cleanupCtx, jig, lbDNS)
			framework.ExpectNoError(err, "failed to delete service and wait for load balancer deletion")
		})

		By("verifying security groups are attached to the NLB")
		Expect(len(foundLB.SecurityGroups)).To(BeNumerically(">", 0),
			"load balancer should have security groups attached")
		framework.Logf("Load balancer has %d security group(s) attached", len(foundLB.SecurityGroups))

		By("inspecting security group rules")
		currentSGRules, err := getAWSSecurityGroupRules(ctx, ec2Client, foundLB.SecurityGroups)
		framework.ExpectNoError(err, "failed to get security group rules to calculate the diff")
		Expect(len(currentSGRules)).To(BeNumerically(">", 0), "security group rules should not be empty")

		expectedPorts := []int32{}
		for _, rule := range currentSGRules {
			if rule.ToPort == nil {
				continue
			}
			if *rule.ToPort == 80 || *rule.ToPort == 443 {
				expectedPorts = append(expectedPorts, *rule.ToPort)
			}
		}
		Expect(expectedPorts).To(ContainElements(int32(80), int32(443)),
			"security groups should include rules for ports 80 and 443")
	})

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should create NLB service with BYO security group and preserve it after deletion
	//
	// Creates a new Service type loadBalancer Network Load Balancer (NLB) with a user-provided (BYO)
	// security group annotation, validates that the specified security group is attached to the NLB,
	// then deletes the service and validates that the BYO security group is preserved (not deleted).
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//
	// Expected Results:
	//   - BYO security group is created successfully
	//   - Service type loadBalancer Network Load Balancer (NLB) is created with BYO SG annotation
	//   - Backend pods start and become ready
	//   - Load balancer has the BYO security group attached (not managed SG)
	//   - BYO security group has no cluster tag (not "owned")
	//   - Service and load balancer are deleted successfully
	//   - BYO security group is NOT deleted (user retains ownership)
	//   - BYO security group still exists in AWS after service deletion
	//   - The test must fail if BYO security group is not attached or is deleted
	//   - The test must skip if the feature gate is not enabled
	It("should create NLB with BYO SG and preserve it after deletion", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("creating required AWS clients")
		ec2Client, err := createAWSClientEC2(ctx)
		framework.ExpectNoError(err, "failed to create AWS EC2 client")

		elbClient, err := createAWSClientLoadBalancer(ctx)
		framework.ExpectNoError(err, "failed to create AWS ELB client")

		By("discovering cluster VPC and name for BYO security group creation")
		vpcID, err := getClusterVPCID(ctx, cs, ec2Client)
		framework.ExpectNoError(err, "failed to get cluster VPC ID")

		clusterName, err := getClusterName(ctx, cs, ec2Client)
		framework.ExpectNoError(err, "failed to get cluster name")

		By("creating BYO security group for testing")
		sgName := fmt.Sprintf("e2e-nlb-byo-sg-create-%s", ns.Name)
		sgDescription := fmt.Sprintf("BYO Security Group for e2e test %s", ns.Name)
		byoSGID, err := createAWSSecurityGroup(ctx, ec2Client, sgName, sgDescription, vpcID)
		framework.ExpectNoError(err, "failed to create BYO security group")
		framework.Logf("created BYO security group: %s", byoSGID)

		// Add cleanup for BYO security group
		DeferCleanup(func(cleanupCtx context.Context) {
			By("cleaning up BYO security group")
			err := waitForSecurityGroupDeletion(cleanupCtx, ec2Client, byoSGID, 5*time.Minute)
			if err != nil {
				framework.Logf("warning: failed to delete BYO security group %s: %v", byoSGID, err)
			}
		})

		By("adding ingress rules to BYO security group")
		ports := []v1.ServicePort{{Port: 80, Protocol: v1.ProtocolTCP}}
		err = authorizeSecurityGroupIngress(ctx, ec2Client, byoSGID, ports)
		framework.ExpectNoError(err, "failed to authorize ingress for BYO security group")

		By("creating test service with BYO security group annotation")
		serviceName := "nlb-byo-sg-create"
		jig := e2eservice.NewTestJig(cs, ns.Name, serviceName)

		svc := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: jig.Namespace,
				Name:      jig.Name,
				Labels:    jig.Labels,
				Annotations: map[string]string{
					annotationLBType:           "nlb",
					annotationLBSecurityGroups: byoSGID,
				},
			},
			Spec: v1.ServiceSpec{
				Type:            v1.ServiceTypeLoadBalancer,
				SessionAffinity: v1.ServiceAffinityNone,
				Selector:        jig.Labels,
				Ports: []v1.ServicePort{
					{
						Name:       "http",
						Protocol:   v1.ProtocolTCP,
						Port:       80,
						TargetPort: intstr.FromInt(8080),
					},
				},
			},
		}

		_, err = jig.Client.CoreV1().Services(jig.Namespace).Create(ctx, svc, metav1.CreateOptions{})
		framework.ExpectNoError(err, "failed to create LoadBalancer Service")

		By("waiting for AWS load balancer provisioning")
		loadBalancerCreateTimeout := e2eservice.GetServiceLoadBalancerCreationTimeout(ctx, cs)
		svc, err = jig.WaitForLoadBalancer(ctx, loadBalancerCreateTimeout)
		framework.ExpectNoError(err, "LoadBalancer provisioning failed")

		By("extracting load balancer DNS name")
		Expect(len(svc.Status.LoadBalancer.Ingress)).To(BeNumerically(">", 0),
			"no ingress entry found in LoadBalancer status")
		lbDNS := svc.Status.LoadBalancer.Ingress[0].Hostname
		framework.Logf("load balancer DNS: %s", lbDNS)

		By("getting NLB from AWS API")
		foundLB, err := getAWSLoadBalancerFromDNSName(ctx, elbClient, lbDNS)
		framework.ExpectNoError(err, "failed to find load balancer with DNS name %s", lbDNS)
		Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

		By("verifying BYO security group is attached to the NLB")
		Expect(len(foundLB.SecurityGroups)).To(BeNumerically(">", 0),
			"load balancer should have security groups attached")

		foundBYOSG := slices.Contains(foundLB.SecurityGroups, byoSGID)
		Expect(foundBYOSG).To(BeTrue(),
			"load balancer should have the BYO security group %s attached, but found: %v", byoSGID, foundLB.SecurityGroups)

		By("verifying BYO security group is not cluster owned")
		isManaged, err := isSecurityGroupManaged(ctx, ec2Client, byoSGID, clusterName)
		framework.ExpectNoError(err, "failed to check if BYO security group is managed")
		Expect(isManaged).To(BeFalse(),
			"BYO security group should NOT be managed by the controller (should not have 'owned' tag)")

		framework.Logf("successfully validated NLB with BYO security group %s", byoSGID)

		By("verifying BYO security group exists before service deletion")
		exists, err := securityGroupExists(ctx, ec2Client, byoSGID)
		framework.ExpectNoError(err, "failed to check if BYO security group exists")
		Expect(exists).To(BeTrue(), "BYO security group %s should exist before service deletion", byoSGID)

		By("deleting the service")
		err = deleteServiceAndWaitForLoadBalancerDeletion(ctx, jig, lbDNS)
		framework.ExpectNoError(err, "failed to delete service and wait for load balancer deletion")
		framework.Logf("service and load balancer deleted successfully")

		By("verifying BYO security group STILL EXISTS after service deletion")
		exists, err = securityGroupExists(ctx, ec2Client, byoSGID)
		framework.ExpectNoError(err, "failed to check if BYO security group exists after deletion")
		Expect(exists).To(BeTrue(),
			"BYO security group %s should NOT be deleted by the controller (user retains ownership)", byoSGID)

		By("verifying BYO security group properties are unchanged")
		sg, err := getAWSSecurityGroup(ctx, ec2Client, byoSGID)
		framework.ExpectNoError(err, "failed to get BYO security group after service deletion")
		Expect(sg).NotTo(BeNil(), "BYO security group should be retrievable after service deletion")
		Expect(aws.ToString(sg.GroupName)).To(Equal(sgName),
			"BYO security group name should be unchanged")

		// Verify it's still marked as not cluster owned
		isManaged, err = isSecurityGroupManaged(ctx, ec2Client, byoSGID, clusterName)
		framework.ExpectNoError(err, "failed to check if BYO security group is managed")
		Expect(isManaged).To(BeFalse(),
			"BYO security group should still be user-managed (not controller-owned) after service deletion")

		framework.Logf("successfully validated that BYO security group %s was preserved after service deletion", byoSGID)
	})

	// Test: [cloud-provider-aws-e2e-openshift] loadbalancer NLB [OCPFeatureGate:AWSServiceLBNetworkSecurityGroup] should transition NLB between managed and BYO security groups
	//
	// Creates a Service type loadBalancer Network Load Balancer (NLB) with managed security groups,
	// transitions to a user-provided (BYO) security group, then transitions back to managed security groups,
	// validating the full round-trip and verifying BYO security group preservation.
	//
	// Prerequisites:
	//   - AWSServiceLBNetworkSecurityGroup feature gate is enabled
	//
	// Expected Results:
	//   - Service type loadBalancer Network Load Balancer (NLB) is created with managed SG initially
	//   - Managed security group is attached and has cluster ownership tag ("owned")
	//   - Service is updated with BYO security group annotation
	//   - Controller transitions from managed SG to BYO SG
	//   - Load balancer has BYO security group attached after first transition
	//   - Old managed security groups are deleted by the controller
	//   - Service is updated to remove BYO security group annotation
	//   - Controller transitions back from BYO SG to managed SG
	//   - Load balancer has new managed security group attached after second transition
	//   - BYO security group is preserved (not deleted) throughout
	//   - The test must fail if any transition doesn't occur correctly
	//   - The test must skip if the feature gate is not enabled
	It("should transition NLB between managed and BYO security groups", func(ctx context.Context) {
		isNLBFeatureEnabled(ctx)

		By("creating required AWS clients")
		ec2Client, err := createAWSClientEC2(ctx)
		framework.ExpectNoError(err, "failed to create AWS EC2 client")

		elbClient, err := createAWSClientLoadBalancer(ctx)
		framework.ExpectNoError(err, "failed to create AWS ELB client")

		By("discovering cluster name for security group management")
		clusterName, err := getClusterName(ctx, cs, ec2Client)
		framework.ExpectNoError(err, "failed to get cluster name")

		By("creating test service with managed security groups (no BYO annotation)")
		serviceName := "nlb-sg-update"
		_, jig, err := createServiceNLB(ctx, cs, ns, serviceName, map[int32]int32{80: 8080})
		framework.ExpectNoError(err, "failed to create NLB service load balancer")

		foundLB, lbDNS, err := getNLBMetaFromName(ctx, cs, ns, serviceName, elbClient)
		framework.ExpectNoError(err, "failed to get NLB metadata")
		Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

		DeferCleanup(func(cleanupCtx context.Context) {
			err := deleteServiceAndWaitForLoadBalancerDeletion(cleanupCtx, jig, lbDNS)
			framework.ExpectNoError(err, "failed to delete service and wait for load balancer deletion")
		})

		By("verifying managed security groups are attached initially")
		Expect(len(foundLB.SecurityGroups)).To(BeNumerically(">", 0),
			"load balancer should have managed security groups attached initially")
		initialManagedSGs := foundLB.SecurityGroups
		framework.Logf("initial managed security groups: %v", initialManagedSGs)

		By("verifying initial security groups are managed by the controller")
		for _, sgID := range initialManagedSGs {
			isManaged, err := isSecurityGroupManaged(ctx, ec2Client, sgID, clusterName)
			framework.ExpectNoError(err, "failed to check if security group %s is managed", sgID)
			Expect(isManaged).To(BeTrue(),
				"initial security group %s should be managed by the controller", sgID)
		}

		By("discovering cluster VPC for BYO security group creation")
		vpcID, err := getClusterVPCID(ctx, cs, ec2Client)
		framework.ExpectNoError(err, "failed to get cluster VPC ID")

		By("creating BYO security group for transition")
		sgName := fmt.Sprintf("e2e-nlb-byo-sg-update-%s", ns.Name)
		sgDescription := fmt.Sprintf("BYO Security Group for e2e test %s", ns.Name)
		byoSGID, err := createAWSSecurityGroup(ctx, ec2Client, sgName, sgDescription, vpcID)
		framework.ExpectNoError(err, "failed to create BYO security group")
		framework.Logf("created BYO security group: %s", byoSGID)

		// Add cleanup for BYO security group
		DeferCleanup(func(cleanupCtx context.Context) {
			By("cleaning up BYO security group")
			err := waitForSecurityGroupDeletion(cleanupCtx, ec2Client, byoSGID, 5*time.Minute)
			if err != nil {
				framework.Logf("warning: failed to delete BYO security group %s: %v", byoSGID, err)
			}
		})

		By("adding ingress rules to BYO security group")
		ports := []v1.ServicePort{{Port: 80, Protocol: v1.ProtocolTCP}}
		err = authorizeSecurityGroupIngress(ctx, ec2Client, byoSGID, ports)
		framework.ExpectNoError(err, "failed to authorize ingress for BYO security group")

		By("updating service to use BYO security group annotation")
		_, err = jig.UpdateService(ctx, func(s *v1.Service) {
			if s.Annotations == nil {
				s.Annotations = make(map[string]string)
			}
			s.Annotations[annotationLBSecurityGroups] = byoSGID
		})
		framework.ExpectNoError(err, "failed to update service with BYO security group annotation")
		framework.Logf("service updated with BYO security group annotation: %s", byoSGID)

		By("waiting for controller to reconcile and attach BYO security group")
		var updatedLB *elbv2types.LoadBalancer
		err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true, func(pollCtx context.Context) (bool, error) {
			select {
			case <-pollCtx.Done():
				return false, pollCtx.Err()
			default:
			}

			lb, err := getAWSLoadBalancerFromDNSName(pollCtx, elbClient, lbDNS)
			if err != nil {
				framework.Logf("error getting load balancer: %v", err)
				return false, nil
			}
			if lb == nil {
				framework.Logf("load balancer not found yet")
				return false, nil
			}

			// Check if BYO SG is attached
			if slices.Contains(lb.SecurityGroups, byoSGID) {
				updatedLB = lb
				framework.Logf("BYO security group %s is now attached to load balancer", byoSGID)
				return true, nil
			}

			framework.Logf("BYO security group not yet attached, current SGs: %v", lb.SecurityGroups)
			return false, nil
		})
		framework.ExpectNoError(err, "BYO security group should be attached to load balancer after service update")
		Expect(updatedLB).NotTo(BeNil(), "updated load balancer should not be nil")

		By("verifying BYO security group is attached to the NLB")
		foundBYOSG := slices.Contains(updatedLB.SecurityGroups, byoSGID)
		Expect(foundBYOSG).To(BeTrue(),
			"load balancer should have BYO security group %s attached after update", byoSGID)

		By("verifying old managed security groups are deleted")
		err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true, func(pollCtx context.Context) (bool, error) {
			select {
			case <-pollCtx.Done():
				return false, pollCtx.Err()
			default:
			}

			allDeleted := true
			for _, sgID := range initialManagedSGs {
				exists, err := securityGroupExists(pollCtx, ec2Client, sgID)
				if err != nil {
					framework.Logf("error checking if managed SG %s exists: %v", sgID, err)
					return false, nil
				}
				if exists {
					framework.Logf("managed security group %s still exists, waiting for cleanup...", sgID)
					allDeleted = false
				} else {
					framework.Logf("managed security group %s was successfully deleted", sgID)
				}
			}
			return allDeleted, nil
		})
		framework.ExpectNoError(err, "old managed security groups should be deleted after BYO SG is attached")

		framework.Logf("successfully validated transition from managed SG to BYO SG %s", byoSGID)

		// Round-trip: Transition back from BYO to managed
		By("updating service to remove BYO security group annotation (transition back to managed)")
		_, err = jig.UpdateService(ctx, func(s *v1.Service) {
			delete(s.Annotations, annotationLBSecurityGroups)
		})
		framework.ExpectNoError(err, "failed to update service to remove BYO security group annotation")
		framework.Logf("service updated to remove BYO security group annotation")

		By("waiting for controller to reconcile and attach new managed security group")
		var finalLB *elbv2types.LoadBalancer
		var newManagedSGIDs []string
		err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true, func(pollCtx context.Context) (bool, error) {
			select {
			case <-pollCtx.Done():
				return false, pollCtx.Err()
			default:
			}

			lb, err := getAWSLoadBalancerFromDNSName(pollCtx, elbClient, lbDNS)
			if err != nil {
				framework.Logf("error getting load balancer: %v", err)
				return false, nil
			}
			if lb == nil {
				framework.Logf("load balancer not found yet")
				return false, nil
			}

			// Check if BYO SG is removed
			hasBYO := slices.Contains(lb.SecurityGroups, byoSGID)
			if hasBYO {
				framework.Logf("BYO security group still attached, waiting for transition back to managed...")
				return false, nil
			}

			// Must have at least one security group
			if len(lb.SecurityGroups) == 0 {
				framework.Logf("no security groups attached yet")
				return false, nil
			}

			// Check if the new SG is managed
			for _, sgID := range lb.SecurityGroups {
				managed, err := isSecurityGroupManaged(pollCtx, ec2Client, sgID, clusterName)
				if err != nil {
					framework.Logf("error checking if SG %s is managed: %v", sgID, err)
					return false, nil
				}
				if managed {
					finalLB = lb
					newManagedSGIDs = lb.SecurityGroups
					framework.Logf("new managed security groups attached: %v", newManagedSGIDs)
					return true, nil
				}
			}

			framework.Logf("waiting for managed security groups to be created and attached")
			return false, nil
		})
		framework.ExpectNoError(err, "new managed security group should be created and attached after removing BYO annotation")
		Expect(finalLB).NotTo(BeNil(), "final load balancer should not be nil")
		Expect(len(newManagedSGIDs)).To(BeNumerically(">", 0), "should have new managed security groups attached")

		By("verifying BYO security group is no longer attached after transition to managed")
		hasBYO := slices.Contains(finalLB.SecurityGroups, byoSGID)
		Expect(hasBYO).To(BeFalse(),
			"BYO security group %s should no longer be attached to load balancer after transition", byoSGID)

		By("verifying new managed security groups are controller-owned")
		for _, sgID := range newManagedSGIDs {
			isManaged, err := isSecurityGroupManaged(ctx, ec2Client, sgID, clusterName)
			framework.ExpectNoError(err, "failed to check if security group %s is managed", sgID)
			Expect(isManaged).To(BeTrue(),
				"security group %s should be managed by the controller (have 'owned' tag)", sgID)
		}

		By("verifying BYO security group still exists (preserved after round-trip)")
		exists, err := securityGroupExists(ctx, ec2Client, byoSGID)
		framework.ExpectNoError(err, "failed to check if BYO security group exists")
		Expect(exists).To(BeTrue(),
			"BYO security group %s should NOT be deleted when transitioning back to managed (user retains ownership)", byoSGID)

		By("verifying BYO security group is not managed by controller")
		isManaged, err := isSecurityGroupManaged(ctx, ec2Client, byoSGID, clusterName)
		framework.ExpectNoError(err, "failed to check if BYO security group is managed")
		Expect(isManaged).To(BeFalse(),
			"BYO security group should still be user-managed after round-trip")

		framework.Logf("successfully validated round-trip: managed → BYO SG %s → managed, with BYO SG preserved", byoSGID)
	})
})

// createServiceNLB creates a Service type loadBalancer Network Load Balancer (NLB) with the given port mapping.
func createServiceNLB(ctx context.Context, cs clientset.Interface, ns *v1.Namespace, serviceName string, portMapping map[int32]int32) (*v1.Service, *e2eservice.TestJig, error) {
	loadBalancerCreateTimeout := e2eservice.GetServiceLoadBalancerCreationTimeout(ctx, cs)
	framework.Logf("AWS load balancer timeout: %s", loadBalancerCreateTimeout)

	jig := e2eservice.NewTestJig(cs, ns.Name, serviceName)

	By("creating NLB LoadBalancer service")
	ports := []v1.ServicePort{}
	for port, targetPort := range portMapping {
		ports = append(ports, v1.ServicePort{
			Name:       fmt.Sprintf("port-%d", port),
			Protocol:   v1.ProtocolTCP,
			Port:       port,
			TargetPort: intstr.FromInt(int(targetPort)),
		})
	}
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: jig.Namespace,
			Name:      jig.Name,
			Labels:    jig.Labels,
			Annotations: map[string]string{
				annotationLBType: "nlb",
			},
		},
		Spec: v1.ServiceSpec{
			Type:            v1.ServiceTypeLoadBalancer,
			SessionAffinity: v1.ServiceAffinityNone,
			Selector:        jig.Labels,
			Ports:           ports,
		},
	}

	_, err := jig.Client.CoreV1().Services(jig.Namespace).Create(ctx, svc, metav1.CreateOptions{})
	framework.ExpectNoError(err, "failed to create LoadBalancer Service")

	By("waiting for AWS load balancer provisioning")
	svc, err = jig.WaitForLoadBalancer(ctx, loadBalancerCreateTimeout)
	framework.ExpectNoError(err, "LoadBalancer provisioning failed")

	return svc, jig, nil
}

// getNLBMetaFromName gets the NLB metadata (AWS API object) from the service/loadbalancer name.
func getNLBMetaFromName(ctx context.Context, cs clientset.Interface, ns *v1.Namespace, serviceName string, elbc *elbv2.Client) (*elbv2types.LoadBalancer, string, error) {
	By("getting service to extract load balancer DNS name")
	svc, err := cs.CoreV1().Services(ns.Name).Get(ctx, serviceName, metav1.GetOptions{})
	framework.ExpectNoError(err, "failed to get service %s", serviceName)

	By("extracting load balancer DNS name")
	Expect(len(svc.Status.LoadBalancer.Ingress)).To(BeNumerically(">", 0),
		"no ingress entry found in LoadBalancer status")

	lbDNS := svc.Status.LoadBalancer.Ingress[0].Hostname
	Expect(lbDNS).NotTo(BeEmpty(), "Ingress Hostname must not be empty")
	framework.Logf("Load balancer DNS: %s", lbDNS)

	foundLB, err := getAWSLoadBalancerFromDNSName(ctx, elbc, lbDNS)
	framework.ExpectNoError(err, "failed to find load balancer with DNS name %s", lbDNS)
	Expect(foundLB).NotTo(BeNil(), "found load balancer is nil")

	return foundLB, lbDNS, nil
}

// deleteServiceAndWaitForLoadBalancerDeletion deletes the service and waits for the load balancer to be deleted.
func deleteServiceAndWaitForLoadBalancerDeletion(ctx context.Context, jig *e2eservice.TestJig, lbDNS string) error {
	By("deleting the service")
	err := jig.Client.CoreV1().Services(jig.Namespace).Delete(ctx, jig.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err, "failed to delete service")

	By("waiting for load balancer to be destroyed")
	loadBalancerCreateTimeout := e2eservice.GetServiceLoadBalancerCreationTimeout(ctx, jig.Client)

	// Get ELB client once before polling
	elbClient, err := createAWSClientLoadBalancer(ctx)
	framework.ExpectNoError(err, "failed to create AWS ELB client")

	// Poll for load balancer deletion
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, loadBalancerCreateTimeout, true, func(ctx context.Context) (bool, error) {
		foundLB, err := getAWSLoadBalancerFromDNSName(ctx, elbClient, lbDNS)
		if err != nil {
			// Check if the error indicates the load balancer was not found (i.e., successfully deleted)
			if strings.Contains(err.Error(), "no load balancer found with DNS name") {
				framework.Logf("Load balancer %s has been deleted", lbDNS)
				return true, nil
			}
			framework.Logf("Error querying load balancer %s during deletion wait: %v", lbDNS, err)
			return false, nil
		}
		if foundLB == nil {
			// LB is gone - success
			framework.Logf("Load balancer %s has been deleted", lbDNS)
			return true, nil
		}
		// LB still exists, keep polling
		framework.Logf("Load balancer %s still exists, waiting for deletion...", lbDNS)
		return false, nil
	})
	framework.ExpectNoError(err, "load balancer should be destroyed after service deletion")
	framework.Logf("Load balancer %s destroyed successfully", lbDNS)

	return nil
}
