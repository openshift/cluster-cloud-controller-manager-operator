package aws

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/ccm-aws-tests/e2e/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

// SDKManagedNLB holds all the AWS resources created for an SDK-managed NLB.
// Used for cleanup via DeferCleanup.
type SDKManagedNLB struct {
	NLBARN      string
	NLBDNS      string
	TGARN       string
	ListenerARN string
	NLBSGID     string // SG created for and attached to the NLB (for cleanup)
	SGID        string // Master SG that was modified (rule added)
	SGRuleID    string // ID of the ingress rule we added on master SG (for cleanup)
	VPC         string
	Subnets     []string
	InstanceIDs []string
	Port        int32
	InfraID     string
}

// ClusterInfra holds the discovered cluster infrastructure details needed to
// create an SDK-managed NLB that mirrors how the OCP installer provisions
// the KAS NLB.
type ClusterInfra struct {
	InfraID     string
	VPCID       string
	SubnetIDs   []string
	InstanceIDs []string
	MasterSGID  string
}

// SDKNLBCreateOpts configures optional SDK NLB creation parameters.
// Zero values use defaults matching existing HTTP-based tests.
type SDKNLBCreateOpts struct {
	HealthCheckProtocol elbv2types.ProtocolEnum // default HTTP
}

// discoverClusterInfra discovers VPC, subnets, master instance IDs, and master
// security group from the running cluster. Instance IDs come from K8s node
// spec.providerID (reliable, no EC2 tag assumptions). VPC, subnets, and SG
// come from EC2 DescribeInstances using those known instance IDs.
func discoverClusterInfra(ctx context.Context, cs clientset.Interface, ec2Client *ec2.Client) (*ClusterInfra, error) {
	// Get the infrastructure name from the Infrastructure CR.
	ocClient, err := common.GetOcClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create config client: %w", err)
	}
	infra, err := ocClient.Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Infrastructure CR: %w", err)
	}
	infraID := infra.Status.InfrastructureName
	if infraID == "" {
		return nil, fmt.Errorf("Infrastructure status.infrastructureName is empty")
	}
	framework.Logf("discovered infrastructure name: %s", infraID)

	// Get control-plane node instance IDs from K8s node spec.providerID.
	// providerID format: aws:///us-east-1a/i-0123456789abcdef0
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/control-plane",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list control-plane nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return nil, fmt.Errorf("no control-plane nodes found")
	}

	var instanceIDs []string
	for _, node := range nodes.Items {
		// Parse providerID: aws:///us-east-1a/i-xxxx → extract i-xxxx
		providerID := node.Spec.ProviderID
		parts := strings.Split(providerID, "/")
		if len(parts) < 2 {
			framework.Logf("skipping node %s with unexpected providerID: %s", node.Name, providerID)
			continue
		}
		instanceID := parts[len(parts)-1]
		if !strings.HasPrefix(instanceID, "i-") {
			framework.Logf("skipping node %s with non-EC2 instance ID: %s", node.Name, instanceID)
			continue
		}
		instanceIDs = append(instanceIDs, instanceID)
		framework.Logf("node %s → instance %s", node.Name, instanceID)
	}
	if len(instanceIDs) == 0 {
		return nil, fmt.Errorf("no EC2 instance IDs found from control-plane nodes")
	}

	// Use EC2 DescribeInstances with known instance IDs to get VPC, subnets, SG.
	result, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe instances %v: %w", instanceIDs, err)
	}

	ci := &ClusterInfra{
		InfraID:     infraID,
		InstanceIDs: instanceIDs,
	}

	seenSubnets := make(map[string]bool)
	for _, reservation := range result.Reservations {
		for _, inst := range reservation.Instances {
			// Capture VPC from the first instance.
			if ci.VPCID == "" {
				ci.VPCID = awssdk.ToString(inst.VpcId)
				framework.Logf("discovered VPC: %s", ci.VPCID)
			}

			// Collect unique subnets.
			subnetID := awssdk.ToString(inst.SubnetId)
			if !seenSubnets[subnetID] {
				seenSubnets[subnetID] = true
				ci.SubnetIDs = append(ci.SubnetIDs, subnetID)
			}

			// Find the master security group.
			if ci.MasterSGID == "" {
				for _, sg := range inst.SecurityGroups {
					sgName := awssdk.ToString(sg.GroupName)
					if strings.Contains(sgName, "-master-sg") || strings.Contains(sgName, "-controlplane") {
						ci.MasterSGID = awssdk.ToString(sg.GroupId)
						framework.Logf("discovered master SG: %s (%s)", ci.MasterSGID, sgName)
						break
					}
				}
				// Fallback: use the first SG from the first master instance.
				if ci.MasterSGID == "" && len(inst.SecurityGroups) > 0 {
					ci.MasterSGID = awssdk.ToString(inst.SecurityGroups[0].GroupId)
					framework.Logf("using first SG as master SG (fallback): %s", ci.MasterSGID)
				}
			}
		}
	}

	framework.Logf("discovered %d master instances: %v", len(ci.InstanceIDs), ci.InstanceIDs)
	framework.Logf("discovered %d subnets: %v", len(ci.SubnetIDs), ci.SubnetIDs)

	return ci, nil
}

