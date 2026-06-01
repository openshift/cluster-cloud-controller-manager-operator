package controllers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// degradedSetter binds ctx to setCondition, returning a no-arg func() error
// suitable for use as the setDegraded callback in handleTerminal and handleTransient.
func degradedSetter(ctx context.Context, setCondition func(context.Context) error) func() error {
	return func() error { return setCondition(ctx) }
}

// finalizeReconcile is the deferred dispatcher shared by CloudConfigReconciler and
// TrustedCABundleReconciler. Terminal errors degrade immediately; transient errors
// degrade only after threshold; nil errors invoke clearFn.
func finalizeReconcile(
	fw *failureWindow,
	clk clock.PassiveClock,
	staleAfter, threshold time.Duration,
	name string,
	clearFn func(),
	setDegraded func() error,
	result *ctrl.Result,
	retErr *error,
) {
	if *retErr == nil {
		clearFn()
		return
	}
	if errors.Is(*retErr, reconcile.TerminalError(nil)) {
		*result, *retErr = fw.handleTerminal(name, *retErr, setDegraded)
	} else {
		*result, *retErr = fw.handleTransient(clk.Now(), staleAfter, threshold, name, *retErr, setDegraded)
	}
}

// failureWindow tracks consecutive transient failures. All methods are safe for concurrent use.
type failureWindow struct {
	mu                      sync.Mutex
	consecutiveFailureSince *time.Time
	lastTransientFailureAt  *time.Time
}

// clear resets the failure window. Call this on every successful reconcile.
func (fw *failureWindow) clear() {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	fw.consecutiveFailureSince = nil
	fw.lastTransientFailureAt = nil
}

// observe records a transient failure at now and returns the elapsed time since
// the window started plus a boolean indicating whether the window was just opened
// or restarted. staleAfter controls stale-window detection: if the gap since the
// last observed failure exceeds staleAfter, the window restarts. Pass 0 to disable.
func (fw *failureWindow) observe(now time.Time, staleAfter time.Duration) (elapsed time.Duration, started bool) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	stale := staleAfter > 0 && fw.lastTransientFailureAt != nil && now.Sub(*fw.lastTransientFailureAt) > staleAfter
	if fw.consecutiveFailureSince == nil || stale {
		fw.consecutiveFailureSince = &now
		fw.lastTransientFailureAt = &now
		return 0, true
	}
	fw.lastTransientFailureAt = &now
	return now.Sub(*fw.consecutiveFailureSince), false
}

// handleTransient records a transient failure and degrades only after threshold has elapsed.
// name labels log messages. staleAfter controls stale-window restart (pass 0 to disable).
// setDegraded is invoked only when the threshold is exceeded.
// Always returns a non-nil error so controller-runtime requeues with exponential backoff.
func (fw *failureWindow) handleTransient(now time.Time, staleAfter, threshold time.Duration, name string, err error, setDegraded func() error) (ctrl.Result, error) {
	elapsed, started := fw.observe(now, staleAfter)
	if started {
		klog.V(4).Infof("%s: transient failure started (%v), will degrade after %s", name, err, threshold)
		return ctrl.Result{}, err
	}
	if elapsed < threshold {
		klog.V(4).Infof("%s: transient failure ongoing for %s (threshold %s): %v", name, elapsed, threshold, err)
		return ctrl.Result{}, err
	}
	klog.Warningf("%s: transient failure exceeded threshold (%s), setting degraded condition for controller: %v", name, elapsed, err)
	if setErr := setDegraded(); setErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set degraded condition: %w", setErr)
	}
	return ctrl.Result{}, err
}
