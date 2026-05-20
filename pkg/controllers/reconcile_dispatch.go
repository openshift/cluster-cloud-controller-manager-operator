package controllers

import (
	"context"
	"errors"
	"time"

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
