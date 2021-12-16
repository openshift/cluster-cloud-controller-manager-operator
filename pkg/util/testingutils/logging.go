package testingutils

import (
	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
)

// TurnOffKlog supresses klog. Intended to use only in tests.
func TurnOffKlog() {
	klog.SetLogger(logr.New(&EmptyLogger{}))
}

func TurnOnKlog() {
	klog.Flush()
	klog.SetLogger(klogr.New())
}

// EmptyLogger implements logr.Logging
type EmptyLogger struct{}

// Enabled always returns false
func (e *EmptyLogger) Init(info logr.RuntimeInfo) {}

// Enabled always returns false
func (e *EmptyLogger) Enabled(level int) bool {
	return false
}

// Info does nothing
func (e *EmptyLogger) Info(level int, msg string, keysAndValues ...interface{}) {}

// Error does nothing
func (e *EmptyLogger) Error(err error, msg string, keysAndValues ...interface{}) {}

// V returns itself
func (e *EmptyLogger) V(level int) logr.LogSink {
	return e
}

// WithValues returns itself
func (e *EmptyLogger) WithValues(keysAndValues ...interface{}) logr.LogSink {
	return e
}

// WithName returns itself
func (e *EmptyLogger) WithName(name string) logr.LogSink {
	return e
}
