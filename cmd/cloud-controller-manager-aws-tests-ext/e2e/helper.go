package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	ec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

// AWS helpers

// createAWSClientLoadBalancer creates an AWS ELBv2 client using default credentials configured in the environment.
func createAWSClientLoadBalancer(ctx context.Context) (*elbv2.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %v", err)
	}
	return elbv2.NewFromConfig(cfg), nil
}

// getAWSLoadBalancerFromDNSName finds a load balancer by DNS name using the AWS ELBv2 client.
func getAWSLoadBalancerFromDNSName(ctx context.Context, elbClient *elbv2.Client, lbDNSName string) (*elbv2types.LoadBalancer, error) {
	var foundLB *elbv2types.LoadBalancer
	framework.Logf("describing load balancers with DNS %s", lbDNSName)

	paginator := elbv2.NewDescribeLoadBalancersPaginator(elbClient, &elbv2.DescribeLoadBalancersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to describe load balancers: %v", err)
		}

		framework.Logf("found %d load balancers in page", len(page.LoadBalancers))
		// Search for the load balancer with matching DNS name in this page
		for i := range page.LoadBalancers {
			if aws.ToString(page.LoadBalancers[i].DNSName) == lbDNSName {
				foundLB = &page.LoadBalancers[i]
				framework.Logf("found load balancer with DNS %s", aws.ToString(foundLB.DNSName))
				break
			}
		}
		if foundLB != nil {
			break
		}
	}

	if foundLB == nil {
		return nil, fmt.Errorf("no load balancer found with DNS name: %s", lbDNSName)
	}

	return foundLB, nil
}

// isFeatureEnabled checks if an OpenShift feature gate is enabled by querying the
// FeatureGate resource named "cluster" using the typed OpenShift config API.
//
// This function uses the official OpenShift config/v1 API types for type-safe
// access to feature gate information, providing better performance and maintainability
// compared to dynamic client approaches.
//
// Parameters:
//   - ctx: Context for the API call
//   - featureName: Name of the feature gate to check (e.g., "AWSServiceLBNetworkSecurityGroup")
//
// Returns:
//   - bool: true if the feature is enabled, false if disabled or not found
//   - error: error if the API call fails
//
// Note: For HyperShift clusters, this checks the management cluster's feature gates.
// To check hosted cluster feature gates, use the hosted cluster's kubeconfig.
func isFeatureEnabled(ctx context.Context, featureName string) (bool, error) {
	// Get the REST config
	restConfig, err := framework.LoadConfig()
	if err != nil {
		return false, fmt.Errorf("failed to load kubeconfig: %v", err)
	}

	// Create typed config client (more efficient than dynamic client)
	configClient, err := configv1client.NewForConfig(restConfig)
	if err != nil {
		return false, fmt.Errorf("failed to create config client: %v", err)
	}

	// Get the FeatureGate resource using typed API
	featureGate, err := configClient.FeatureGates().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get FeatureGate 'cluster': %v", err)
	}

	// Iterate through the feature gates status (typed structs)
	for _, fg := range featureGate.Status.FeatureGates {
		// Check enabled list
		for _, enabled := range fg.Enabled {
			if string(enabled.Name) == featureName {
				framework.Logf("Feature %s is enabled (version %s)", featureName, fg.Version)
				return true, nil
			}
		}

		// Check disabled list
		for _, disabled := range fg.Disabled {
			if string(disabled.Name) == featureName {
				framework.Logf("Feature %s is disabled (version %s)", featureName, fg.Version)
				return false, nil
			}
		}
	}

	// Feature not found in either list
	framework.Logf("Feature %s not found in FeatureGate status", featureName)
	return false, nil
}

// getAWSClientEC2 creates an AWS EC2 client using default credentials configured in the environment.
func createAWSClientEC2(ctx context.Context) (*ec2.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %v", err)
	}
	return ec2.NewFromConfig(cfg), nil
}

// getAWSSecurityGroup retrieves a security group by ID using the AWS EC2 client.
func getAWSSecurityGroup(ctx context.Context, ec2Client *ec2.Client, sgID string) (*ec2types.SecurityGroup, error) {
	framework.Logf("describing security group %s", sgID)
	input := &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{sgID},
	}

	result, err := ec2Client.DescribeSecurityGroups(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe security group %s: %v", sgID, err)
	}

	if len(result.SecurityGroups) == 0 {
		return nil, fmt.Errorf("security group %s not found", sgID)
	}

	return &result.SecurityGroups[0], nil
}

// getAWSSecurityGroupRules gets the security group rules for the given security group IDs.
func getAWSSecurityGroupRules(ctx context.Context, ec2Client *ec2.Client, groups []string) ([]ec2types.IpPermission, error) {
	rules := []ec2types.IpPermission{}
	for _, group := range groups {
		sg, err := getAWSSecurityGroup(ctx, ec2Client, group)
		if err != nil {
			return nil, err
		}
		rules = append(rules, sg.IpPermissions...)
	}
	return rules, nil
}

