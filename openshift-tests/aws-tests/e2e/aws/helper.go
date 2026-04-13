package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	ec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/aws-tests/e2e/common"
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

// isFeatureEnabled is a convenience wrapper around common.IsFeatureEnabled.
// Deprecated: Use common.IsFeatureEnabled directly instead.
func isFeatureEnabled(ctx context.Context, featureName string) (bool, error) {
	return common.IsFeatureEnabled(ctx, featureName)
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