// addSGIngressRule adds an inbound TCP rule on the given port from 0.0.0.0/0
// to the specified security group. It returns the security group rule ID for
// later cleanup.
func addSGIngressRule(ctx context.Context, ec2Client *ec2.Client, sgID string, port int32) (string, error) {
	framework.Logf("adding ingress rule to SG %s: TCP port %d from 0.0.0.0/0", sgID, port)

	input := &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: awssdk.String(sgID),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: awssdk.String("tcp"),
				FromPort:   awssdk.Int32(port),
				ToPort:     awssdk.Int32(port),
				IpRanges: []ec2types.IpRange{
					{
						CidrIp:      awssdk.String("0.0.0.0/0"),
						Description: awssdk.String(fmt.Sprintf("e2e-nlb-health-test port %d", port)),
					},
				},
			},
		},
	}

	result, err := ec2Client.AuthorizeSecurityGroupIngress(ctx, input)
	if err != nil {
		// Handle idempotency: if the rule already exists, find its ID
		// so we can still clean it up later.
		if strings.Contains(err.Error(), "InvalidPermission.Duplicate") {
			framework.Logf("SG rule for TCP %d already exists on %s, finding existing rule ID", port, sgID)
			ruleID, findErr := findSGRuleID(ctx, ec2Client, sgID, port)
			if findErr != nil {
				framework.Logf("warning: could not find existing rule ID: %v", findErr)
				return "", nil // Rule exists but we can't find the ID — skip cleanup
			}
			framework.Logf("found existing ingress rule %s on SG %s for TCP port %d", ruleID, sgID, port)
			return ruleID, nil
		}
		return "", fmt.Errorf("failed to add ingress rule to SG %s: %w", sgID, err)
	}

	var ruleID string
	if len(result.SecurityGroupRules) > 0 {
		ruleID = awssdk.ToString(result.SecurityGroupRules[0].SecurityGroupRuleId)
	}
	framework.Logf("added ingress rule %s to SG %s for TCP port %d", ruleID, sgID, port)
	return ruleID, nil
}

// findSGRuleID finds the rule ID of an existing inbound TCP rule on the
// given port. Used when the rule already exists (idempotent add).
func findSGRuleID(ctx context.Context, ec2Client *ec2.Client, sgID string, port int32) (string, error) {
	output, err := ec2Client.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []ec2types.Filter{
			{Name: awssdk.String("group-id"), Values: []string{sgID}},
		},
	})
	if err != nil {
		return "", err
	}
	for _, rule := range output.SecurityGroupRules {
		if !awssdk.ToBool(rule.IsEgress) &&
			awssdk.ToString(rule.IpProtocol) == "tcp" &&
			awssdk.ToInt32(rule.FromPort) == port &&
			awssdk.ToInt32(rule.ToPort) == port {
			return awssdk.ToString(rule.SecurityGroupRuleId), nil
		}
	}
	return "", fmt.Errorf("no matching rule found for TCP %d on SG %s", port, sgID)
}

