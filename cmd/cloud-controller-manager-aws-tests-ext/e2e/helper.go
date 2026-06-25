package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	ec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// GetCloudConfig retrieves the CCM cloud-config ConfigMap.
// When cs is nil, a clientset is created from the current kubeconfig.
// This function must not call Ginkgo control-flow helpers (Skip, Fail, etc.)
// because it is also called from main.go outside a spec context.
func GetCloudConfig(ctx context.Context, cs clientset.Interface) (*v1.ConfigMap, error) {
	var err error
	if cs == nil {
		restConfig, err := framework.LoadConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
		}
		cs, err = clientset.NewForConfig(restConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create kube clientset: %w", err)
		}
	}
	cm, err := cs.CoreV1().ConfigMaps(cloudConfigNamespace).Get(ctx, cloudConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get cloud-config ConfigMap: %w", err)
	}
	return cm, nil
}

// isConfigPresentCloudConfig checks if a specific configuration key is present in the
// cloud-config data stored in the given ConfigMap. It searches all data entries for an
// INI-style key=value match. Values are split by comma to support multi-value configs
// e.g.: "ipFamilies = IPv4,IPv6" returns ["IPv4", "IPv6"], and
// "NLBSecurityGroupMode" = "Managed" returns ["Managed"].
func isConfigPresentCloudConfig(cm *v1.ConfigMap, configKey string) (bool, []string, error) {
	if cm == nil {
		return false, nil, fmt.Errorf("ConfigMap is nil")
	}
	if configKey == "" {
		return false, nil, fmt.Errorf("configKey is empty")
	}

	pattern, err := regexp.Compile(`(?m)^\s*` + regexp.QuoteMeta(configKey) + `\s*=\s*(.*)$`)
	if err != nil {
		return false, nil, fmt.Errorf("failed to compile regex for key %q: %w", configKey, err)
	}

	for dataKey, content := range cm.Data {
		allMatches := pattern.FindAllStringSubmatch(content, -1)
		if allMatches == nil {
			continue
		}

		var values []string
		for _, matches := range allMatches {
			rawValue := strings.TrimSpace(matches[1])
			if rawValue == "" {
				continue
			}
			for _, p := range strings.Split(rawValue, ",") {
				if v := strings.TrimSpace(p); v != "" {
					values = append(values, v)
				}
			}
		}

		framework.Logf("Found key %q in ConfigMap data key %q with values: %v", configKey, dataKey, values)
		return true, values, nil
	}

	framework.Logf("Key %q not found in ConfigMap %s/%s", configKey, cm.Namespace, cm.Name)
	return false, nil, nil
}

// IsDualStack checks the NodeIPFamilies key in the cloud-config ConfigMap.
// It returns (isDualStack, primaryIPv6, error) where isDualStack is true when
// both IPv4 and IPv6 are present, and primaryIPv6 is true when the first
// entry is IPv6 (e.g. NodeIPFamilies=ipv6 then NodeIPFamilies=ipv4).
// When NodeIPFamilies is absent, both booleans are false with no error.
func IsDualStack(cm *v1.ConfigMap) (bool, bool, error) {
	found, values, err := isConfigPresentCloudConfig(cm, "NodeIPFamilies")
	if err != nil {
		return false, false, fmt.Errorf("failed to lookup up configuration NodeIPFamilies in cloud-config: %w", err)
	}
	if !found {
		return false, false, nil
	}
	var hasIPv4, hasIPv6 bool
	for _, ipFamily := range values {
		switch strings.ToLower(ipFamily) {
		case "ipv6":
			hasIPv6 = true
		case "ipv4":
			hasIPv4 = true
		}
	}
	primaryIPv6 := len(values) > 0 && strings.ToLower(values[0]) == "ipv6"
	return hasIPv4 && hasIPv6, primaryIPv6, nil
}