// securityGroupExists checks if a security group exists by ID.
// Returns true if it exists, false if it doesn't exist or was deleted.
func securityGroupExists(ctx context.Context, ec2Client *ec2.Client, sgID string) (bool, error) {
	framework.Logf("checking if security group %s exists", sgID)
	input := &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{sgID},
	}

	_, err := ec2Client.DescribeSecurityGroups(ctx, input)
	if err != nil {
		// Check if it's a "not found" error
		if ec2IsNotFoundError(err) {
			framework.Logf("security group %s does not exist", sgID)
			return false, nil
		}
		return false, fmt.Errorf("failed to check security group %s: %v", sgID, err)
	}

	framework.Logf("security group %s exists", sgID)
	return true, nil
}

// ec2IsNotFoundError checks if an error is an EC2 "not found" error.
func ec2IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// Check for common EC2 not found error messages
	errMsg := err.Error()
	return strings.Contains(errMsg, "InvalidGroup.NotFound") ||
		strings.Contains(errMsg, "InvalidGroupId.NotFound") ||
		strings.Contains(errMsg, "InvalidGroup.Malformed")
}

// isAWSThrottlingError checks if an error is an AWS throttling/rate limit error.
func isAWSThrottlingError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "Throttling") ||
		strings.Contains(errMsg, "RequestLimitExceeded") ||
		strings.Contains(errMsg, "TooManyRequests") ||
		strings.Contains(errMsg, "RequestThrottled")
}

// createAWSSecurityGroup creates a test security group.
func createAWSSecurityGroup(ctx context.Context, ec2Client *ec2.Client, name, description, vpcID string) (string, error) {
	framework.Logf("creating security group %s in VPC %s", name, vpcID)

	result, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   &name,
		Description: &description,
		VpcId:       &vpcID,
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("Name"),
						Value: &name,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create security group %s: %v", name, err)
	}

	sgID := aws.ToString(result.GroupId)
	framework.Logf("created security group %s with ID %s", name, sgID)
	return sgID, nil
}

// isSecurityGroupManaged checks if a security group is managed by the controller.
// It checks for the cluster ownership tag to determine if the controller owns this security group.
// Managed SGs have tag kubernetes.io/cluster/<name> = "owned"
func isSecurityGroupManaged(ctx context.Context, ec2Client *ec2.Client, sgID, clusterName string) (bool, error) {
	sg, err := getAWSSecurityGroup(ctx, ec2Client, sgID)
	if err != nil {
		return false, err
	}

	// Check for cluster ownership tag
	clusterTagKey := fmt.Sprintf("kubernetes.io/cluster/%s", clusterName)
	for _, tag := range sg.Tags {
		if aws.ToString(tag.Key) == clusterTagKey && aws.ToString(tag.Value) == "owned" {
			return true, nil
		}
	}
	return false, nil
}

// authorizeSecurityGroupIngress adds ingress rules to a security group for the given service ports.
func authorizeSecurityGroupIngress(ctx context.Context, ec2Client *ec2.Client, sgID string, ports []v1.ServicePort) error {
	if len(ports) == 0 {
		return nil
	}

	framework.Logf("authorizing ingress rules for security group %s", sgID)

	ingressRules := make([]ec2types.IpPermission, 0, len(ports))
	for _, port := range ports {
		protocol := strings.ToLower(string(port.Protocol))
		rule := ec2types.IpPermission{
			FromPort:   aws.Int32(port.Port),
			ToPort:     aws.Int32(port.Port),
			IpProtocol: &protocol,
			IpRanges: []ec2types.IpRange{
				{
					CidrIp:      aws.String("0.0.0.0/0"),
					Description: aws.String(fmt.Sprintf("E2E test access for port %d", port.Port)),
				},
			},
		}
		ingressRules = append(ingressRules, rule)
	}

	_, err := ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId:       &sgID,
		IpPermissions: ingressRules,
	})
	if err != nil {
		// Check if error is due to duplicate rules (which is acceptable)
		if strings.Contains(err.Error(), "InvalidPermission.Duplicate") {
			framework.Logf("some rules already exist in security group %s (this is okay)", sgID)
			return nil
		}
		return fmt.Errorf("failed to authorize ingress for security group %s: %v", sgID, err)
	}

	framework.Logf("successfully authorized %d ingress rule(s) for security group %s", len(ingressRules), sgID)
	return nil
}