// removeSGIngressRule removes the specified inbound rule from the security
// group. Best-effort: errors are logged but not returned.
func removeSGIngressRule(ctx context.Context, ec2Client *ec2.Client, sgID, ruleID string) {
	framework.Logf("removing ingress rule %s from SG %s", ruleID, sgID)

	input := &ec2.RevokeSecurityGroupIngressInput{
		GroupId:              awssdk.String(sgID),
		SecurityGroupRuleIds: []string{ruleID},
	}

	_, err := ec2Client.RevokeSecurityGroupIngress(ctx, input)
	if err != nil {
		framework.Logf("WARNING: failed to remove ingress rule %s from SG %s (best-effort): %v", ruleID, sgID, err)
		return
	}
	framework.Logf("removed ingress rule %s from SG %s", ruleID, sgID)
}

// createSDKManagedNLB creates an NLB, target group, and listener via the AWS
// SDK, replicating how the OCP installer provisions the KAS NLB.
func createSDKManagedNLB(ctx context.Context, elbClient *elbv2.Client, ec2Client *ec2.Client, infra *ClusterInfra, port int32, kasPatch *testKASPatch, opts ...SDKNLBCreateOpts) (*SDKManagedNLB, error) {
	hcProtocol := elbv2types.ProtocolEnumHttp
	if len(opts) > 0 && opts[0].HealthCheckProtocol != "" {
		hcProtocol = opts[0].HealthCheckProtocol
	}
	// Use the infra ID truncated to fit AWS 32-char name limit.
	// Strip trailing dashes to satisfy AWS naming regex: (?!.*-$)^[A-Za-z0-9-]+$
	shortID := infra.InfraID
	if len(shortID) > 16 {
		shortID = shortID[:16]
	}
	shortID = strings.TrimRight(shortID, "-")
	resourceName := fmt.Sprintf("e2e-ht-%s", shortID)

	nlb := &SDKManagedNLB{
		VPC:         infra.VPCID,
		Subnets:     infra.SubnetIDs,
		InstanceIDs: infra.InstanceIDs,
		Port:        port,
		InfraID:     infra.InfraID,
	}

	// 1. Create target group.
	framework.Logf("creating target group %s (port %d, hc=%s)", resourceName, port, hcProtocol)
	tgInput := &elbv2.CreateTargetGroupInput{
		Name:                       awssdk.String(resourceName),
		TargetType:                 elbv2types.TargetTypeEnumInstance,
		Protocol:                   elbv2types.ProtocolEnumTcp,
		Port:                       awssdk.Int32(port),
		VpcId:                      awssdk.String(infra.VPCID),
		HealthCheckEnabled:         awssdk.Bool(true),
		HealthCheckProtocol:        hcProtocol,
		HealthCheckPath:            awssdk.String("/readyz"),
		HealthCheckPort:            awssdk.String(fmt.Sprintf("%d", port)),
		HealthCheckIntervalSeconds: awssdk.Int32(10),
		HealthyThresholdCount:      awssdk.Int32(2),
		UnhealthyThresholdCount:    awssdk.Int32(2),
	}

	tgResult, err := elbClient.CreateTargetGroup(ctx, tgInput)
	if err != nil {
		return nlb, fmt.Errorf("failed to create target group: %w", err)
	}
	nlb.TGARN = awssdk.ToString(tgResult.TargetGroups[0].TargetGroupArn)
	framework.Logf("created target group: %s", nlb.TGARN)

	// TODO move set TG attribs here, before registration

	// 2. Register targets.
	targets := make([]elbv2types.TargetDescription, 0, len(infra.InstanceIDs))
	for _, id := range infra.InstanceIDs {
		targets = append(targets, elbv2types.TargetDescription{
			Id:   awssdk.String(id),
			Port: awssdk.Int32(port),
		})
	}

	regInput := &elbv2.RegisterTargetsInput{
		TargetGroupArn: awssdk.String(nlb.TGARN),
		Targets:        targets,
	}
	_, err = elbClient.RegisterTargets(ctx, regInput)
	if err != nil {
		return nlb, fmt.Errorf("failed to register targets: %w", err)
	}
	framework.Logf("registered %d targets in target group", len(targets))

	// 3. Create NLB security group (allows inbound TCP on the test port).
	// Idempotent: if the SG already exists (e.g. leftover from a previous failed
	// run that couldn't delete it due to DependencyViolation), look it up and
	// reuse it rather than failing.
	nlbSGName := resourceName + "-nlb-sg"
	framework.Logf("creating NLB security group %s in VPC %s", nlbSGName, infra.VPCID)
	sgResult, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   awssdk.String(nlbSGName),
		Description: awssdk.String(fmt.Sprintf("e2e NLB SG for port %d", port)),
		VpcId:       awssdk.String(infra.VPCID),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSecurityGroup,
			Tags: []ec2types.Tag{
				{Key: awssdk.String("kubernetes.io/cluster/" + infra.InfraID), Value: awssdk.String("owned")},
				{Key: awssdk.String("e2e-test"), Value: awssdk.String("health-transition")},
			},
		}},
	})
	if err != nil {
		if !strings.Contains(err.Error(), "InvalidGroup.Duplicate") {
			return nlb, fmt.Errorf("failed to create NLB security group: %w", err)
		}
		// SG left over from a previous run — look it up by name.
		framework.Logf("NLB SG %s already exists (leftover), looking up existing SG ID", nlbSGName)
		descResult, descErr := ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
			Filters: []ec2types.Filter{
				{Name: awssdk.String("group-name"), Values: []string{nlbSGName}},
				{Name: awssdk.String("vpc-id"), Values: []string{infra.VPCID}},
			},
		})
		if descErr != nil || len(descResult.SecurityGroups) == 0 {
			return nlb, fmt.Errorf("NLB SG %s already exists but could not look it up: %w", nlbSGName, err)
		}
		nlb.NLBSGID = awssdk.ToString(descResult.SecurityGroups[0].GroupId)
		framework.Logf("reusing existing NLB SG: %s", nlb.NLBSGID)
	} else {
		nlb.NLBSGID = awssdk.ToString(sgResult.GroupId)
		framework.Logf("created NLB SG: %s", nlb.NLBSGID)
	}

	// Allow inbound TCP on the test port. Idempotent: ignore duplicate rule errors.
	_, err = ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: awssdk.String(nlb.NLBSGID),
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: awssdk.String("tcp"),
			FromPort:   awssdk.Int32(port),
			ToPort:     awssdk.Int32(port),
			IpRanges: []ec2types.IpRange{{
				CidrIp:      awssdk.String("0.0.0.0/0"),
				Description: awssdk.String(fmt.Sprintf("e2e NLB inbound TCP %d", port)),
			}},
		}},
	})
	if err != nil && !strings.Contains(err.Error(), "InvalidPermission.Duplicate") {
		return nlb, fmt.Errorf("failed to add ingress rule to NLB SG: %w", err)
	}
	framework.Logf("added ingress rule to NLB SG %s: TCP %d from 0.0.0.0/0", nlb.NLBSGID, port)

	// 4. Create NLB (internal — matches KAS internal NLB and allows
	// in-cluster clients to reach it via VPC-internal DNS).
	framework.Logf("creating NLB %s (internal, subnets=%v)", resourceName, infra.SubnetIDs)
	lbInput := &elbv2.CreateLoadBalancerInput{
		Name:           awssdk.String(resourceName),
		Type:           elbv2types.LoadBalancerTypeEnumNetwork,
		Scheme:         elbv2types.LoadBalancerSchemeEnumInternal,
		Subnets:        infra.SubnetIDs,
		SecurityGroups: []string{nlb.NLBSGID},
		Tags: []elbv2types.Tag{
			{
				Key:   awssdk.String("kubernetes.io/cluster/" + infra.InfraID),
				Value: awssdk.String("owned"),
			},
			{
				Key:   awssdk.String("e2e-test"),
				Value: awssdk.String("health-transition"),
			},
		},
	}

	lbResult, err := elbClient.CreateLoadBalancer(ctx, lbInput)
	if err != nil {
		return nlb, fmt.Errorf("failed to create NLB: %w", err)
	}
	nlb.NLBARN = awssdk.ToString(lbResult.LoadBalancers[0].LoadBalancerArn)
	nlb.NLBDNS = awssdk.ToString(lbResult.LoadBalancers[0].DNSName)
	framework.Logf("created NLB: ARN=%s DNS=%s", nlb.NLBARN, nlb.NLBDNS)

	// 4. Wait for NLB to become active.
	if err := waitForNLBActive(ctx, elbClient, nlb.NLBARN, 5*time.Minute); err != nil {
		return nlb, fmt.Errorf("NLB did not become active: %w", err)
	}

	// 5. Enable cross-zone load balancing (AWS NLB default is off; real KAS internal
	// NLB and Service-based e2e tests use cross-zone enabled).
	if err := setNLBCrossZoneEnabled(ctx, elbClient, nlb.NLBARN, kasPatch.lbCrossZoneEnabled); err != nil {
		return nlb, fmt.Errorf("failed to enable cross-zone load balancing on NLB: %w", err)
	}

	// 6. Create listener.
	framework.Logf("creating listener on NLB (TCP port %d -> TG %s)", port, nlb.TGARN)
	listenerInput := &elbv2.CreateListenerInput{
		LoadBalancerArn: awssdk.String(nlb.NLBARN),
		Protocol:        elbv2types.ProtocolEnumTcp,
		Port:            awssdk.Int32(port),
		DefaultActions: []elbv2types.Action{
			{
				Type:           elbv2types.ActionTypeEnumForward,
				TargetGroupArn: awssdk.String(nlb.TGARN),
			},
		},
	}

	listenerResult, err := elbClient.CreateListener(ctx, listenerInput)
	if err != nil {
		return nlb, fmt.Errorf("failed to create listener: %w", err)
	}
	nlb.ListenerARN = awssdk.ToString(listenerResult.Listeners[0].ListenerArn)
	framework.Logf("created listener: %s", nlb.ListenerARN)

	return nlb, nil
}

