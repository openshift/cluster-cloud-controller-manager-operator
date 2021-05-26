package common

import (
	"fmt"

	"gopkg.in/validator.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

func init() {
	err := validator.SetValidationFunc("supportedType", supportedType)
	utilruntime.Must(err)
}

// supportedType will validate the type of Object is supported
func supportedType(object interface{}, _ string) error {
	switch object.(type) {
	case appsv1.Deployment, corev1.ConfigMap:
		return nil
	default:
		return fmt.Errorf("unsupported resource type for apply: %T", object)
	}
}
