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
// recording state transitions per target. It discovers the target group ARN
// from the load balancer ARN.
type Observer struct {
	elbClient  *elbv2.Client
	tgARN      string
	targetType string
	interval   time.Duration

	mu        sync.Mutex
	events    []HealthEvent
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
	for _, d := range output.TargetHealthDescriptions {
		id := aws.ToString(d.Target.Id)
		port := aws.ToInt32(d.Target.Port)
		state := string(d.TargetHealth.State)
		reason := string(d.TargetHealth.Reason)

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
}