// deleteSDKManagedNLB tears down all resources created by createSDKManagedNLB
// in the correct order. Best-effort: errors are logged but do not fail the test.
func deleteSDKManagedNLB(ctx context.Context, elbClient *elbv2.Client, ec2Client *ec2.Client, nlb *SDKManagedNLB) {
	// 1. Delete listener.
	if nlb.ListenerARN != "" {
		framework.Logf("deleting listener %s", nlb.ListenerARN)
		_, err := elbClient.DeleteListener(ctx, &elbv2.DeleteListenerInput{
			ListenerArn: awssdk.String(nlb.ListenerARN),
		})
		if err != nil {
			framework.Logf("WARNING: failed to delete listener %s (best-effort): %v", nlb.ListenerARN, err)
		} else {
			framework.Logf("deleted listener %s", nlb.ListenerARN)
		}
	}

	// 2. Delete NLB.
	if nlb.NLBARN != "" {
		framework.Logf("deleting NLB %s", nlb.NLBARN)
		_, err := elbClient.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{
			LoadBalancerArn: awssdk.String(nlb.NLBARN),
		})
		if err != nil {
			framework.Logf("WARNING: failed to delete NLB %s (best-effort): %v", nlb.NLBARN, err)
		} else {
			framework.Logf("deleted NLB %s", nlb.NLBARN)
		}

		// 3. Wait for NLB deletion to complete.
		waitForNLBDeleted(ctx, elbClient, nlb.NLBARN, 5*time.Minute)
	}

	// 4. Deregister targets.
	if nlb.TGARN != "" {
		targets := make([]elbv2types.TargetDescription, 0, len(nlb.InstanceIDs))
		for _, id := range nlb.InstanceIDs {
			targets = append(targets, elbv2types.TargetDescription{
				Id:   awssdk.String(id),
				Port: awssdk.Int32(nlb.Port),
			})
		}
		if len(targets) > 0 {
			framework.Logf("deregistering %d targets from TG %s", len(targets), nlb.TGARN)
			_, err := elbClient.DeregisterTargets(ctx, &elbv2.DeregisterTargetsInput{
				TargetGroupArn: awssdk.String(nlb.TGARN),
				Targets:        targets,
			})
			if err != nil {
				framework.Logf("WARNING: failed to deregister targets from TG %s (best-effort): %v", nlb.TGARN, err)
			} else {
				framework.Logf("deregistered targets from TG %s", nlb.TGARN)
			}
		}

		// 5. Delete target group.
		framework.Logf("deleting target group %s", nlb.TGARN)
		_, err := elbClient.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{
			TargetGroupArn: awssdk.String(nlb.TGARN),
		})
		if err != nil {
			framework.Logf("WARNING: failed to delete target group %s (best-effort): %v", nlb.TGARN, err)
		} else {
			framework.Logf("deleted target group %s", nlb.TGARN)
		}
	}

	// 6. Delete the NLB security group created by createSDKManagedNLB.
	// AWS releases the SG dependency asynchronously after NLB deletion, so
	// retry with backoff on DependencyViolation errors.
	if nlb.NLBSGID != "" && ec2Client != nil {
		framework.Logf("deleting NLB security group %s (with retry for dependency release)", nlb.NLBSGID)
		retryDelays := []int{5, 10, 15, 20, 30}
		for attempt, delaySec := range retryDelays {
			_, err := ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
				GroupId: awssdk.String(nlb.NLBSGID),
			})
			if err == nil {
				framework.Logf("deleted NLB security group %s (attempt %d)", nlb.NLBSGID, attempt+1)
				break
			}
			if strings.Contains(err.Error(), "DependencyViolation") && attempt < len(retryDelays)-1 {
				framework.Logf("SG %s still has dependents (attempt %d), retrying in %ds: %v",
					nlb.NLBSGID, attempt+1, delaySec, err)
				time.Sleep(time.Duration(delaySec) * time.Second)
				continue
			}
			framework.Logf("WARNING: failed to delete NLB SG %s after %d attempts (best-effort): %v",
				nlb.NLBSGID, attempt+1, err)
			break
		}
	}
}

