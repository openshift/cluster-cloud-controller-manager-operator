package aws

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/ccm-aws-tests/e2e/aws/health"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	envHealthserverImage = "HEALTHSERVER_IMAGE"

	healthTransitionTestPrefix = e2eTestPrefixLoadBalancer + " health-transition"
)

// Scenario 5.5: Pre-readyz routing detection (OCPBUGS-86789 reproducer).
//
// Deploys a health-controllable server behind an NLB, triggers a pod restart,
// and checks whether the NLB routes NEW connections to the restarted target
// before its /readyz endpoint returns 200 — while other healthy targets exist.
//
// Prerequisites:
//   - HEALTHSERVER_IMAGE environment variable set to a registry-accessible
//     healthserver container image (built from cmd/healthserver/).
//   - AWS credentials with ELBv2 DescribeTargetHealth and DescribeTargetGroups.
//
// How to run:
//
//	export HEALTHSERVER_IMAGE=quay.io/<user>/healthserver:latest
//	./openshift-tests/bin/cloud-controller-manager-aws-tests-ext run-test \
//	  "[cloud-provider-aws-e2e-openshift] loadbalancer health-transition NLB target health state transitions during rolling restart should not route to pre-readyz targets when healthy targets are available [Suite:openshift/conformance/parallel]"
var _ = Describe(healthTransitionTestPrefix+" NLB", func() {
	f := framework.NewDefaultFramework("cloud-provider-aws")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	var cs clientset.Interface
	var ns *v1.Namespace

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
	})

	Context("target health state transitions during rolling restart", func() {
		It("should not route to pre-readyz targets "+
			"when healthy targets are available", func(ctx context.Context) {

			image := os.Getenv(envHealthserverImage)
			if image == "" {
				Skip(fmt.Sprintf("%s not set, skipping health transition test", envHealthserverImage))
			}

			replicas := int32(3)
			startupDelay := 60 * time.Second
			clientInterval := 500 * time.Millisecond
			observerInterval := 1 * time.Second
			steadyStateDuration := 30 * time.Second
			observeDuration := startupDelay + 3*time.Minute

			deployName := "healthserver"
			svcName := "healthserver-lb"

			By("creating healthserver Deployment")
			deploy := buildHealthserverDeployment(ns.Name, deployName, replicas, startupDelay, image)
			_, err := cs.AppsV1().Deployments(ns.Name).Create(ctx, deploy, metav1.CreateOptions{})
			framework.ExpectNoError(err, "create deployment")

			By("creating NLB Service with /readyz health check")
			svc := buildHealthTransitionService(ns.Name, svcName, deployName)
			_, err = cs.CoreV1().Services(ns.Name).Create(ctx, svc, metav1.CreateOptions{})
			framework.ExpectNoError(err, "create service")

			var lbDNS string
			DeferCleanup(func(cleanupCtx context.Context) {
				framework.Logf("cleaning up health transition test resources")
				_ = cs.AppsV1().Deployments(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
				_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, svcName, metav1.DeleteOptions{})
				if lbDNS != "" {
					waitForLBDeletion(cleanupCtx, lbDNS)
				}
			})

			By("waiting for Deployment rollout")
			err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
				d, err := cs.AppsV1().Deployments(ns.Name).Get(ctx, deployName, metav1.GetOptions{})
				if err != nil {
					return false, nil
				}
				framework.Logf("deployment ready replicas: %d/%d", d.Status.ReadyReplicas, replicas)
				return d.Status.ReadyReplicas >= replicas, nil
			})
			framework.ExpectNoError(err, "deployment rollout")

			By("waiting for NLB provisioning")
			err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
				s, err := cs.CoreV1().Services(ns.Name).Get(ctx, svcName, metav1.GetOptions{})
				if err != nil {
					return false, nil
				}
				if len(s.Status.LoadBalancer.Ingress) > 0 {
					lbDNS = s.Status.LoadBalancer.Ingress[0].Hostname
					return lbDNS != "", nil
				}
				return false, nil
			})
			framework.ExpectNoError(err, "NLB provisioning")
			framework.Logf("NLB DNS: %s", lbDNS)

			By("discovering NLB and target group in AWS")
			elbClient, err := createAWSClientLoadBalancer(ctx)
			framework.ExpectNoError(err, "create ELB client")

			foundLB, err := getAWSLoadBalancerFromDNSName(ctx, elbClient, lbDNS)
			framework.ExpectNoError(err, "find NLB")
			lbARN := aws.ToString(foundLB.LoadBalancerArn)
			framework.Logf("NLB ARN: %s", lbARN)

			observer := health.NewObserver(elbClient, observerInterval)
			err = observer.DiscoverTargetGroup(ctx, lbARN)
			framework.ExpectNoError(err, "discover target group")
			framework.Logf("TG ARN: %s (target type: %s)", observer.TargetGroupARN(), observer.TargetType())

			By("waiting for all TG targets to become healthy")
			err = observer.WaitForAllHealthy(ctx, int(replicas), 10*time.Minute)
			framework.ExpectNoError(err, "targets healthy")
			framework.Logf("all %d TG targets are healthy", replicas)

			By("starting client polling and TG health observer")
			client := health.NewClient(fmt.Sprintf("http://%s/", lbDNS), clientInterval)
			observer.Start(ctx)
			client.Start(ctx)

			By(fmt.Sprintf("verifying steady state for %s", steadyStateDuration))
			time.Sleep(steadyStateDuration)

			steadyRecords := client.Records()
			steadyNonReady := 0
			for _, r := range steadyRecords {
				if r.IsNonReadyReq {
					steadyNonReady++
				}
			}
			framework.Logf("steady state: %d requests, %d non-ready", len(steadyRecords), steadyNonReady)
			Expect(steadyNonReady).To(Equal(0), "no pre-readyz responses expected during steady state")

			By("deleting one pod to trigger restart cycle")
			pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app=%s", deployName),
			})
			framework.ExpectNoError(err, "list pods")
			Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

			targetPod := pods.Items[0].Name
			targetNode := pods.Items[0].Spec.NodeName
			framework.Logf("deleting pod %s (node: %s) to trigger restart", targetPod, targetNode)
			err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
			framework.ExpectNoError(err, "delete pod")

			By(fmt.Sprintf("observing health transitions for %s", observeDuration))
			time.Sleep(observeDuration)

			By("collecting results")
			client.Stop()
			observer.Stop()

			allRecords := client.Records()
			allEvents := observer.Events()

			nonReadyCount := 0
			var nonReadyDetails []string
			uniqueServers := make(map[string]bool)
			errorCount := 0

			for _, r := range allRecords {
				if r.ServerID != "" {
					uniqueServers[r.ServerID] = true
				}
				if r.IsNonReadyReq {
					nonReadyCount++
					nonReadyDetails = append(nonReadyDetails, fmt.Sprintf(
						"  t=%s server=%s target_ip=%s tcp_dial=%s",
						r.Timestamp.Format(time.RFC3339), r.ServerID, r.TargetIP, r.TCPDialDuration))
				}
				if r.Error != "" {
					errorCount++
				}
			}

			framework.Logf("")
			framework.Logf("═══════════════════════════════════════════════════════════")
			framework.Logf("HEALTH TRANSITION REPORT — Scenario 5.5 (Pre-Readyz Routing)")
			framework.Logf("═══════════════════════════════════════════════════════════")
			framework.Logf("Replicas:          %d", replicas)
			framework.Logf("Startup Delay:     %s", startupDelay)
			framework.Logf("Total Requests:    %d", len(allRecords))
			framework.Logf("Request Errors:    %d", errorCount)
			framework.Logf("Unique Servers:    %d", len(uniqueServers))
			framework.Logf("NonReadyRequests:  %d", nonReadyCount)
			framework.Logf("TG Health Events:  %d", len(allEvents))

			if nonReadyCount > 0 {
				framework.Logf("")
				framework.Logf("PRE-READYZ ROUTING DETECTED (OCPBUGS-86789):")
				for _, d := range nonReadyDetails {
					framework.Logf("%s", d)
				}
			}

			framework.Logf("")
			framework.Logf("TG Health Timeline:")
			for _, e := range allEvents {
				framework.Logf("  t=%s target=%s %s→%s reason=%s",
					e.Timestamp.Format(time.RFC3339), e.TargetID, e.PrevState, e.State, e.Reason)
			}

			framework.Logf("")
			if nonReadyCount > 0 {
				framework.Logf("VERDICT: NLB routed %d request(s) to pre-readyz target(s)", nonReadyCount)
				framework.Logf("This reproduces OCPBUGS-86789 — NLB routes before /readyz passes")
			} else {
				framework.Logf("VERDICT: No pre-readyz routing detected in this iteration")
			}
			framework.Logf("═══════════════════════════════════════════════════════════")
		})
	})
})

