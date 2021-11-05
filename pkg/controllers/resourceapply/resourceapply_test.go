package resourceapply

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"

	appsclientv1 "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestApplyConfigMap(t *testing.T) {
	tests := []struct {
		name     string
		existing *corev1.ConfigMap
		input    *corev1.ConfigMap

		expectedModified bool
	}{
		{
			name: "create",
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
			},

			expectedModified: true,
		},
		{
			name: "skip on extra label",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
			},

			expectedModified: false,
		},
		{
			name: "update on missing label",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"new": "merge"}},
			},

			expectedModified: true,
		},
		{
			name: "update on mismatch data",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
				Data: map[string]string{
					"configmap": "value",
				},
			},

			expectedModified: true,
		},
		{
			name: "update on mismatch binary data",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Data: map[string]string{
					"configmap": "value",
				},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
				Data: map[string]string{
					"configmap": "value",
				},
				BinaryData: map[string][]byte{
					"binconfigmap": []byte("value"),
				},
			},

			expectedModified: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := fakeclient.NewClientBuilder().Build()
			if test.existing != nil {
				err := client.Create(context.TODO(), test.existing)
				if err != nil {
					t.Fatal(err)
				}
			}
			actualModified, err := applyConfigMap(context.TODO(), client, record.NewFakeRecorder(1000), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if test.expectedModified != actualModified {
				t.Errorf("expected %v, got %v", test.expectedModified, actualModified)
			}
		})
	}
}

func TestApplyDeployment(t *testing.T) {
	tests := []struct {
		name              string
		desiredDeployment *appsv1.Deployment
		actualDeployment  *appsv1.Deployment

		expectError        bool
		expectedUpdate     bool
		expectedDeployment *appsv1.Deployment
	}{
		{
			name:               "the deployment is created because it doesn't exist",
			desiredDeployment:  workloadDeployment(),
			expectedDeployment: workloadDeploymentWithDefaultSpecHash(),
			expectedUpdate:     true,
		},

		{
			name:               "the deployment already exists and it's up to date",
			desiredDeployment:  workloadDeployment(),
			actualDeployment:   workloadDeploymentWithDefaultSpecHash(),
			expectedDeployment: workloadDeploymentWithDefaultSpecHash(),
		},

		{
			name:              "the actual deployment was modified by a user and must be updated",
			desiredDeployment: workloadDeployment(),
			actualDeployment: func() *appsv1.Deployment {
				w := workloadDeploymentWithDefaultSpecHash()
				w.Generation = 2
				return w
			}(),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeploymentWithDefaultSpecHash()
				w.Generation = 3
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the deployment is updated due to a change in the spec",
			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment()
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			actualDeployment: workloadDeploymentWithDefaultSpecHash(),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeployment()
				w.Annotations["operator.openshift.io/spec-hash"] = "5322a9feed3671ec5e7bc72c86c9b7e2f628b00e9c7c8c4c93a48ee63e8db47a"
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the deployment is updated due to a change in Labels field",
			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment()
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			actualDeployment: workloadDeploymentWithDefaultSpecHash(),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeploymentWithDefaultSpecHash()
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the deployment is updated due to a change in Annotations field",
			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment()
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			actualDeployment: workloadDeploymentWithDefaultSpecHash(),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeploymentWithDefaultSpecHash()
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventRecorder := record.NewFakeRecorder(1000)
			client := fakeclient.NewClientBuilder().Build()
			if tt.actualDeployment != nil {
				err := client.Create(context.TODO(), tt.actualDeployment)
				if err != nil {
					t.Fatal(err)
				}
			}

			updated, err := applyDeployment(context.TODO(), client, eventRecorder, tt.desiredDeployment)
			if tt.expectError && err == nil {
				t.Fatal("expected to get an error")
			}
			if !tt.expectError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.expectedUpdate && !updated {
				t.Fatal("expected ApplyDeployment to report updated=true")
			}
			if !tt.expectedUpdate && updated {
				t.Fatal("expected ApplyDeployment to report updated=false")
			}

			updatedDeployment := &appsv1.Deployment{}
			err = client.Get(context.TODO(), appsclientv1.ObjectKeyFromObject(tt.desiredDeployment), updatedDeployment)
			if err != nil {
				t.Fatal(err)
			}

			if !equality.Semantic.DeepDerivative(tt.expectedDeployment.Spec, updatedDeployment.Spec) {
				t.Fatalf("Expected deployment: %+v, got %+v", tt.expectedDeployment, updatedDeployment)
			}
		})
	}
}

func workloadDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "apiserver",
			Namespace: "openshift-apiserver",
			Labels:    map[string]string{},
			Annotations: map[string]string{
				generationAnnotation: "1",
			},
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32Ptr(3),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{},
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "apiserver",
							Image: "docker-registry/img",
						},
					},
				},
			},
		},
	}
}

func workloadDeploymentWithDefaultSpecHash() *appsv1.Deployment {
	w := workloadDeployment()
	w.Annotations[specHashAnnotation] = "9ed89f9298716b3cde992326224f46d46e84042c41f9ff5820e7811f318be99e"
	return w
}

func TestApplyDaemonSet(t *testing.T) {
	tests := []struct {
		name             string
		desiredDaemonSet *appsv1.DaemonSet
		actualDaemonSet  *appsv1.DaemonSet

		expectError       bool
		expectedUpdate    bool
		expectedDaemonSet *appsv1.DaemonSet
	}{
		{
			name:              "the daemonset is created because it doesn't exist",
			desiredDaemonSet:  workloadDaemonSet(),
			expectedDaemonSet: workloadDaemonSetWithDefaultSpecHash(),
			expectedUpdate:    true,
		},

		{
			name:              "the daemonset already exists and it's up to date",
			desiredDaemonSet:  workloadDaemonSet(),
			actualDaemonSet:   workloadDaemonSetWithDefaultSpecHash(),
			expectedDaemonSet: workloadDaemonSetWithDefaultSpecHash(),
		},

		{
			name:             "the actual daemonset was modified by a user and must be updated",
			desiredDaemonSet: workloadDaemonSet(),
			actualDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSetWithDefaultSpecHash()
				w.Generation = 2
				return w
			}(),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSetWithDefaultSpecHash()
				w.Generation = 3
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the daemonset is updated due to a change in the spec",
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet()
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet()
				w.Annotations["operator.openshift.io/spec-hash"] = "5322a9feed3671ec5e7bc72c86c9b7e2f628b00e9c7c8c4c93a48ee63e8db47a"
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the daemonset is updated due to a change in Labels field",
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet()
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSetWithDefaultSpecHash()
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the daemonset is updated due to a change in Annotations field",
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet()
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSetWithDefaultSpecHash()
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventRecorder := record.NewFakeRecorder(1000)
			client := fakeclient.NewClientBuilder().Build()
			if tt.actualDaemonSet != nil {
				err := client.Create(context.TODO(), tt.actualDaemonSet)
				if err != nil {
					t.Fatal(err)
				}
			}

			updated, err := applyDaemonSet(context.TODO(), client, eventRecorder, tt.desiredDaemonSet)
			if tt.expectError && err == nil {
				t.Fatal("expected to get an error")
			}
			if !tt.expectError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.expectedUpdate && !updated {
				t.Fatal("expected ApplyDaemonSet to report updated=true")
			}
			if !tt.expectedUpdate && updated {
				t.Fatal("expected ApplyDaemonSet to report updated=false")
			}

			updatedDaemonSet := &appsv1.DaemonSet{}
			err = client.Get(context.TODO(), appsclientv1.ObjectKeyFromObject(tt.desiredDaemonSet), updatedDaemonSet)
			if err != nil {
				t.Fatal(err)
			}

			if !equality.Semantic.DeepDerivative(tt.expectedDaemonSet.Spec, updatedDaemonSet.Spec) {
				t.Fatalf("Expected DaemonSet: %+v, got %+v", tt.expectedDaemonSet, updatedDaemonSet)
			}
		})
	}
}

func workloadDaemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "apiserver",
			Namespace: "openshift-apiserver",
			Labels:    map[string]string{},
			Annotations: map[string]string{
				generationAnnotation: "1",
			},
			Generation: 1,
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{},
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "apiserver",
							Image: "docker-registry/img",
						},
					},
				},
			},
		},
	}
}

func workloadDaemonSetWithDefaultSpecHash() *appsv1.DaemonSet {
	w := workloadDaemonSet()
	w.Annotations[specHashAnnotation] = "ebe199e4e68c8ba52c7723e988895cde2ea804e8d0691d130e689f5168721bd1"
	return w
}
