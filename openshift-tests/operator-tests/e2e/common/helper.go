package common

import (
	ginkgov2 "github.com/onsi/ginkgo/v2"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"
)

// NewClientConfigForTest returns a config configured to connect to the api server
func NewClientConfigForTest() (*rest.Config, error) {
	config, err := framework.LoadConfig()
	if err == nil {
		ginkgov2.GinkgoLogr.Info("Found configuration for", "host", config.Host)
	}
	return config, err
}