func buildHealthserverDeployment(namespace, name string, replicas int32, startupDelay time.Duration, image string) *appsv1.Deployment {
	labels := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: v1.PodSpec{
					TopologySpreadConstraints: []v1.TopologySpreadConstraint{{
						MaxSkew:           1,
						TopologyKey:       "kubernetes.io/hostname",
						WhenUnsatisfiable: v1.ScheduleAnyway,
						LabelSelector:     &metav1.LabelSelector{MatchLabels: labels},
					}},
					Containers: []v1.Container{{
						Name:  "healthserver",
						Image: image,
						Args:  []string{fmt.Sprintf("--startup-delay=%s", startupDelay)},
						Ports: []v1.ContainerPort{{
							Name:          "http",
							ContainerPort: 8080,
						}},
						Env: []v1.EnvVar{{
							Name: "POD_NAME",
							ValueFrom: &v1.EnvVarSource{
								FieldRef: &v1.ObjectFieldSelector{FieldPath: "metadata.name"},
							},
						}},
					}},
				},
			},
		},
	}
}

func buildHealthTransitionService(namespace, name, deployName string) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-type":                            "nlb",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol":            "HTTP",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-path":                "/readyz",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-port":                "traffic-port",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval":            "10",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-healthy-threshold":   "2",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-unhealthy-threshold": "2",
			},
		},
		Spec: v1.ServiceSpec{
			Type:                  v1.ServiceTypeLoadBalancer,
			ExternalTrafficPolicy: v1.ServiceExternalTrafficPolicyLocal,
			Selector:              map[string]string{"app": deployName},
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   v1.ProtocolTCP,
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
}

func waitForLBDeletion(ctx context.Context, lbDNS string) {
	elbClient, err := createAWSClientLoadBalancer(ctx)
	if err != nil {
		framework.Logf("failed to create ELB client for cleanup: %v", err)
		return
	}
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		lb, err := findAWSLoadBalancerByDNSName(ctx, elbClient, lbDNS)
		if err != nil {
			return false, nil
		}
		return lb == nil, nil
	})
	if err != nil {
		framework.Logf("warning: timed out waiting for LB deletion: %v", err)
	}
}
