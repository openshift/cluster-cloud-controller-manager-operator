package aws

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	ec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/ccm-aws-tests/e2e/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
)

// awsRegionPattern matches valid AWS region names across all partitions:
// standard (aws: us-east-1), China (aws-cn: cn-northwest-1),
// GovCloud (aws-us-gov: us-gov-west-1), European Sovereign Cloud (aws-eusc: eusc-de-east-1),
// and ISO/ISOB (aws-iso/iso-b: us-isob-east-1).
var awsRegionPattern = regexp.MustCompile(`^[a-z]{2,4}(?:-[a-z0-9]+)+-\d+$`)

// AWS helpers

// loadAWSConfig loads the default AWS SDK configuration. If the resolved region
// is not a valid AWS region (e.g. a CI lease UUID), it falls back to the region
// from the cluster's Infrastructure resource.
func loadAWSConfig(ctx context.Context) (aws.Config, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return aws.Config{}, fmt.Errorf("unable to load AWS config: %v", err)
	}

	// This validation is required to prevent landing wrong hypershift region configurations
	// set by environment variable.
	if !awsRegionPattern.MatchString(cfg.Region) {
		region, err := getRegionFromInfrastructure(ctx)
		if err != nil {
			return aws.Config{}, fmt.Errorf("AWS region %q is not valid and failed to get region from Infrastructure: %v", cfg.Region, err)
		}
		framework.Logf("AWS SDK region %q is not valid, using region from Infrastructure: %s", cfg.Region, region)
		cfg.Region = region
	}

	framework.Logf("AWS config loaded: region=%s", cfg.Region)
	return cfg, nil
}

// getRegionFromInfrastructure reads the AWS region from the cluster's
// Infrastructure resource (status.platformStatus.aws.region).
func getRegionFromInfrastructure(ctx context.Context) (string, error) {
	oc, err := common.GetOcClient(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create config client: %v", err)
	}
	infra, err := oc.Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get Infrastructure: %v", err)
	}
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return "", fmt.Errorf("Infrastructure platformStatus.aws is nil")
	}
	region := infra.Status.PlatformStatus.AWS.Region
	if region == "" {
		return "", fmt.Errorf("Infrastructure platformStatus.aws.region is empty")
	}
	return region, nil
}

// createAWSClientLoadBalancer creates an AWS ELBv2 client using default credentials configured in the environment.
// It forces the public regional endpoint to avoid VPC private endpoint DNS
// resolution issues when running from a management cluster (HyperShift).
func createAWSClientLoadBalancer(ctx context.Context) (*elbv2.Client, error) {
	cfg, err := loadAWSConfig(ctx)
	if err != nil {
		return nil, err
	}

	customRetryer := retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = 10
		o.MaxBackoff = 30 * time.Second
	})

	return elbv2.NewFromConfig(cfg, func(o *elbv2.Options) {
		o.Retryer = customRetryer
		if cfg.Region != "" {
			o.BaseEndpoint = aws.String(fmt.Sprintf("https://elasticloadbalancing.%s.amazonaws.com", cfg.Region))
		}
	}), nil
}

// getAWSLoadBalancerFromDNSName finds a load balancer by DNS name using the AWS ELBv2 client.
func getAWSLoadBalancerFromDNSName(ctx context.Context, elbClient *elbv2.Client, lbDNSName string) (*elbv2types.LoadBalancer, error) {
	var foundLB *elbv2types.LoadBalancer
	framework.Logf("describing load balancers with DNS %s", lbDNSName)

	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 15*time.Minute, true, func(ctx context.Context) (bool, error) {
		paginator := elbv2.NewDescribeLoadBalancersPaginator(elbClient, &elbv2.DescribeLoadBalancersInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				framework.Logf("transient error describing load balancers (will retry): %v", err)
				return false, nil
			}

			framework.Logf("found %d load balancers in page", len(page.LoadBalancers))
			for i := range page.LoadBalancers {
				if aws.ToString(page.LoadBalancers[i].DNSName) == lbDNSName {
					foundLB = &page.LoadBalancers[i]
					framework.Logf("found load balancer with DNS %s", aws.ToString(foundLB.DNSName))
					return true, nil
				}
			}
		}
		return false, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to find load balancer with DNS name %s: %v", lbDNSName, err)
	}

	if foundLB == nil {
		return nil, fmt.Errorf("no load balancer found with DNS name: %s", lbDNSName)
	}

	return foundLB, nil
}

// findAWSLoadBalancerByDNSName performs a single-attempt lookup for a load balancer by DNS name.
// Returns nil (without error) if the load balancer is not found.
func findAWSLoadBalancerByDNSName(ctx context.Context, elbClient *elbv2.Client, lbDNSName string) (*elbv2types.LoadBalancer, error) {
	paginator := elbv2.NewDescribeLoadBalancersPaginator(elbClient, &elbv2.DescribeLoadBalancersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to describe load balancers: %v", err)
		}

		framework.Logf("found %d load balancers in page", len(page.LoadBalancers))
		for i := range page.LoadBalancers {
			if aws.ToString(page.LoadBalancers[i].DNSName) == lbDNSName {
				return &page.LoadBalancers[i], nil
			}
		}
	}
	return nil, nil
}

// isFeatureEnabled is a convenience wrapper around common.IsFeatureEnabled.
// Deprecated: Use common.IsFeatureEnabled directly instead.
func isFeatureEnabled(ctx context.Context, featureName string) (bool, error) {
	return common.IsFeatureEnabled(ctx, featureName)
}

// createAWSClientEC2 creates an AWS EC2 client using default credentials configured in the environment.
// It forces the public regional endpoint to avoid VPC private endpoint DNS
// resolution issues when running from a management cluster (HyperShift).
func createAWSClientEC2(ctx context.Context) (*ec2.Client, error) {
	cfg, err := loadAWSConfig(ctx)
	if err != nil {
		return nil, err
	}
	return ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		if cfg.Region != "" {
			o.BaseEndpoint = aws.String(fmt.Sprintf("https://ec2.%s.amazonaws.com", cfg.Region))
		}
	}), nil
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