// deleteAWSSecurityGroup deletes a security group.
func deleteAWSSecurityGroup(ctx context.Context, ec2Client *ec2.Client, sgID string) error {
	framework.Logf("deleting security group %s", sgID)

	_, err := ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
		GroupId: &sgID,
	})
	if err != nil {
		// If already deleted, that's okay
		if ec2IsNotFoundError(err) {
			framework.Logf("security group %s already deleted", sgID)
			return nil
		}
		return fmt.Errorf("failed to delete security group %s: %v", sgID, err)
	}

	framework.Logf("successfully deleted security group %s", sgID)
	return nil
}

// waitForSecurityGroupDeletion attempts to delete a security group and waits for it to be deleted.
// It handles dependency violations when the SG is still attached to resources like load balancers.
func waitForSecurityGroupDeletion(ctx context.Context, ec2Client *ec2.Client, sgID string, timeout time.Duration) error {
	framework.Logf("waiting for security group %s deletion (timeout: %v)", sgID, timeout)

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(pollCtx context.Context) (bool, error) {
		// First check if SG still exists
		exists, err := securityGroupExists(pollCtx, ec2Client, sgID)
		if err != nil {
			// Handle throttling errors by continuing to poll
			if isAWSThrottlingError(err) {
				framework.Logf("AWS throttling encountered while checking security group %s, retrying...", sgID)
				return false, nil
			}
			return false, fmt.Errorf("error checking if security group exists: %v", err)
		}

		if !exists {
			framework.Logf("security group %s successfully deleted", sgID)
			return true, nil
		}

		// Try to delete it
		err = deleteAWSSecurityGroup(pollCtx, ec2Client, sgID)
		if err != nil {
			// Handle throttling errors by continuing to poll
			if isAWSThrottlingError(err) {
				framework.Logf("AWS throttling encountered while deleting security group %s, retrying...", sgID)
				return false, nil
			}

			// Check for dependency violation errors - keep retrying
			if strings.Contains(err.Error(), "DependencyViolation") ||
				strings.Contains(err.Error(), "InvalidGroup.InUse") ||
				strings.Contains(err.Error(), "resource has a dependent object") {
				framework.Logf("security group %s still has dependencies, retrying...", sgID)
				return false, nil // Keep waiting
			}

			// Check if it's already deleted
			if ec2IsNotFoundError(err) {
				framework.Logf("security group %s deleted", sgID)
				return true, nil
			}

			// For other errors, return the error
			return false, err
		}

		// Deletion succeeded
		return true, nil
	})
}

// getClusterInstanceID extracts an EC2 instance ID from a cluster node's provider ID.
func getClusterInstanceID(ctx context.Context, cs clientset.Interface) (string, error) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %v", err)
	}

	if len(nodes.Items) == 0 {
		return "", fmt.Errorf("no nodes found in cluster")
	}

	// Get instance ID from first node
	for _, node := range nodes.Items {
		providerID := node.Spec.ProviderID
		if providerID == "" {
			continue
		}
		// providerID format: aws:///us-east-1a/i-1234567890abcdef0
		providerID = strings.Replace(providerID, "aws:///", "", 1)
		parts := strings.Split(providerID, "/")
		if len(parts) < 2 {
			continue
		}
		instanceID := parts[1]
		if strings.HasPrefix(instanceID, "i-") {
			return instanceID, nil
		}
	}

	return "", fmt.Errorf("could not find valid instance ID from cluster nodes")
}

// getClusterVPCID discovers the VPC ID from a cluster node's network interface.
func getClusterVPCID(ctx context.Context, cs clientset.Interface, ec2Client *ec2.Client) (string, error) {
	instanceID, err := getClusterInstanceID(ctx, cs)
	if err != nil {
		return "", err
	}

	// Describe instance to get VPC ID
	result, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe instance %s: %v", instanceID, err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return "", fmt.Errorf("instance %s not found", instanceID)
	}

	vpcID := aws.ToString(result.Reservations[0].Instances[0].VpcId)
	if vpcID == "" {
		return "", fmt.Errorf("VPC ID not found for instance %s", instanceID)
	}

	framework.Logf("discovered cluster VPC ID: %s", vpcID)
	return vpcID, nil
}

// getClusterName discovers the cluster name from a cluster node's tags.
func getClusterName(ctx context.Context, cs clientset.Interface, ec2Client *ec2.Client) (string, error) {
	instanceID, err := getClusterInstanceID(ctx, cs)
	if err != nil {
		return "", err
	}

	// Describe instance to get cluster tag
	result, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe instance %s: %v", instanceID, err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return "", fmt.Errorf("instance %s not found", instanceID)
	}

	// Find cluster tag (kubernetes.io/cluster/<cluster-name>)
	for _, tag := range result.Reservations[0].Instances[0].Tags {
		key := aws.ToString(tag.Key)
		if after, ok := strings.CutPrefix(key, "kubernetes.io/cluster/"); ok {
			clusterName := after
			framework.Logf("discovered cluster name: %s", clusterName)
			return clusterName, nil
		}
	}

	return "", fmt.Errorf("cluster tag not found on instance %s", instanceID)
}
