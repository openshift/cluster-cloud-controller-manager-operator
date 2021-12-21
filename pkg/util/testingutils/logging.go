package testingutils

import (
	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

// TurnOffKlog supresses klog. Intended to use only in tests.
func TurnOffKlog() {
	klog.SetLogger(logr.Discard())
}

// TurnOnKlog remove the logger and to set back to defaults
func TurnOnKlog() {
	klog.ClearLogger()
}