// waitForNLBActive polls DescribeLoadBalancers until the NLB state is "active"
// or the timeout is reached.
func waitForNLBActive(ctx context.Context, elbClient *elbv2.Client, nlbARN string, timeout time.Duration) error {
	framework.Logf("waiting for NLB %s to become active (timeout %s)", nlbARN, timeout)

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for NLB %s to become active after %s", nlbARN, timeout)
		}

		result, err := elbClient.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{
			LoadBalancerArns: []string{nlbARN},
		})
		if err != nil {
			framework.Logf("transient error describing NLB %s (will retry): %v", nlbARN, err)
			time.Sleep(10 * time.Second)
			continue
		}

		if len(result.LoadBalancers) > 0 {
			state := result.LoadBalancers[0].State
			if state != nil {
				framework.Logf("NLB %s state: %s", nlbARN, state.Code)
				if state.Code == elbv2types.LoadBalancerStateEnumActive {
					framework.Logf("NLB %s is active", nlbARN)
					return nil
				}
			}
		}

		time.Sleep(10 * time.Second)
	}
}

// waitForNLBDeleted polls DescribeLoadBalancers until the NLB is gone (404)
// or the timeout is reached. Best-effort: errors are logged but not returned.
func waitForNLBDeleted(ctx context.Context, elbClient *elbv2.Client, nlbARN string, timeout time.Duration) {
	framework.Logf("waiting for NLB %s to be deleted (timeout %s)", nlbARN, timeout)

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			framework.Logf("WARNING: timed out waiting for NLB %s deletion after %s", nlbARN, timeout)
			return
		}

		result, err := elbClient.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{
			LoadBalancerArns: []string{nlbARN},
		})
		if err != nil {
			// A "not found" error means the NLB has been deleted.
			if strings.Contains(err.Error(), "LoadBalancerNotFound") {
				framework.Logf("NLB %s has been deleted", nlbARN)
				return
			}
			framework.Logf("transient error describing NLB %s (will retry): %v", nlbARN, err)
			time.Sleep(10 * time.Second)
			continue
		}

		if len(result.LoadBalancers) == 0 {
			framework.Logf("NLB %s has been deleted", nlbARN)
			return
		}

		framework.Logf("NLB %s still exists, waiting for deletion...", nlbARN)
		time.Sleep(10 * time.Second)
	}
}

