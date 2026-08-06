package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Observer polls the AWS DescribeTargetHealth API at a configurable interval,
// recording state transitions per target and full-state snapshots per poll.
type Observer struct {
	elbClient  *elbv2.Client
	tgARN      string
	targetType string
	interval   time.Duration

	mu        sync.Mutex
	events    []HealthEvent
	snapshots []TargetSnapshot
	lastState map[string]string

	cancel context.CancelFunc
}

// NewObserver creates an Observer that polls target health at the given interval.
func NewObserver(elbClient *elbv2.Client, interval time.Duration) *Observer {
	return &Observer{
		elbClient: elbClient,
		interval:  interval,
		lastState: make(map[string]string),
	}
}

// DiscoverTargetGroup finds the first target group associated with the given
// NLB ARN and records its ARN and target type.
func (o *Observer) DiscoverTargetGroup(ctx context.Context, lbARN string) error {
	output, err := o.elbClient.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
		LoadBalancerArn: aws.String(lbARN),
	})
	if err != nil {
		return fmt.Errorf("describe target groups: %w", err)
	}
	if len(output.TargetGroups) == 0 {
		return fmt.Errorf("no target groups for LB %s", lbARN)
	}
	o.tgARN = aws.ToString(output.TargetGroups[0].TargetGroupArn)
	o.targetType = string(output.TargetGroups[0].TargetType)
	return nil
}

// TargetGroupARN returns the discovered target group ARN.
func (o *Observer) TargetGroupARN() string { return o.tgARN }

// TargetType returns the target type (instance, ip, lambda, alb).
func (o *Observer) TargetType() string { return o.targetType }

// PollOnce performs a single DescribeTargetHealth call and returns a snapshot.
// This can be called independently of Start/Stop — useful during setup when
// the background polling loop is not yet running.
func (o *Observer) PollOnce(ctx context.Context) (TargetSnapshot, error) {
	output, err := o.elbClient.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(o.tgARN),
	})
	if err != nil {
		return TargetSnapshot{}, fmt.Errorf("describe target health: %w", err)
	}

	snap := TargetSnapshot{
		Timestamp: time.Now(),
		Targets:   make(map[string]string, len(output.TargetHealthDescriptions)),
	}
	for _, d := range output.TargetHealthDescriptions {
		id := aws.ToString(d.Target.Id)
		state := string(d.TargetHealth.State)
		snap.Targets[id] = state
		switch d.TargetHealth.State {
		case elbv2types.TargetHealthStateEnumHealthy:
			snap.HealthyCount++
		case elbv2types.TargetHealthStateEnumUnhealthy, elbv2types.TargetHealthStateEnumUnhealthyDraining:
			snap.UnhealthyCount++
		case elbv2types.TargetHealthStateEnumInitial:
			snap.InitialCount++
		case elbv2types.TargetHealthStateEnumDraining:
			snap.DrainingCount++
		}
	}
	return snap, nil
}

// TGAttribute is a key-value pair from DescribeTargetGroupAttributes.
type TGAttribute struct {
	Key   string
	Value string
}

// DescribeTGAttributes returns the target group attributes for the discovered TG.
func (o *Observer) DescribeTGAttributes(ctx context.Context) ([]TGAttribute, error) {
	output, err := o.elbClient.DescribeTargetGroupAttributes(ctx, &elbv2.DescribeTargetGroupAttributesInput{
		TargetGroupArn: aws.String(o.tgARN),
	})
	if err != nil {
		return nil, fmt.Errorf("describe TG attributes: %w", err)
	}
	attrs := make([]TGAttribute, 0, len(output.Attributes))
	for _, a := range output.Attributes {
		attrs = append(attrs, TGAttribute{
			Key:   aws.ToString(a.Key),
			Value: aws.ToString(a.Value),
		})
	}
	return attrs, nil
}

// ModifyTGAttributes sets target group attributes via the AWS API.
// Used to apply TG configuration variations (e.g., CAPA fix attributes)
// after the TG is created by the cloud controller.
func (o *Observer) ModifyTGAttributes(ctx context.Context, attrs map[string]string) error {
	var kv []elbv2types.TargetGroupAttribute
	for k, v := range attrs {
		kv = append(kv, elbv2types.TargetGroupAttribute{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}
	_, err := o.elbClient.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: aws.String(o.tgARN),
		Attributes:     kv,
	})
	if err != nil {
		return fmt.Errorf("modify TG attributes: %w", err)
	}
	return nil
}

// WaitForAllHealthy blocks until at least minHealthy targets report
// TargetHealthStateEnumHealthy, or the timeout is reached.
func (o *Observer) WaitForAllHealthy(ctx context.Context, minHealthy int, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, o.interval, timeout, true, func(ctx context.Context) (bool, error) {
		output, err := o.elbClient.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(o.tgARN),
		})
		if err != nil {
			return false, nil
		}
		healthy := 0
		for _, d := range output.TargetHealthDescriptions {
			if d.TargetHealth.State == elbv2types.TargetHealthStateEnumHealthy {
				healthy++
			}
		}
		return healthy >= minHealthy, nil
	})
}

// Start begins polling DescribeTargetHealth in a background goroutine.
func (o *Observer) Start(ctx context.Context) {
	ctx, o.cancel = context.WithCancel(ctx)
	go o.pollLoop(ctx)
}

// Stop cancels the background polling goroutine.
func (o *Observer) Stop() {
	if o.cancel != nil {
		o.cancel()
	}
}

// Events returns a copy of all recorded health state transition events.
func (o *Observer) Events() []HealthEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]HealthEvent, len(o.events))
	copy(result, o.events)
	return result
}

// Snapshots returns a copy of all per-poll full-state snapshots.
func (o *Observer) Snapshots() []TargetSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]TargetSnapshot, len(o.snapshots))
	copy(result, o.snapshots)
	return result
}

func (o *Observer) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.pollOnce(ctx)
		}
	}
}

func (o *Observer) pollOnce(ctx context.Context) {
	output, err := o.elbClient.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(o.tgARN),
	})
	if err != nil {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()

	snap := TargetSnapshot{
		Timestamp: now,
		Targets:   make(map[string]string, len(output.TargetHealthDescriptions)),
	}

	for _, d := range output.TargetHealthDescriptions {
		id := aws.ToString(d.Target.Id)
		port := aws.ToInt32(d.Target.Port)
		state := string(d.TargetHealth.State)
		reason := string(d.TargetHealth.Reason)

		snap.Targets[id] = state
		switch d.TargetHealth.State {
		case elbv2types.TargetHealthStateEnumHealthy:
			snap.HealthyCount++
		case elbv2types.TargetHealthStateEnumUnhealthy, elbv2types.TargetHealthStateEnumUnhealthyDraining:
			snap.UnhealthyCount++
		case elbv2types.TargetHealthStateEnumInitial:
			snap.InitialCount++
		case elbv2types.TargetHealthStateEnumDraining:
			snap.DrainingCount++
		}

		prev := o.lastState[id]
		if state != prev {
			o.events = append(o.events, HealthEvent{
				Timestamp:  now,
				TargetID:   id,
				TargetPort: port,
				State:      state,
				PrevState:  prev,
				Reason:     reason,
			})
			o.lastState[id] = state
		}
	}

	o.snapshots = append(o.snapshots, snap)
}
