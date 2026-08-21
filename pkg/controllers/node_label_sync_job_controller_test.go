package controllers

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testNodeLabelSyncNamespace = "test-node-label-sync"
	testNodeLabelSyncImage     = "quay.io/example/test-node-label-sync-job:latest"
	testNodeLabelSyncVersion   = "1.2.3-test"
)

// deleteJob deletes the Job and waits for it to disappear. envtest has no job controller
// running to remove the batch.kubernetes.io/job-tracking finalizer that the apiserver adds on
// create, so a plain Delete would otherwise leave the object stuck terminating forever and
// block the next test from creating a Job with the same name.
func deleteJob(ctx context.Context, key client.ObjectKey) {
	job := &batchv1.Job{}
	if err := cl.Get(ctx, key, job); apierrors.IsNotFound(err) {
		return
	}
	_ = cl.Delete(ctx, job)

	Eventually(func() error {
		j := &batchv1.Job{}
		if err := cl.Get(ctx, key, j); err != nil {
			return err
		}
		if len(j.Finalizers) > 0 {
			j.Finalizers = nil
			_ = cl.Update(ctx, j)
		}
		return fmt.Errorf("job %s still exists", key)
	}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))
}

var _ = Describe("NodeLabelSyncJobReconciler", func() {
	ctx := context.Background()

	jobKey := client.ObjectKey{Name: nodeLabelSyncJobName, Namespace: testNodeLabelSyncNamespace}

	newReconciler := func(enabled, disabled []configv1.FeatureGateName) *NodeLabelSyncJobReconciler {
		return &NodeLabelSyncJobReconciler{
			Client:            cl,
			Namespace:         testNodeLabelSyncNamespace,
			Image:             testNodeLabelSyncImage,
			ReleaseVersion:    testNodeLabelSyncVersion,
			FeatureGateAccess: featuregates.NewHardcodedFeatureGateAccessForTesting(enabled, disabled, nil, nil),
		}
	}

	createInfra := func(platform configv1.PlatformType) {
		infra := makeInfrastructureResource(platform)
		Expect(cl.Create(ctx, infra)).To(Succeed())
		infra.Status = makeInfraStatus(platform)
		Expect(cl.Status().Update(ctx, infra)).To(Succeed())
	}

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNodeLabelSyncNamespace}}
		if err := cl.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	AfterEach(func() {
		infra := &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: infrastructureResourceName}}
		_ = cl.Delete(ctx, infra)
		Eventually(func() error {
			return cl.Get(ctx, client.ObjectKeyFromObject(infra), &configv1.Infrastructure{})
		}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))

		deleteJob(ctx, jobKey)
	})

	It("does nothing when Infrastructure does not exist", func() {
		reconciler := newReconciler([]configv1.FeatureGateName{features.FeatureGateVSphereMixedNodeEnv}, nil)
		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, jobKey, &batchv1.Job{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("does nothing for a non-vSphere platform", func() {
		createInfra(configv1.AWSPlatformType)

		reconciler := newReconciler([]configv1.FeatureGateName{features.FeatureGateVSphereMixedNodeEnv}, nil)
		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, jobKey, &batchv1.Job{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("does nothing when the VSphereMixedNodeEnv feature gate is disabled", func() {
		createInfra(configv1.VSpherePlatformType)

		reconciler := newReconciler(nil, []configv1.FeatureGateName{features.FeatureGateVSphereMixedNodeEnv})
		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, jobKey, &batchv1.Job{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("creates the Job once vSphere platform and the feature gate are both active", func() {
		createInfra(configv1.VSpherePlatformType)

		reconciler := newReconciler([]configv1.FeatureGateName{features.FeatureGateVSphereMixedNodeEnv}, nil)
		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		job := &batchv1.Job{}
		Expect(cl.Get(ctx, jobKey, job)).To(Succeed())
		Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal(testNodeLabelSyncImage))
		Expect(job.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{Name: "RELEASE_VERSION", Value: testNodeLabelSyncVersion}))
		Expect(job.Spec.Template.Spec.HostNetwork).To(BeTrue())
		Expect(job.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("node-role.kubernetes.io/master", ""))
	})

	It("leaves an already-existing Job untouched", func() {
		createInfra(configv1.VSpherePlatformType)

		reconciler := newReconciler([]configv1.FeatureGateName{features.FeatureGateVSphereMixedNodeEnv}, nil)

		existingJob := reconciler.buildNodeLabelSyncJob()
		existingJob.Spec.Template.Spec.Containers[0].Image = "some-other-image"
		Expect(cl.Create(ctx, existingJob)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		job := &batchv1.Job{}
		Expect(cl.Get(ctx, jobKey, job)).To(Succeed())
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("some-other-image"))
	})
})