// setNLBCrossZoneEnabled sets load_balancing.cross_zone.enabled on the NLB.
// TG attribute load_balancing.cross_zone.enabled defaults to
// use_load_balancer_configuration, so enabling it on the NLB is sufficient.
func setNLBCrossZoneEnabled(ctx context.Context, elbClient *elbv2.Client, nlbARN string, enabled bool) error {
	val := "false"
	if enabled {
		val = "true"
	}
	framework.Logf("setting NLB %s load_balancing.cross_zone.enabled=%s", nlbARN, val)
	_, err := elbClient.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: awssdk.String(nlbARN),
		Attributes: []elbv2types.LoadBalancerAttribute{
			{Key: awssdk.String("load_balancing.cross_zone.enabled"), Value: awssdk.String(val)},
		},
	})
	if err != nil {
		return fmt.Errorf("ModifyLoadBalancerAttributes cross_zone=%s: %w", val, err)
	}
	return nil
}

// setTGPreserveClientIP sets the preserve_client_ip.enabled attribute on a
// target group. Pass enabled=false to disable source-IP stickiness so the NLB
// distributes connections across targets independent of the client IP.
func setTGPreserveClientIP(ctx context.Context, elbClient *elbv2.Client, tgARN string, enabled bool) error {
	val := "true"
	if !enabled {
		val = "false"
	}
	framework.Logf("setting TG %s preserve_client_ip.enabled=%s", tgARN, val)
	_, err := elbClient.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: awssdk.String(tgARN),
		Attributes: []elbv2types.TargetGroupAttribute{
			{Key: awssdk.String("preserve_client_ip.enabled"), Value: awssdk.String(val)},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to set preserve_client_ip.enabled=%s on TG %s: %w", val, tgARN, err)
	}
	framework.Logf("TG %s preserve_client_ip.enabled=%s set", tgARN, val)
	return err
}

