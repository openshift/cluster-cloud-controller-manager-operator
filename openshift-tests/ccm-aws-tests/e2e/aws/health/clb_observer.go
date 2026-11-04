package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	"k8s.io/apimachinery/pkg/util/wait"
)

// CLBObserver polls the Classic Load Balancer DescribeInstanceHealth API,
// recording state transitions per instance. CLB uses ELB v1 API with
// simpler health states: InService, OutOfService, Unknown.
type CLBObserver struct {
	elbClient *elb.Client
	lbName    string
	interval  time.Duration

	mu        sync.Mutex
	events    []HealthEvent
	snapshots []TargetSnapshot
	lastState map[string]string

	cancel context.CancelFunc
}

// NewCLBObserver creates a CLB observer that polls instance health.
func NewCLBObserver(elbClient *elb.Client, lbName string, interval time.Duration) *CLBObserver {
	return &CLBObserver{
		elbClient: elbClient,
		lbName:    lbName,
		interval:  interval,
		lastState: make(map[string]string),
	}
}

// LBName returns the CLB name.
func (o *CLBObserver) LBName() string { return o.lbName }

// mapCLBState maps CLB instance states to the common health state names
// used by the report (matching NLB terminology for consistent comparison).
func mapCLBState(clbState string) string {
	switch clbState {
	case "InService":
		return "healthy"
	case "OutOfService":
		return "unhealthy"
	case "Unknown":
		return "initial"
	default:
		return clbState
	}
}

// WaitForAllHealthy blocks until all registered instances report InService.
func (o *CLBObserver) WaitForAllHealthy(ctx context.Context, minHealthy int, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, o.interval, timeout, true, func(ctx context.Context) (bool, error) {
		output, err := o.elbClient.DescribeInstanceHealth(ctx, &elb.DescribeInstanceHealthInput{
			LoadBalancerName: aws.String(o.lbName),
		})
		if err != nil {
			return false, nil
		}
		healthy := 0
		for _, is := range output.InstanceStates {
			if aws.ToString(is.State) == "InService" {
				healthy++
			}
		}
		return healthy >= minHealthy, nil
	})
}

// PollOnce performs a single DescribeInstanceHealth call and returns a
// TargetSnapshot (same format as NLB observer for consistent reporting).
func (o *CLBObserver) PollOnce(ctx context.Context) (TargetSnapshot, error) {
	output, err := o.elbClient.DescribeInstanceHealth(ctx, &elb.DescribeInstanceHealthInput{
		LoadBalancerName: aws.String(o.lbName),
	})
	if err != nil {
		return TargetSnapshot{}, fmt.Errorf("describe instance health: %w", err)
	}

	snap := TargetSnapshot{
		Timestamp: time.Now(),
		Targets:   make(map[string]string, len(output.InstanceStates)),
	}
	for _, is := range output.InstanceStates {
		id := aws.ToString(is.InstanceId)
		rawState := aws.ToString(is.State)
		state := mapCLBState(rawState)
		snap.Targets[id] = state
		switch state {
		case "healthy":
			snap.HealthyCount++
		case "unhealthy":
			snap.UnhealthyCount++
		case "initial":
			snap.InitialCount++
		}
	}
	return snap, nil
}

// Start begins polling DescribeInstanceHealth in a background goroutine.
func (o *CLBObserver) Start(ctx context.Context) {
	ctx, o.cancel = context.WithCancel(ctx)
	go o.pollLoop(ctx)
}

// Stop cancels the background polling goroutine.
func (o *CLBObserver) Stop() {
	if o.cancel != nil {
		o.cancel()
	}
}

// Events returns a copy of all recorded health state transition events.
func (o *CLBObserver) Events() []HealthEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]HealthEvent, len(o.events))
	copy(result, o.events)
	return result
}

// Snapshots returns a copy of all per-poll full-state snapshots.
func (o *CLBObserver) Snapshots() []TargetSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]TargetSnapshot, len(o.snapshots))
	copy(result, o.snapshots)
	return result
}

func (o *CLBObserver) pollLoop(ctx context.Context) {
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

func (o *CLBObserver) pollOnce(ctx context.Context) {
	output, err := o.elbClient.DescribeInstanceHealth(ctx, &elb.DescribeInstanceHealthInput{
		LoadBalancerName: aws.String(o.lbName),
	})
	if err != nil {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()

	snap := TargetSnapshot{
		Timestamp: now,
		Targets:   make(map[string]string, len(output.InstanceStates)),
	}

	for _, is := range output.InstanceStates {
		id := aws.ToString(is.InstanceId)
		rawState := aws.ToString(is.State)
		state := mapCLBState(rawState)

		snap.Targets[id] = state
		switch state {
		case "healthy":
			snap.HealthyCount++
		case "unhealthy":
			snap.UnhealthyCount++
		case "initial":
			snap.InitialCount++
		}

		prev := o.lastState[id]
		if state != prev {
			o.events = append(o.events, HealthEvent{
				Timestamp:  now,
				TargetID:   id,
				TargetPort: 0, // CLB doesn't report port per instance
				State:      state,
				PrevState:  prev,
				Reason:     rawState, // Keep original CLB state as reason
			})
			o.lastState[id] = state
		}
	}

	o.snapshots = append(o.snapshots, snap)
}
