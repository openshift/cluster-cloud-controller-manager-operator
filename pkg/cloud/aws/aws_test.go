package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

func TestResourcesRenderingSmoke(t *testing.T) {

	tc := []struct {
		name       string
		config     config.OperatorConfig
		initErrMsg string
	}{
		{
			name:       "Empty config",
			config:     config.OperatorConfig{},
			initErrMsg: "aws: missed images in config: CloudControllerManager: non zero value required",
		}, {
			name: "Minimal allowed config",
			config: config.OperatorConfig{
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAWS: "CloudControllerManagerAws",
				},
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType},
			},
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assets, err := NewProviderAssets(tc.config)
			if tc.initErrMsg != "" {
				assert.EqualError(t, err, tc.initErrMsg)
				return
			} else {
				assert.NoError(t, err)
			}

			resources := assets.GetRenderedResources()
			assert.Len(t, resources, 3)

			deploy, ok := resources[0].(*appsv1.Deployment)
			require.True(t, ok, "first resource should be a Deployment")

			container := deploy.Spec.Template.Spec.Containers[0]

			assert.Contains(t, container.Env, corev1.EnvVar{
				Name:  "AWS_SHARED_CREDENTIALS_FILE",
				Value: "/etc/aws-credentials/credentials",
			})

			assert.Contains(t, container.VolumeMounts, corev1.VolumeMount{
				Name:      "aws-credentials",
				MountPath: "/etc/aws-credentials",
				ReadOnly:  true,
			})
			assert.Contains(t, container.VolumeMounts, corev1.VolumeMount{
				Name:      "bound-sa-token",
				MountPath: "/var/run/secrets/openshift/serviceaccount",
				ReadOnly:  true,
			})

			volumeNames := make([]string, 0, len(deploy.Spec.Template.Spec.Volumes))
			for _, v := range deploy.Spec.Template.Spec.Volumes {
				volumeNames = append(volumeNames, v.Name)
			}
			assert.Contains(t, volumeNames, "aws-credentials")
			assert.Contains(t, volumeNames, "bound-sa-token")
		})
	}
}