// setTGKASAttributes configures the target group to match the real KAS
// (kube-apiserver) NLB target group attributes. The key differences from
// AWS defaults are:
//   - connection_termination=false: NLB does NOT immediately terminate
//     connections to unhealthy targets (default: true).
//   - draining_interval=300s: NLB drains unhealthy targets for up to 300s
//     before stopping traffic (default: 0).
//   - deregistration_delay=300s with connection_termination=false.
//   - preserve_client_ip is configurable (KAS default: false).
func setTGKASAttributes(ctx context.Context, elbClient *elbv2.Client, tgARN string, kasPatch *testKASPatch) error {
	if kasPatch == nil {
		return fmt.Errorf("kasPatch is required")
	}
	preserveCIP := strconv.FormatBool(kasPatch.tgPreserveClientIPEnabled)
	connTerm := strconv.FormatBool(kasPatch.tgHealthStateUnhealthyConnTermEnabled)
	drainInterval := strconv.Itoa(kasPatch.tgHealthStateUnhealthyDrainIntervalSec)
	deregDelay := strconv.Itoa(kasPatch.tgDesregDelayTimeoutSec)
	deregConnTerm := strconv.FormatBool(kasPatch.tgDesregConnectTermEnabled)

	attrs := []elbv2types.TargetGroupAttribute{
		{Key: awssdk.String("preserve_client_ip.enabled"), Value: awssdk.String(preserveCIP)},
		{Key: awssdk.String("target_health_state.unhealthy.connection_termination.enabled"), Value: awssdk.String(connTerm)},
		{Key: awssdk.String("target_health_state.unhealthy.draining_interval_seconds"), Value: awssdk.String(drainInterval)},
		{Key: awssdk.String("deregistration_delay.timeout_seconds"), Value: awssdk.String(deregDelay)},
		{Key: awssdk.String("deregistration_delay.connection_termination.enabled"), Value: awssdk.String(deregConnTerm)},
		{Key: awssdk.String("stickiness.enabled"), Value: awssdk.String("false")},
	}

	framework.Logf(
		"setting TG %s to KAS-equivalent attributes (preserve_client_ip=%s, conn_term=%s, draining=%ss, dereg_delay=%ss)",
		tgARN, preserveCIP, connTerm, drainInterval, deregDelay,
	)
	for _, a := range attrs {
		framework.Logf("  %s=%s", *a.Key, *a.Value)
	}

	_, err := elbClient.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: awssdk.String(tgARN),
		Attributes:     attrs,
	})
	if err != nil {
		return fmt.Errorf("failed to set KAS attributes on TG %s: %w", tgARN, err)
	}
	framework.Logf("TG %s KAS attributes applied", tgARN)
	return nil
}
