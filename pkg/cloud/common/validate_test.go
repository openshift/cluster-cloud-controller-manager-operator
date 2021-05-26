package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestSupportedType(t *testing.T) {
	tc := []struct {
		name          string
		object        interface{}
		expectedError string
	}{{
		name:   "Support Deployment type",
		object: appsv1.Deployment{},
	}, {
		name:   "Support ConfigMap type",
		object: corev1.ConfigMap{},
	}, {
		name:          "Incorrect resource type DaemonSet should not be unmarshalled",
		object:        appsv1.DaemonSet{},
		expectedError: "unsupported resource type for apply: v1.DaemonSet",
	}, {
		name:          "Incorrect resource type should not be unmarshalled",
		object:        corev1.PersistentVolume{},
		expectedError: "unsupported resource type for apply: v1.PersistentVolume",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			err := supportedType(tc.object, "")
			if tc.expectedError != "" {
				assert.EqualError(t, err, tc.expectedError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
