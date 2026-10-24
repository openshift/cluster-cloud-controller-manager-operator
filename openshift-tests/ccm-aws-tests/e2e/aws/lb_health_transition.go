package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/ccm-aws-tests/e2e/aws/health"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/ccm-aws-tests/e2e/common"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	// envHealthserverImage is the container image for the unified binary
	// e2e-nlb-health-test. Used for all three roles (serve, client, aggregator).
	envHealthserverImage = "HEALTHSERVER_IMAGE"

	healthTransitionTestPrefix = e2eTestPrefixLoadBalancer + " health-transition"

	// healthserverPort is the port the healthserver binds on the node IP
	// via hostNetwork. Chosen to avoid conflicts with existing services on
	// control-plane nodes (verified via netstat). Echoes 6443 (KAS port).
	healthserverPort = 19443

	// aggregatorPort is the port the aggregator listens on (worker node).
	aggregatorPort = 8090

	// clientPort is the port the in-cluster client serves metrics/records on.
	clientPort = 8080

	// kasShutdownDelay matches the KAS shutdown-delay-duration (135s graceful +
	// margin), simulating how long KAS keeps serving after /readyz→503 before
	// the process exits.  CKAO sets 135s; we add buffer for HC propagation.
	kasShutdownDelay = 192 * time.Second

	// defaultClientInterval controls how often each worker sends requests
	// through the NLB. Each worker fires independently on its own ticker.
	// With DisableKeepAlives (new TCP per request), each worker creates
	// one outbound connection at a time. Too many workers with short
	// intervals can exhaust ephemeral ports and starve K8s API calls.
	// With the in-cluster client (~1-5ms RTT to NLB), higher concurrency
	// is safe. 16 workers at 50ms = ~320 req/s at 1ms RTT, ~160 req/s
	// at 5ms RTT. Port exhaustion is not a concern because the client
	// runs inside the cluster on a worker node, not from an external
	// machine competing with K8s API calls.
	defaultClientInterval = 50 * time.Millisecond
	defaultClientWorkers  = 16

	// postHealthyObserve is how long we continue observing after all targets
	// become healthy (both initial setup and post-restart). 90s gives enough
	// time to confirm stable routing while keeping test duration reasonable.
	postHealthyObserve = 90 * time.Second

	// shutdownDrainObserve90sec is how long we wait after TG detects unhealthy
	// before triggering in-place container restart (ctl). Allows NLB to finish
	// routing to the draining target (t7) before TCP goes down briefly.
	// Used by 5.5-SDK-multi-kas-ctl (legacy 90s timing — Hyperplane-dependent repro).
	shutdownDrainObserve90sec = 90 * time.Second

	// shutdownDrainObserve30sec — validated primary drain (~55s to TCP up after t5).
	// See tls-ctl drain variants in runSDKMultiKasTLSCtlDrainTest.
	shutdownDrainObserve30sec = 30 * time.Second

	// Additional drain durations for 5.5-SDK-multi-kas-tls-ctl variants (NLB propagation sweep).
	shutdownDrainObserve15sec  = 15 * time.Second
	shutdownDrainObserve60sec  = 60 * time.Second
	shutdownDrainObserve129sec = 129 * time.Second // KAS shutdown-delay-duration (129s)
	shutdownDrainObserve150sec = 150 * time.Second
	shutdownDrainObserve180sec = 180 * time.Second
	shutdownDrainObserve210sec = 210 * time.Second
	shutdownDrainObserve240sec = 240 * time.Second
	shutdownDrainObserve300sec = 300 * time.Second
	shutdownDrainObserve600sec = 600 * time.Second
)

// transitionTimeline captures all timing milestones from the SPLAT-307 state
// machine extended with restart-phase timers (t7.1–t7.4) for OCPBUGS-86789.
//
// A zero time.Time means the milestone was not observed.
type transitionTimeline struct {
	// Initial registration phase (t0-t4, captured during setup)
	T0 time.Time // deployment created (pods scheduling)
	T1 time.Time // deployment ready (all pods Running)
	T2 time.Time // NLB provisioned (LB DNS assigned)
	T3 time.Time // all TG targets healthy (HC passed + propagated)
	T4 time.Time // first client request received (NLB routing established)

	// Shutdown phase (SPLAT-307 path)
	T5 time.Time // readyz→503 signal sent
	T6 time.Time // first observer event: target unhealthy
	T7 time.Time // last client request routed to target after t5

	// Restart phase (Scenario 5.5 only; zero for 5.2)
	T71 time.Time // pod delete sent
	T73 time.Time // new pod TCP up (from X-Server-Start-Time header)
	T74 time.Time // first pre-readyz request from new pod (BUG if present)

	// Startup phase
	T8  time.Time // readyz→200 (from X-First-Readyz-Time header or admin signal)
	T9  time.Time // first observer event: target healthy after t8
	T10 time.Time // first client request to target after t9

	// Counters
	UnhealthyReqCount   int // requests served by target between t5 and t7
	PreReadyzReqCount   int // requests with X-Server-State: pre-readyz
	LateConnectionCount int // requests to target after 80% of kasShutdownDelay and before/at t7

	// Identity
	TargetPod  string
	TargetNode string
	NewPod     string

	// PodNodeMap maps pod names to the node they run on, used for
	// displaying node identity alongside pod names in the report.
	PodNodeMap map[string]string
}

// serviceConfig records test metadata and AWS resource identifiers for the report.
// AWS configuration snapshots (Describe* output) are populated at report time via
// populateAWSReportConfig — not from parsed test intent.
type serviceConfig struct {
	ServiceAnnotations map[string]string // e2e / Kubernetes Service intent (not AWS API)
	TGARN              string
	TGTargetType       string
	LBARN              string
	LBDNS              string

	// AWS API snapshots (populated in buildReport by populateAWSReportConfig).
	AWSLoadBalancer      []health.TGAttribute
	AWSLoadBalancerAttrs []health.TGAttribute
	AWSTargetGroup       []health.TGAttribute
	AWSTargetGroupAttrs  []health.TGAttribute

	// Environment summary
	Region   string
	Platform string // e.g., "AWS"
	Topology string // e.g., "HighlyAvailable"
}

var _ = Describe(healthTransitionTestPrefix, func() {
	f := framework.NewDefaultFramework("cloud-provider-aws")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	var cs clientset.Interface
	var ns *v1.Namespace

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
	})

	// // ── Scenario 5.5 ───────────────────────────────────────────────────
	// Context("NLB pre-readyz routing detection (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"when healthy targets are available", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		replicas := int32(3)
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay

	// 		deployName := "healthserver"
	// 		svcName := "healthserver-lb"

	// 		// Setup creates NLB targeting master nodes, waits for ALL targets healthy
	// 		_, observer, svcCfg, setupTimes, clientPodName := setupHealthTransition(
	// 			ctx, cs, ns, deployName, svcName, image,
	// 			replicas, startupDelay,
	// 		)

	// 		// The in-cluster client is already running (deployed in setup).
	// 		// Start the TG observer for health state tracking, and push
	// 		// TG snapshots to the aggregator every 2s so all state changes
	// 		// from the AWS perspective appear in the aggregator timeline.
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		framework.Logf("[observer] started TG health polling (1s) + aggregator push (2s)")
	// 		framework.Logf("[client-pod] in-cluster client %s already sending requests", clientPodName)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		// Steady state: 90s for the in-cluster client to establish
	// 		// traffic to all replicas before triggering the test scenario.
	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		// Fetch steady-state records from the in-cluster client
	// 		steadyRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		framework.Logf("[steady] %d requests from in-cluster client, %d non-ready", len(steadyRecords), steadyNonReady)
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		// Build knownServers and podNodeMap from ALL existing pods.
	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}

	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		// t5 = t7.1: Delete pod — kubelet sends SIGTERM, healthserver sets
	// 		// readyz→503 and keeps serving for terminationGracePeriodSeconds (192s).
	// 		// This exactly matches KAS rollout behavior: SIGTERM → readyz→503 →
	// 		// keep serving for shutdown-delay-duration → process killed.
	// 		// After terminationGracePeriodSeconds, kubelet kills the pod and
	// 		// the Deployment creates a replacement.
	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod")
	// 		newPod := waitForNewPod(ctx, cs, ns.Name, deployName, targetPod)

	// 		// Capture the new pod's node for the report
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 		}

	// 		// First wait for the TG to detect the unhealthy target (HC
	// 		// needs threshold×interval to detect). Without this, the next
	// 		// waitForAllTGTargetsHealthy returns immediately because the TG
	// 		// hasn't processed the failure yet.
	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		// Now wait for the restarted target to recover and become healthy.
	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		// Fetch all request records from the in-cluster client pod
	// 		allRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5 (Pre-Readyz Routing / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())

	// 		report += buildVerdict55(tl, allRecords)

	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5 variant with CAPA TG attributes ────────────────────
	// // Same as 5.5 but applies the CAPA fix TG attributes after TG creation:
	// //   target_health_state.unhealthy.connection_termination.enabled = false
	// //   target_health_state.unhealthy.draining_interval_seconds = 300
	// // This simulates the NLB configuration applied by CAPA (OCPBUGS-55626).
	// Context("NLB pre-readyz routing with CAPA TG attributes (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with connection-termination disabled and draining=300s", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		replicas := int32(3)
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay

	// 		deployName := "healthserver"
	// 		svcName := "healthserver-lb"

	// 		_, observer, svcCfg, setupTimes, clientPodName := setupHealthTransition(
	// 			ctx, cs, ns, deployName, svcName, image,
	// 			replicas, startupDelay,
	// 		)

	// 		// Apply CAPA fix TG attributes BEFORE starting the observer.
	// 		capaAttrs := map[string]string{
	// 			"target_health_state.unhealthy.connection_termination.enabled": "false",
	// 			"target_health_state.unhealthy.draining_interval_seconds":      "300",
	// 		}
	// 		By("applying CAPA TG attributes (conn_term=false, draining=300s)")
	// 		err := observer.ModifyTGAttributes(ctx, capaAttrs)
	// 		framework.ExpectNoError(err, "modify TG attributes for CAPA variant")

	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		framework.Logf("[observer] started TG health polling (1s) + aggregator push (2s)")
	// 		framework.Logf("[client-pod] in-cluster client %s already sending requests", clientPodName)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods (K8s API may be overloaded by client workers)")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		// Build knownServers and podNodeMap from ALL existing pods.
	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}

	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod")
	// 		newPod := waitForNewPod(ctx, cs, ns.Name, deployName, targetPod)

	// 		// Capture the new pod's node for the report
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 		}

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		// Fetch all request records from the in-cluster client pod
	// 		allRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-CAPA (Pre-Readyz + conn_term=false draining=300s)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())

	// 		report += buildVerdict55(tl, allRecords)

	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.2 ───────────────────────────────────────────────────
	// Context("NLB shutdown propagation measurement (SPLAT-307)", func() {
	// 	It("should stop routing within shutdown-delay after "+
	// 		"readyz starts failing", func(ctx context.Context) {

	// 		// Scenario 5.2 requires the admin signal (readyz→503 without pod
	// 		// deletion) which doesn't work with hostNetwork: the K8s API server
	// 		// pod proxy can't reach nodeIP:19443 due to security group rules.
	// 		// TODO: implement alternative signaling (e.g., ConfigMap watch, or
	// 		// a non-hostNetwork admin sidecar).
	// 		Skip("Scenario 5.2 not yet supported with hostNetwork (admin signal unreachable)")

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		replicas := int32(3)
	// 		startupDelay := 60 * time.Second
	// 		shutdownObserveDuration := 3 * time.Minute
	// 		recoveryObserveDuration := 3 * time.Minute

	// 		deployName := "healthserver"
	// 		svcName := "healthserver-lb"

	// 		_, observer, svcCfg, setupTimes, clientPodName := setupHealthTransition(
	// 			ctx, cs, ns, deployName, svcName, image,
	// 			replicas, startupDelay,
	// 		)

	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		framework.Logf("[observer] started TG health polling (1s) + aggregator push (2s)")
	// 		framework.Logf("[client-pod] in-cluster client %s already sending requests", clientPodName)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		By("listing pods to identify target for shutdown simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("signaling target pod readyz→503 (t5)")
	// 		t5 := time.Now()
	// 		err = sendAdminSignal(ctx, cs, ns.Name, targetPod, false)
	// 		framework.ExpectNoError(err)

	// 		By(fmt.Sprintf("observing shutdown propagation for %s", shutdownObserveDuration))
	// 		time.Sleep(shutdownObserveDuration)

	// 		By("signaling target pod readyz→200 (t8)")
	// 		t8 := time.Now()
	// 		err = sendAdminSignal(ctx, cs, ns.Name, targetPod, true)
	// 		framework.ExpectNoError(err)

	// 		By(fmt.Sprintf("observing recovery for %s", recoveryObserveDuration))
	// 		time.Sleep(recoveryObserveDuration)

	// 		// Fetch all records from the in-cluster client
	// 		allRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline52(targetPod, t5, t8, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		// t4: first successful client request
	// 		for _, r := range allRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.2 (Shutdown Propagation / SPLAT-307)",
	// 			tl, svcCfg, replicas, startupDelay, 0,
	// 			allRecords, allEvents, observer.Snapshots())

	// 		report += buildVerdict52(tl)

	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5 CLB baseline ───────────────────────────────────────
	// // Same as Scenario 5.5 but using Classic Load Balancer instead of NLB.
	// // Compares CLB and NLB health transition behavior to determine if the
	// // pre-readyz routing issue is NLB-specific (Hyperplane) or broader.
	// Context("CLB pre-readyz routing detection baseline (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"when healthy targets are available", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		replicas := int32(3)
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay

	// 		deployName := "healthserver"
	// 		svcName := "healthserver-lb"

	// 		// Setup uses CLB (no nlb annotation) with same HC config
	// 		lbDNS, clbObserver, svcCfg, setupTimes, clientPodName := setupHealthTransitionCLB(
	// 			ctx, cs, ns, deployName, svcName, image,
	// 			replicas, startupDelay,
	// 		)
	// 		_ = lbDNS

	// 		clbObserver.Start(ctx)
	// 		// Push CLB health snapshots to aggregator every 2s
	// 		stopCLBPush := startCLBSnapshotPusher(ctx, cs, ns.Name, clbObserver)
	// 		framework.Logf("[observer] started CLB health polling (1s) + aggregator push (2s)")
	// 		framework.Logf("[client-pod] in-cluster client %s already sending requests", clientPodName)
	// 		defer func() { stopCLBPush(); clbObserver.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		framework.Logf("[steady] %d requests from in-cluster client, %d non-ready", len(steadyRecords), steadyNonReady)
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}

	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod")
	// 		newPod := waitForNewPod(ctx, cs, ns.Name, deployName, targetPod)

	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 		}

	// 		// Wait for CLB to detect unhealthy, then recover
	// 		By("waiting for CLB to detect unhealthy instance")
	// 		waitForCLBUnhealthy(ctx, clbObserver, 3*time.Minute)

	// 		By("waiting for all CLB instances to become healthy")
	// 		err = clbObserver.WaitForAllHealthy(ctx, int(replicas), 10*time.Minute)
	// 		framework.ExpectNoError(err, "CLB instances healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		allEvents := clbObserver.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-CLB (Pre-Readyz Routing CLB Baseline / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, clbObserver.Snapshots())

	// 		report += buildVerdict55(tl, allRecords)

	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5 SDK-managed NLB (KAS-equivalent) ───────────────────
	// // Creates the NLB directly via AWS SDK with instance:19443 targets,
	// // replicating how the OCP installer creates the KAS NLB with
	// // instance:6443 targets. Both traffic AND health checks go to the
	// // same port (19443) on the same path — no kube-proxy, no NodePort,
	// // no K8s Service. This is the closest reproduction of the actual
	// // KAS NLB setup where OCPBUGS-86789 is observed.
	// Context("SDK-managed NLB pre-readyz routing (KAS-equivalent) (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with instance:port targeting matching KAS NLB", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay

	// 		deployName := "healthserver"

	// 		// Register cleanup FIRST, before creating any resources.
	// 		// The cleanup function captures variables by reference — they're
	// 		// populated as resources are created during setup.
	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-managed NLB test resources")
	// 		if sdkNLB != nil {
	// 			elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 			if err == nil {
	// 				ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 				if ec2Err != nil {
	// 					framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 				}
	// 				deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 			}
	// 		}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-client", metav1.DeleteOptions{})
	// 		})

	// 		// ── Deploy aggregator ──
	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)
	// 		framework.Logf("[aggregator] ready at %s", aggregatorURL)

	// 		// ── SCC for hostNetwork ──
	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		// ── Deploy healthserver pods via DaemonSet (one per master node) ──
	// 		// DaemonSet guarantees same-node replacement on pod deletion,
	// 		// matching KAS static pod rollout behavior.
	// 		By("creating healthserver DaemonSet (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSet(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err := cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()
	// 		framework.Logf("[daemonset] %d pods ready on master nodes", replicas)

	// 		// ── Discover cluster infrastructure ──
	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")
	// 		framework.Logf("[infra] infraID=%s vpc=%s subnets=%v instances=%v sg=%s",
	// 			infra.InfraID, infra.VPCID, infra.SubnetIDs, infra.InstanceIDs, infra.MasterSGID)

	// 		// ── Add SG rule for port 19443 ──
	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		// ── Create NLB via SDK ──
	// 		By("creating SDK-managed NLB with instance:19443 targets")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()
	// 		framework.Logf("[sdk-nlb] NLB DNS: %s", sdkNLB.NLBDNS)

	// 		// DeferCleanup is already registered at the top of this test.
	// 		// Populate the sdkNLB variable so cleanup knows what to delete.

	// 		// ── Create TG observer (reuses existing NLB observer since SDK NLB uses ELBv2) ──
	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		// The TG ARN is already known from createSDKManagedNLB
	// 		// Set it directly on the observer by discovering from the NLB ARN
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")
	// 		framework.Logf("[sdk-nlb] TG ARN: %s (target type: %s)", observer.TargetGroupARN(), observer.TargetType())

	// 		// ── Wait for all targets healthy ──
	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		// ── Deploy in-cluster client (pointing to SDK NLB DNS) ──
	// 		// Use double the default workers for better resolution of the
	// 		// narrow pre-readyz window (~10s HC interval).
	// 		By("deploying in-cluster client on worker node")
	// 		clientPodName := deployInClusterClient(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers*2, defaultClientInterval)

	// 		// ── Build service config for report ──
	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":            "true",
	// 				"target-port":            fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":           fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}

	// 		// Fetch TG attributes for report
	// 		// ── Start observer + TG push ──
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		framework.Logf("[observer] started TG health polling (1s) + aggregator push (2s)")
	// 		framework.Logf("[client-pod] in-cluster client %s sending to SDK NLB", clientPodName)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		// ── Steady state ──
	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		framework.Logf("[steady] %d requests from in-cluster client, %d non-ready", len(steadyRecords), steadyNonReady)
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		// ── Pick target ──
	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}

	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		// ── Delete pod (SIGTERM triggers readyz→503) ──
	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		// waitForNewPod only skips the old pod by name, so it would
	// 		// immediately return one of the other still-running DaemonSet pods.
	// 		// Use waitForNewPodFromSet which requires a pod name NOT in knownServers.
	// 		By("waiting for replacement pod on same node (DaemonSet guarantee)")
	// 		newPod := waitForNewPodFromSet(ctx, cs, ns.Name, deployName, knownServers)

	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 			if newPodObj.Spec.NodeName != targetNode {
	// 				framework.Logf("WARNING: [daemonset] replacement pod %s landed on %s, expected %s — port conflict with terminating pod?",
	// 					newPod, newPodObj.Spec.NodeName, targetNode)
	// 			} else {
	// 				framework.Logf("[daemonset] replacement pod %s on same node %s (verified)", newPod, targetNode)
	// 			}
	// 		}

	// 		// ── Wait for TG unhealthy then healthy ──
	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		// ── Collect + report ──
	// 		allRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-SDK (Pre-Readyz Routing KAS-Equivalent / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())

	// 		report += buildVerdict55(tl, allRecords)

	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5-SDK-no-cip ─────────────────────────────────────────
	// // Identical to 5.5-SDK but with preserve_client_ip.enabled=false on the
	// // TG. The NLB then distributes connections across targets without source-IP
	// // stickiness, so all 3 targets receive traffic even from a single client pod.
	// // Allows direct comparison with 5.5-SDK to isolate the preserve_client_ip effect.
	// Context("SDK-managed NLB pre-readyz routing, preserve_client_ip=false (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with instance:port targeting and preserve_client_ip disabled", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay
	// 		deployName := "healthserver"

	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-no-cip test resources")
	// 			if sdkNLB != nil {
	// 				elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 				if err == nil {
	// 					ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 					if ec2Err != nil {
	// 						framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 					}
	// 					deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 				}
	// 			}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-client", metav1.DeleteOptions{})
	// 		})

	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		By("creating healthserver DaemonSet (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSet(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err := cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()

	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")

	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		By("creating SDK-managed NLB with instance:19443 targets")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()

	// 		By("disabling preserve_client_ip on TG (so NLB distributes across all targets)")
	// 		err = setTGPreserveClientIP(ctx, elbClient, sdkNLB.TGARN, false)
	// 		framework.ExpectNoError(err, "set preserve_client_ip=false")

	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")

	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		By("deploying in-cluster client on worker node")
	// 		clientPodName := deployInClusterClient(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers*2, defaultClientInterval)

	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":              "true",
	// 				"preserve_client_ip":       "false",
	// 				"target-port":              fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":             fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                  fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod on same node (DaemonSet guarantee)")
	// 		newPod := waitForNewPodFromSet(ctx, cs, ns.Name, deployName, knownServers)
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 			if newPodObj.Spec.NodeName != targetNode {
	// 				framework.Logf("WARNING: [daemonset] replacement pod %s landed on %s, expected %s",
	// 					newPod, newPodObj.Spec.NodeName, targetNode)
	// 			} else {
	// 				framework.Logf("[daemonset] replacement pod %s on same node %s (verified)", newPod, targetNode)
	// 			}
	// 		}

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchClientRecords(ctx, cs, ns.Name, clientPodName)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-SDK-no-cip (preserve_client_ip=false / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())
	// 		report += buildVerdict55(tl, allRecords)
	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5-SDK-multi ───────────────────────────────────────────
	// // Identical to 5.5-SDK but deploys one client pod per worker node
	// // (DaemonSet). Each pod has a distinct source IP so the NLB distributes
	// // traffic across all targets with preserve_client_ip=true (same as real
	// // KAS clients coming from different node IPs). Records from all client
	// // pods are merged before analysis.
	// Context("SDK-managed NLB pre-readyz routing, multi-client (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with instance:port targeting and multiple client IPs", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay
	// 		deployName := "healthserver"
	// 		clientDSName := "healthtest-client"

	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-multi test resources")
	// 			if sdkNLB != nil {
	// 				elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 				if err == nil {
	// 					ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 					if ec2Err != nil {
	// 						framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 					}
	// 					deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 				}
	// 			}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, clientDSName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 		})

	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		By("creating healthserver DaemonSet (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSet(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err := cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()

	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")

	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		By("creating SDK-managed NLB with instance:19443 targets")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()

	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")

	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		// One client pod per worker node — each has a unique source IP,
	// 		// so the NLB distributes traffic across all 3 targets.
	// 		By("deploying client DaemonSet on worker nodes (one pod per worker)")
	// 		clientPodNames := deployClientDaemonSet(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers, defaultClientInterval)

	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":              "true",
	// 				"client-mode":              "multi-client-daemonset",
	// 				"preserve_client_ip":       "true",
	// 				"target-port":              fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":             fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                  fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod on same node (DaemonSet guarantee)")
	// 		newPod := waitForNewPodFromSet(ctx, cs, ns.Name, deployName, knownServers)
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 			if newPodObj.Spec.NodeName != targetNode {
	// 				framework.Logf("WARNING: [daemonset] replacement pod %s landed on %s, expected %s",
	// 					newPod, newPodObj.Spec.NodeName, targetNode)
	// 			} else {
	// 				framework.Logf("[daemonset] replacement pod %s on same node %s (verified)", newPod, targetNode)
	// 			}
	// 		}

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-SDK-multi (Multi-Client DaemonSet / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())
	// 		report += buildVerdict55(tl, allRecords)
	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5-SDK-multi-no-cip ───────────────────────────────────
	// // Identical to 5.5-SDK-multi but with preserve_client_ip.enabled=false.
	// // Isolates whether multi-client distribution changes pre-readyz behaviour
	// // when source-IP stickiness is disabled.
	// Context("SDK-managed NLB pre-readyz routing, multi-client preserve_client_ip=false (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with instance:port targeting, multiple client IPs, and preserve_client_ip disabled", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay
	// 		deployName := "healthserver"
	// 		clientDSName := "healthtest-client"

	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-multi-no-cip test resources")
	// 			if sdkNLB != nil {
	// 				elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 				if err == nil {
	// 					ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 					if ec2Err != nil {
	// 						framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 					}
	// 					deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 				}
	// 			}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, clientDSName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 		})

	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		By("creating healthserver DaemonSet (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSet(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err := cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()

	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")

	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		By("creating SDK-managed NLB with instance:19443 targets")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()

	// 		By("disabling preserve_client_ip on TG (so NLB distributes across all targets)")
	// 		err = setTGPreserveClientIP(ctx, elbClient, sdkNLB.TGARN, false)
	// 		framework.ExpectNoError(err, "set preserve_client_ip=false")

	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")

	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		By("deploying client DaemonSet on worker nodes (one pod per worker)")
	// 		clientPodNames := deployClientDaemonSet(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers, defaultClientInterval)

	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":              "true",
	// 				"client-mode":              "multi-client-daemonset",
	// 				"preserve_client_ip":       "false",
	// 				"target-port":              fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":             fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                  fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod on same node (DaemonSet guarantee)")
	// 		newPod := waitForNewPodFromSet(ctx, cs, ns.Name, deployName, knownServers)
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 			if newPodObj.Spec.NodeName != targetNode {
	// 				framework.Logf("WARNING: [daemonset] replacement pod %s landed on %s, expected %s",
	// 					newPod, newPodObj.Spec.NodeName, targetNode)
	// 			} else {
	// 				framework.Logf("[daemonset] replacement pod %s on same node %s (verified)", newPod, targetNode)
	// 			}
	// 		}

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-SDK-multi-no-cip (Multi-Client + preserve_client_ip=false / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())
	// 		report += buildVerdict55(tl, allRecords)
	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5-SDK-multi-kas ──────────────────────────────────────
	// // Multi-client DaemonSet with TG attributes matching the real KAS NLB:
	// //   preserve_client_ip=false, connection_termination=false,
	// //   draining_interval=300s, deregistration_delay=300s.
	// // This is the most faithful reproduction of real OCPBUGS-86789 conditions.
	// Context("SDK-managed NLB pre-readyz routing, multi-client KAS-config (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with instance:port targeting, KAS TG attributes, and multiple client IPs", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay
	// 		deployName := "healthserver"
	// 		clientDSName := "healthtest-client"

	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-multi-kas test resources")
	// 			if sdkNLB != nil {
	// 				elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 				if err == nil {
	// 					ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 					if ec2Err != nil {
	// 						framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 					}
	// 					deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 				}
	// 			}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, clientDSName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 		})

	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		By("creating healthserver DaemonSet (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSet(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err := cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()

	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")

	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		By("creating SDK-managed NLB with instance:19443 targets")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()

	// 		By("applying KAS-equivalent TG attributes (conn_term=false, draining=300s, preserve_client_ip=false)")
	// 		err = setTGKASAttributes(ctx, elbClient, sdkNLB.TGARN, false)
	// 		framework.ExpectNoError(err, "set KAS TG attributes")

	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")

	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		By("deploying client DaemonSet on worker nodes (one pod per worker)")
	// 		clientPodNames := deployClientDaemonSet(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers, defaultClientInterval)

	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":              "true",
	// 				"client-mode":              "multi-client-daemonset",
	// 				"preserve_client_ip":       "false",
	// 				"connection_termination":   "false",
	// 				"draining_interval":        "300",
	// 				"target-port":              fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":             fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                  fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod on same node (DaemonSet guarantee)")
	// 		newPod := waitForNewPodFromSet(ctx, cs, ns.Name, deployName, knownServers)
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 			if newPodObj.Spec.NodeName != targetNode {
	// 				framework.Logf("WARNING: [daemonset] replacement pod %s landed on %s, expected %s",
	// 					newPod, newPodObj.Spec.NodeName, targetNode)
	// 			} else {
	// 				framework.Logf("[daemonset] replacement pod %s on same node %s (verified)", newPod, targetNode)
	// 			}
	// 		}

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-SDK-multi-kas (Multi-Client + KAS TG Config / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())
	// 		report += buildVerdict55(tl, allRecords)
	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5-SDK-multi-kas-cip ──────────────────────────────────
	// // Same as 5.5-SDK-multi-kas but with preserve_client_ip=true.
	// // Allows comparing the effect of source-IP stickiness under real
	// // KAS draining/connection-termination settings.
	// Context("SDK-managed NLB pre-readyz routing, multi-client KAS-config preserve_client_ip=true (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with instance:port targeting, KAS TG attributes, preserve_client_ip enabled, and multiple client IPs", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay
	// 		deployName := "healthserver"
	// 		clientDSName := "healthtest-client"

	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-multi-kas-cip test resources")
	// 			if sdkNLB != nil {
	// 				elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 				if err == nil {
	// 					ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 					if ec2Err != nil {
	// 						framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 					}
	// 					deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 				}
	// 			}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, clientDSName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 		})

	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		By("creating healthserver DaemonSet (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSet(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err := cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()

	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")

	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		By("creating SDK-managed NLB with instance:19443 targets")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()

	// 		By("applying KAS-equivalent TG attributes (conn_term=false, draining=300s, preserve_client_ip=true)")
	// 		err = setTGKASAttributes(ctx, elbClient, sdkNLB.TGARN, true)
	// 		framework.ExpectNoError(err, "set KAS TG attributes")

	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")

	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		By("deploying client DaemonSet on worker nodes (one pod per worker)")
	// 		clientPodNames := deployClientDaemonSet(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers, defaultClientInterval)

	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":              "true",
	// 				"client-mode":              "multi-client-daemonset",
	// 				"preserve_client_ip":       "true",
	// 				"connection_termination":   "false",
	// 				"draining_interval":        "300",
	// 				"target-port":              fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":             fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                  fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod on same node (DaemonSet guarantee)")
	// 		newPod := waitForNewPodFromSet(ctx, cs, ns.Name, deployName, knownServers)
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 			if newPodObj.Spec.NodeName != targetNode {
	// 				framework.Logf("WARNING: [daemonset] replacement pod %s landed on %s, expected %s",
	// 					newPod, newPodObj.Spec.NodeName, targetNode)
	// 			} else {
	// 				framework.Logf("[daemonset] replacement pod %s on same node %s (verified)", newPod, targetNode)
	// 			}
	// 		}

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-SDK-multi-kas-cip (Multi-Client + KAS TG Config + CIP / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())
	// 		report += buildVerdict55(tl, allRecords)
	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5-SDK-multi-kas-tls ──────────────────────────────────
	// // Clone of 5.5-SDK-multi-kas with TLS on traffic port and HTTPS HC on
	// // the same port/path (/readyz), matching real KAS end-to-end TLS behaviour.
	// Context("SDK-managed NLB pre-readyz routing, multi-client KAS-config TLS (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with instance:port targeting, KAS TG attributes, HTTPS HC, TLS traffic, and multiple client IPs", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay
	// 		deployName := "healthserver"
	// 		clientDSName := "healthtest-client"

	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-multi-kas-tls test resources")
	// 			if sdkNLB != nil {
	// 				elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 				if err == nil {
	// 					ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 					if ec2Err != nil {
	// 						framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 					}
	// 					deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 				}
	// 			}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, clientDSName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().ConfigMaps(ns.Name).Delete(cleanupCtx, healthserverTLSConfigMap, metav1.DeleteOptions{})
	// 		})

	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		By("creating TLS cert ConfigMap for healthserver")
	// 		err := ensureHealthserverTLSConfigMap(ctx, cs, ns.Name)
	// 		framework.ExpectNoError(err, "create TLS configmap")

	// 		By("creating healthserver DaemonSet with TLS (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSetTLS(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err = cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()

	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")

	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		By("creating SDK-managed NLB with instance:19443 targets and HTTPS HC")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort), SDKNLBCreateOpts{
	// 			HealthCheckProtocol: elbv2types.ProtocolEnumHttps,
	// 		})
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()

	// 		By("applying KAS-equivalent TG attributes (conn_term=false, draining=300s, preserve_client_ip=false)")
	// 		err = setTGKASAttributes(ctx, elbClient, sdkNLB.TGARN, false)
	// 		framework.ExpectNoError(err, "set KAS TG attributes")

	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")

	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		By("deploying client DaemonSet on worker nodes (TLS to NLB, one pod per worker)")
	// 		clientPodNames := deployClientDaemonSet(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers, defaultClientInterval, true)

	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":              "true",
	// 				"client-mode":              "multi-client-daemonset",
	// 				"preserve_client_ip":       "false",
	// 				"connection_termination":   "false",
	// 				"draining_interval":        "300",
	// 				"tls":                      "true",
	// 				"hc-protocol":              "HTTPS",
	// 				"target-port":              fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":             fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                  fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		By("deleting target pod (t5/t7.1 — SIGTERM triggers readyz→503)")
	// 		t5 := time.Now()
	// 		t71 := t5
	// 		err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
	// 		framework.ExpectNoError(err)

	// 		By("waiting for replacement pod on same node (DaemonSet guarantee)")
	// 		newPod := waitForNewPodFromSet(ctx, cs, ns.Name, deployName, knownServers)
	// 		newPodObj, npErr := cs.CoreV1().Pods(ns.Name).Get(ctx, newPod, metav1.GetOptions{})
	// 		if npErr == nil {
	// 			podNodeMap[newPod] = newPodObj.Spec.NodeName
	// 			if newPodObj.Spec.NodeName != targetNode {
	// 				framework.Logf("WARNING: [daemonset] replacement pod %s landed on %s, expected %s",
	// 					newPod, newPodObj.Spec.NodeName, targetNode)
	// 			} else {
	// 				framework.Logf("[daemonset] replacement pod %s on same node %s (verified)", newPod, targetNode)
	// 			}
	// 		}

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = newPod
	// 		tl.PodNodeMap = podNodeMap

	// 		report := buildReport("5.5-SDK-multi-kas-tls (Multi-Client + KAS TG Config + TLS / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())
	// 		report += buildVerdict55(tl, allRecords)
	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// // ── Scenario 5.5-SDK-multi-kas-ctl ───────────────────────────────────
	// // Same as 5.5-SDK-multi-kas but triggers shutdown via ctl exec (SIGUSR1)
	// // on a named pod, then in-place container restart (SIGUSR2) — no pod delete.
	// // TCP reopens in seconds on the same NLB target during NLB propagation window.
	// Context("SDK-managed NLB pre-readyz routing, multi-client KAS-config ctl restart (OCPBUGS-86789)", func() {
	// 	It("should not route to pre-readyz targets "+
	// 		"with ctl-driven shutdown and in-place container restart on a single backend", func(ctx context.Context) {

	// 		image := os.Getenv(envHealthserverImage)
	// 		if image == "" {
	// 			Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	// 		}

	// 		var replicas int32
	// 		startupDelay := 60 * time.Second
	// 		shutdownDelay := kasShutdownDelay
	// 		deployName := "healthserver"
	// 		clientDSName := "healthtest-client"

	// 		var sdkNLB *SDKManagedNLB
	// 		var sgRuleID string
	// 		var masterSGID string
	// 		DeferCleanup(func(cleanupCtx context.Context) {
	// 			framework.Logf("cleaning up SDK-multi-kas-ctl test resources")
	// 			if sdkNLB != nil {
	// 				elbC, err := createAWSClientLoadBalancer(cleanupCtx)
	// 				if err == nil {
	// 					ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
	// 					if ec2Err != nil {
	// 						framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
	// 					}
	// 					deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
	// 				}
	// 			}
	// 			if sgRuleID != "" && masterSGID != "" {
	// 				ec2C, err := createAWSClientEC2(cleanupCtx)
	// 				if err == nil {
	// 					removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
	// 				}
	// 			}
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
	// 			_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, clientDSName, metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 			_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
	// 		})

	// 		By("deploying aggregator pod + service on worker node")
	// 		aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	// 		By("granting privileged SCC to default service account")
	// 		grantHostNetworkSCC(ctx, cs, ns.Name)

	// 		By("creating healthserver DaemonSet (scheduled on master nodes, hostNetwork)")
	// 		ds := buildHealthserverDaemonSet(ns.Name, deployName, startupDelay, image, aggregatorURL)
	// 		var setupTimes transitionTimeline
	// 		setupTimes.T0 = time.Now()
	// 		_, err := cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	// 		framework.ExpectNoError(err, "create daemonset")

	// 		By("waiting for DaemonSet rollout")
	// 		replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	// 		framework.ExpectNoError(err, "daemonset rollout")
	// 		setupTimes.T1 = time.Now()

	// 		By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	// 		ec2Client, err := createAWSClientEC2(ctx)
	// 		framework.ExpectNoError(err, "create EC2 client")
	// 		elbClient, err := createAWSClientLoadBalancer(ctx)
	// 		framework.ExpectNoError(err, "create ELB client")

	// 		infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	// 		framework.ExpectNoError(err, "discover cluster infrastructure")

	// 		By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	// 		masterSGID = infra.MasterSGID
	// 		sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "add SG inbound rule")

	// 		By("creating SDK-managed NLB with instance:19443 targets")
	// 		sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort))
	// 		framework.ExpectNoError(err, "create SDK-managed NLB")
	// 		sdkNLB.SGID = masterSGID
	// 		sdkNLB.SGRuleID = sgRuleID
	// 		setupTimes.T2 = time.Now()

	// 		By("applying KAS-equivalent TG attributes (conn_term=false, draining=300s, preserve_client_ip=false)")
	// 		err = setTGKASAttributes(ctx, elbClient, sdkNLB.TGARN, false)
	// 		framework.ExpectNoError(err, "set KAS TG attributes")

	// 		observer := health.NewObserver(elbClient, 1*time.Second)
	// 		err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	// 		framework.ExpectNoError(err, "discover target group")

	// 		By("waiting for ALL SDK NLB targets to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "all SDK NLB targets healthy")
	// 		setupTimes.T3 = time.Now()

	// 		By("deploying client DaemonSet on worker nodes (one pod per worker)")
	// 		clientPodNames := deployClientDaemonSet(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
	// 			defaultClientWorkers, defaultClientInterval)

	// 		svcCfg := serviceConfig{
	// 			LBDNS:        sdkNLB.NLBDNS,
	// 			LBARN:        sdkNLB.NLBARN,
	// 			TGARN:        observer.TargetGroupARN(),
	// 			TGTargetType: observer.TargetType(),
	// 			Platform:     "AWS",
	// 			ServiceAnnotations: map[string]string{
	// 				"sdk-managed":              "true",
	// 				"client-mode":              "multi-client-daemonset",
	// 				"restart-mode":             "ctl-in-place",
	// 				"preserve_client_ip":       "false",
	// 				"connection_termination":   "false",
	// 				"draining_interval":        "300",
	// 				"target-port":              fmt.Sprintf("%d", healthserverPort),
	// 				"traffic-port":             fmt.Sprintf("%d", healthserverPort),
	// 				"hc-port":                  fmt.Sprintf("%d", healthserverPort),
	// 				"same-port-traffic-and-hc": "true",
	// 			},
	// 		}
	// 		if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
	// 			svcCfg.Region = region
	// 		}
	// 		if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
	// 			if isExternal {
	// 				svcCfg.Topology = "External (HyperShift)"
	// 			} else {
	// 				svcCfg.Topology = "HighlyAvailable"
	// 			}
	// 		}
	// 		observer.Start(ctx)
	// 		stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	// 		defer func() { stopTGPush(); observer.Stop() }()

	// 		By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		steadyRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		steadyNonReady := 0
	// 		for _, r := range steadyRecords {
	// 			if r.IsNonReadyReq {
	// 				steadyNonReady++
	// 			}
	// 		}
	// 		Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	// 		By("listing pods to identify target for rollout simulation")
	// 		pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
	// 			LabelSelector: fmt.Sprintf("app=%s", deployName),
	// 		})
	// 		framework.ExpectNoError(err, "list healthserver pods")
	// 		Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	// 		knownServers := make(map[string]bool)
	// 		podNodeMap := make(map[string]string)
	// 		for _, p := range pods.Items {
	// 			knownServers[p.Name] = true
	// 			podNodeMap[p.Name] = p.Spec.NodeName
	// 		}
	// 		targetPod := pods.Items[0].Name
	// 		targetNode := pods.Items[0].Spec.NodeName

	// 		restartBaseline, err := getContainerRestartCount(ctx, f, ns.Name, targetPod)
	// 		framework.ExpectNoError(err, "read baseline container restart count")

	// 		By("signaling target via ctl readyz-false (t5 — SIGUSR1, single backend)")
	// 		t5 := time.Now()
	// 		err = execHealthserverCtl(ctx, f, ns.Name, targetPod, "readyz-false")
	// 		framework.ExpectNoError(err, "ctl readyz-false")

	// 		By("waiting for TG to detect unhealthy target")
	// 		waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	// 		By(fmt.Sprintf("observing shutdown drain for %s before in-place restart", shutdownDrainObserve90sec))
	// 		time.Sleep(shutdownDrainObserve90sec)

	// 		By("restarting healthserver in-place via ctl restart (t7.1 — SIGUSR2)")
	// 		t71 := time.Now()
	// 		err = execHealthserverCtl(ctx, f, ns.Name, targetPod, "restart")
	// 		framework.ExpectNoError(err, "ctl restart")

	// 		By("waiting for container restart")
	// 		err = waitForContainerRestart(ctx, f, ns.Name, targetPod, restartBaseline)
	// 		framework.ExpectNoError(err, "container restarted")

	// 		By("waiting for restarted target to become healthy")
	// 		err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	// 		framework.ExpectNoError(err, "restarted target healthy")

	// 		By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	// 		time.Sleep(postHealthyObserve)

	// 		allRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	// 		allEvents := observer.Events()

	// 		tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents, true)
	// 		tl.T0 = setupTimes.T0
	// 		tl.T1 = setupTimes.T1
	// 		tl.T2 = setupTimes.T2
	// 		tl.T3 = setupTimes.T3
	// 		for _, r := range steadyRecords {
	// 			if r.Error == "" && r.HTTPStatus > 0 {
	// 				tl.T4 = r.Timestamp
	// 				break
	// 			}
	// 		}
	// 		tl.TargetPod = targetPod
	// 		tl.TargetNode = targetNode
	// 		tl.NewPod = targetPod

	// 		report := buildReport("5.5-SDK-multi-kas-ctl (Multi-Client + KAS TG + ctl in-place restart / OCPBUGS-86789)",
	// 			tl, svcCfg, replicas, startupDelay, shutdownDelay,
	// 			allRecords, allEvents, observer.Snapshots())
	// 		report += buildVerdict55(tl, allRecords)
	// 		framework.Logf("\n%s", report)
	// 	})
	// })

	// ── Scenario 5.5-SDK-multi-kas-tls-ctl ───────────────────────────────
	// Most faithful KAS repro: KAS TG attributes + TLS/HTTPS HC + ctl in-place
	// restart (v22 TLS + v23 ctl). Drain duration variants isolate NLB propagation overlap.
	Context("SDK-managed NLB pre-readyz routing, multi-client KAS-config TLS ctl restart (OCPBUGS-86789)", func() {
		for _, tc := range []struct {
			observe time.Duration
			label   string
		}{
			{shutdownDrainObserve15sec, "15s"},
			{shutdownDrainObserve30sec, "30s"},
			{shutdownDrainObserve60sec, "60s"},
			{shutdownDrainObserve90sec, "90s"},
			{shutdownDrainObserve129sec, "129s"},
			{shutdownDrainObserve150sec, "150s"},
			{shutdownDrainObserve180sec, "180s"},
			{shutdownDrainObserve210sec, "210s"},
			{shutdownDrainObserve240sec, "240s"},
			{shutdownDrainObserve300sec, "300s"},
			{shutdownDrainObserve600sec, "600s"},
		} {
			tc := tc
			It(fmt.Sprintf("should not route to pre-readyz targets "+
				"with KAS TG attributes, TLS traffic, HTTPS HC, ctl-driven in-place container restart, and drain %s", tc.label),
				func(ctx context.Context) {
					// use default KAS config, next test will explore variants
					runSDKMultiKasTLSCtlDrainTest(ctx, f, cs, ns, tc.observe, tc.label, &testKASPatch{
						lbCrossZoneEnabled:                     true,
						tgDesregDelayTimeoutSec:                300,
						tgDesregConnectTermEnabled:             false,
						tgPreserveClientIPEnabled:              false,
						tgHealthStateUnhealthyConnTermEnabled:  false,
						tgHealthStateUnhealthyDrainIntervalSec: 300,
					})
				})
		}
	})
	// ── Scenario 5.5-SDK-multi-kas-tls-ctl + fine tune LB/TG cfg───────────────
	// Most faithful KAS repro: KAS TG attributes + TLS/HTTPS HC + ctl in-place
	// Exercise LB and TG configurations from the KAS LB/TG attribs to identify
	// opportunities to improve existing configuration.
	Context("SDK-managed NLB pre-readyz routing, multi-client KAS-patch TLS ctl restart (OCPBUGS-86789)", func() {
		// variant: lower Unhealthy draining interval: 30 sec (300 baseline)
		kPatchDraining30 := testKASPatch{
			lbCrossZoneEnabled:                     true,
			tgPreserveClientIPEnabled:              false,
			tgDesregDelayTimeoutSec:                30,
			tgDesregConnectTermEnabled:             false,
			tgHealthStateUnhealthyDrainIntervalSec: 30,
			tgHealthStateUnhealthyConnTermEnabled:  false,
		}
		// variant: lower Unhealthy draining interval: 90 sec (300 baseline)
		kPatchDraining90 := testKASPatch{
			lbCrossZoneEnabled:                     true,
			tgPreserveClientIPEnabled:              false,
			tgDesregDelayTimeoutSec:                90,
			tgDesregConnectTermEnabled:             false,
			tgHealthStateUnhealthyDrainIntervalSec: 90,
			tgHealthStateUnhealthyConnTermEnabled:  false,
		}
		// variant: disable draining, enable connection termination
		kPatchDrainingDisabled := testKASPatch{
			lbCrossZoneEnabled:                     true,
			tgPreserveClientIPEnabled:              false,
			tgDesregDelayTimeoutSec:                0,
			tgDesregConnectTermEnabled:             true,
			tgHealthStateUnhealthyDrainIntervalSec: 0,
			tgHealthStateUnhealthyConnTermEnabled:  true,
		}

		for _, tc := range []struct {
			observe  time.Duration
			label    string
			kasPatch testKASPatch
		}{
			{shutdownDrainObserve30sec, "30s", kPatchDraining30},
			{shutdownDrainObserve30sec, "30s", kPatchDraining90},
			{shutdownDrainObserve30sec, "30s", kPatchDrainingDisabled},
			{shutdownDrainObserve60sec, "60s", kPatchDraining30},
			{shutdownDrainObserve60sec, "60s", kPatchDraining90},
			{shutdownDrainObserve60sec, "60s", kPatchDrainingDisabled},
			{shutdownDrainObserve90sec, "90s", kPatchDraining30},
			{shutdownDrainObserve90sec, "90s", kPatchDraining90},
			{shutdownDrainObserve90sec, "90s", kPatchDrainingDisabled},
			{shutdownDrainObserve129sec, "129s", kPatchDraining30},
			{shutdownDrainObserve129sec, "129s", kPatchDraining90},
			{shutdownDrainObserve129sec, "129s", kPatchDrainingDisabled},
			{shutdownDrainObserve150sec, "150s", kPatchDraining30},
			{shutdownDrainObserve150sec, "150s", kPatchDraining90},
			{shutdownDrainObserve150sec, "150s", kPatchDrainingDisabled},
			{shutdownDrainObserve180sec, "180s", kPatchDraining30},
			{shutdownDrainObserve180sec, "180s", kPatchDraining90},
			{shutdownDrainObserve180sec, "180s", kPatchDrainingDisabled},
			{shutdownDrainObserve210sec, "210s", kPatchDraining30},
			{shutdownDrainObserve210sec, "210s", kPatchDraining90},
			{shutdownDrainObserve210sec, "210s", kPatchDrainingDisabled},
			{shutdownDrainObserve240sec, "240s", kPatchDraining30},
			{shutdownDrainObserve240sec, "240s", kPatchDraining90},
			{shutdownDrainObserve240sec, "240s", kPatchDrainingDisabled},
			{shutdownDrainObserve300sec, "300s", kPatchDraining30},
			{shutdownDrainObserve300sec, "300s", kPatchDraining90},
			{shutdownDrainObserve300sec, "300s", kPatchDrainingDisabled},
		} {
			tc := tc
			It(fmt.Sprintf("should not route to pre-readyz targets "+
				"drain %s, patch "+
				"lbCrossZone=%v "+
				"tgDesDelay=%d "+
				"tgDesConnTerm=%v "+
				"tgPreserveCIP=%v "+
				"tgUnhealthyDelay=%d "+
				"tgUnhealthyConnTerm=%v", tc.label,
				tc.kasPatch.lbCrossZoneEnabled,
				tc.kasPatch.tgDesregDelayTimeoutSec,
				tc.kasPatch.tgDesregConnectTermEnabled,
				tc.kasPatch.tgPreserveClientIPEnabled,
				tc.kasPatch.tgHealthStateUnhealthyDrainIntervalSec,
				tc.kasPatch.tgHealthStateUnhealthyConnTermEnabled,
			),
				func(ctx context.Context) {
					runSDKMultiKasTLSCtlDrainTest(ctx, f, cs, ns, tc.observe, tc.label, &tc.kasPatch)
				})
		}
	})
})

type testKASPatch struct {
	lbCrossZoneEnabled                     bool
	tgDesregDelayTimeoutSec                int
	tgDesregConnectTermEnabled             bool
	tgPreserveClientIPEnabled              bool
	tgHealthStateUnhealthyConnTermEnabled  bool
	tgHealthStateUnhealthyDrainIntervalSec int
}

// runSDKMultiKasTLSCtlDrainTest runs 5.5-SDK-multi-kas-tls-ctl with a configurable
// post-unhealthy drain before ctl in-place restart.
func runSDKMultiKasTLSCtlDrainTest(
	ctx context.Context,
	f *framework.Framework,
	cs clientset.Interface,
	ns *v1.Namespace,
	drainObserve time.Duration,
	drainLabel string,
	kasPatch *testKASPatch,
) {
	image := os.Getenv(envHealthserverImage)
	if image == "" {
		Skip(fmt.Sprintf("%s not set", envHealthserverImage))
	}

	var replicas int32
	startupDelay := 60 * time.Second
	shutdownDelay := kasShutdownDelay
	deployName := "healthserver"
	clientDSName := "healthtest-client"

	var sdkNLB *SDKManagedNLB
	var sgRuleID string
	var masterSGID string
	DeferCleanup(func(cleanupCtx context.Context) {
		framework.Logf("cleaning up SDK-multi-kas-tls-ctl (drain %s) test resources", drainLabel)
		if sdkNLB != nil {
			elbC, err := createAWSClientLoadBalancer(cleanupCtx)
			if err == nil {
				ec2C, ec2Err := createAWSClientEC2(cleanupCtx)
				if ec2Err != nil {
					framework.Logf("WARNING: failed to create EC2 client for NLB cleanup: %v", ec2Err)
				}
				deleteSDKManagedNLB(cleanupCtx, elbC, ec2C, sdkNLB)
			}
		}
		if sgRuleID != "" && masterSGID != "" {
			ec2C, err := createAWSClientEC2(cleanupCtx)
			if err == nil {
				removeSGIngressRule(cleanupCtx, ec2C, masterSGID, sgRuleID)
			}
		}
		_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
		_ = cs.AppsV1().DaemonSets(ns.Name).Delete(cleanupCtx, clientDSName, metav1.DeleteOptions{})
		_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
		_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
		_ = cs.CoreV1().ConfigMaps(ns.Name).Delete(cleanupCtx, healthserverTLSConfigMap, metav1.DeleteOptions{})
	})

	By("deploying aggregator pod + service on worker node")
	aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)

	By("granting privileged SCC to default service account")
	grantHostNetworkSCC(ctx, cs, ns.Name)

	By("creating TLS cert ConfigMap for healthserver")
	err := ensureHealthserverTLSConfigMap(ctx, cs, ns.Name)
	framework.ExpectNoError(err, "create TLS configmap")

	By("creating healthserver DaemonSet with TLS (scheduled on master nodes, hostNetwork)")
	ds := buildHealthserverDaemonSetTLS(ns.Name, deployName, startupDelay, image, aggregatorURL)
	var setupTimes transitionTimeline
	setupTimes.T0 = time.Now()
	_, err = cs.AppsV1().DaemonSets(ns.Name).Create(ctx, ds, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create daemonset")

	By("waiting for DaemonSet rollout")
	replicas, err = waitForDaemonSetReady(ctx, cs, ns.Name, deployName, 5*time.Minute)
	framework.ExpectNoError(err, "daemonset rollout")
	setupTimes.T1 = time.Now()

	By("discovering cluster infrastructure (VPC, subnets, master instances, SG)")
	ec2Client, err := createAWSClientEC2(ctx)
	framework.ExpectNoError(err, "create EC2 client")
	elbClient, err := createAWSClientLoadBalancer(ctx)
	framework.ExpectNoError(err, "create ELB client")

	infra, err := discoverClusterInfra(ctx, cs, ec2Client)
	framework.ExpectNoError(err, "discover cluster infrastructure")

	By(fmt.Sprintf("adding SG inbound rule for TCP %d on master SG %s", healthserverPort, infra.MasterSGID))
	masterSGID = infra.MasterSGID
	sgRuleID, err = addSGIngressRule(ctx, ec2Client, infra.MasterSGID, int32(healthserverPort))
	framework.ExpectNoError(err, "add SG inbound rule")

	By("creating SDK-managed NLB with instance:19443 targets and HTTPS HC")
	sdkNLB, err = createSDKManagedNLB(ctx, elbClient, ec2Client, infra, int32(healthserverPort), kasPatch, SDKNLBCreateOpts{
		HealthCheckProtocol: elbv2types.ProtocolEnumHttps,
	})
	framework.ExpectNoError(err, "create SDK-managed NLB")
	sdkNLB.SGID = masterSGID
	sdkNLB.SGRuleID = sgRuleID
	setupTimes.T2 = time.Now()

	// TODO move set TG before create NLB, after create TG
	By("applying KAS-equivalent TG attributes (conn_term=false, draining=300s, preserve_client_ip=false)")
	err = setTGKASAttributes(ctx, elbClient, sdkNLB.TGARN, kasPatch)
	framework.ExpectNoError(err, "set KAS TG attributes")

	observer := health.NewObserver(elbClient, 1*time.Second)
	err = observer.DiscoverTargetGroup(ctx, sdkNLB.NLBARN)
	framework.ExpectNoError(err, "discover target group")

	By("waiting for ALL SDK NLB targets to become healthy")
	err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	framework.ExpectNoError(err, "all SDK NLB targets healthy")
	setupTimes.T3 = time.Now()

	/*
		Generate traffic to the load balancer.
	*/

	By("deploying client DaemonSet on worker nodes (TLS to NLB, one pod per worker)")
	clientPodNames := deployClientDaemonSet(ctx, cs, ns.Name, image, sdkNLB.NLBDNS, aggregatorURL,
		defaultClientWorkers, defaultClientInterval, true)

	svcCfg := serviceConfig{
		LBDNS:        sdkNLB.NLBDNS,
		LBARN:        sdkNLB.NLBARN,
		TGARN:        observer.TargetGroupARN(),
		TGTargetType: observer.TargetType(),
		Platform:     "AWS",
		ServiceAnnotations: map[string]string{
			"sdk-managed":              "true",
			"client-mode":              "multi-client-daemonset",
			"restart-mode":             "ctl-in-place",
			"preserve_client_ip":       "false",
			"connection_termination":   "false",
			"draining_interval":        "300",
			"tls":                      "true",
			"hc-protocol":              "HTTPS",
			"shutdown-drain-observe":   drainObserve.String(),
			"target-port":              fmt.Sprintf("%d", healthserverPort),
			"traffic-port":             fmt.Sprintf("%d", healthserverPort),
			"hc-port":                  fmt.Sprintf("%d", healthserverPort),
			"same-port-traffic-and-hc": "true",
		},
	}
	if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
		svcCfg.Region = region
	}
	if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
		if isExternal {
			svcCfg.Topology = "External (HyperShift)"
		} else {
			svcCfg.Topology = "HighlyAvailable"
		}
	}
	observer.Start(ctx)
	stopTGPush := startTGSnapshotPusher(ctx, cs, ns.Name, observer)
	defer func() { stopTGPush(); observer.Stop() }()

	By(fmt.Sprintf("verifying steady state for %s", postHealthyObserve))
	time.Sleep(postHealthyObserve)

	steadyRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	steadyNonReady := 0
	for _, r := range steadyRecords {
		if r.IsNonReadyReq {
			steadyNonReady++
		}
	}
	// do we really need to fail here when non-zero? Goal is to collect data not fail at this time.
	Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

	/*
		START rollout simulation
	*/

	By("listing pods to identify target for rollout simulation")
	pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", deployName),
	})
	framework.ExpectNoError(err, "list healthserver pods")
	Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

	knownServers := make(map[string]bool)
	for _, p := range pods.Items {
		knownServers[p.Name] = true
	}
	targetPod := pods.Items[0].Name
	targetNode := pods.Items[0].Spec.NodeName

	restartBaseline, err := getContainerRestartCount(ctx, f, ns.Name, targetPod)
	framework.ExpectNoError(err, "read baseline container restart count")

	By("signaling target via ctl readyz-false (t5 — SIGUSR1, single backend)")
	t5 := time.Now()
	err = execHealthserverCtl(ctx, f, ns.Name, targetPod, "readyz-false")
	framework.ExpectNoError(err, "ctl readyz-false")

	By("waiting for TG to detect unhealthy target")
	waitForTGUnhealthy(ctx, observer, 3*time.Minute)

	By(fmt.Sprintf("observing shutdown drain for %s before in-place restart", drainObserve))
	time.Sleep(drainObserve)

	By("restarting healthserver in-place via ctl restart (t7.1 — SIGUSR2)")
	t71 := time.Now()
	err = execHealthserverCtl(ctx, f, ns.Name, targetPod, "restart")
	framework.ExpectNoError(err, "ctl restart")

	By("waiting for container restart")
	err = waitForContainerRestart(ctx, f, ns.Name, targetPod, restartBaseline)
	framework.ExpectNoError(err, "container restarted")

	By("waiting for restarted target to become healthy")
	err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	framework.ExpectNoError(err, "restarted target healthy")

	/*
		END rollout simulation
	*/

	By(fmt.Sprintf("observing post-recovery traffic for %s", postHealthyObserve))
	time.Sleep(postHealthyObserve)

	/*
		AGGREGATE results
	*/

	allRecords := fetchMergedClientRecords(ctx, cs, ns.Name, clientPodNames)
	allEvents := observer.Events()

	tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents, true)
	tl.T0 = setupTimes.T0
	tl.T1 = setupTimes.T1
	tl.T2 = setupTimes.T2
	tl.T3 = setupTimes.T3
	for _, r := range steadyRecords {
		if r.Error == "" && r.HTTPStatus > 0 {
			tl.T4 = r.Timestamp
			break
		}
	}
	tl.TargetPod = targetPod
	tl.TargetNode = targetNode
	tl.NewPod = targetPod

	report := buildReport(fmt.Sprintf(
		"5.5-SDK-multi-kas-tls-ctl drain %s (Multi-Client + KAS TG + TLS + ctl in-place restart / OCPBUGS-86789)",
		drainLabel),
		tl, svcCfg, replicas, startupDelay, shutdownDelay,
		allRecords, allEvents, observer.Snapshots())
	report += buildVerdict55(tl, allRecords)
	framework.Logf("\n%s", report)
}

// ─── Setup helper ───────────────────────────────────────────────────────────

// setupHealthTransition creates the healthserver Deployment and NLB Service,
// discovers the TG, fetches TG config, and waits for ALL TG targets to be
// healthy before returning. Pods are scheduled on master/control-plane nodes
// to match KAS topology. The NLB targets only master nodes via the
// target-node-labels annotation. Cross-zone load balancing is enabled.
func setupHealthTransition(
	ctx context.Context,
	cs clientset.Interface,
	ns *v1.Namespace,
	deployName, svcName, image string,
	replicas int32,
	startupDelay time.Duration,
) (lbDNS string, observer *health.Observer, cfg serviceConfig, setupTimes transitionTimeline, clientPodName string) {

	// Deploy the aggregator first — servers and client will connect to it.
	// The aggregator runs on a worker node with normal networking.
	By("deploying aggregator pod + service on worker node")
	aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)
	framework.Logf("[aggregator] ready at %s", aggregatorURL)

	// Grant the default SA in this namespace permission to use hostNetwork
	// via the OpenShift hostnetwork-v2 SCC. Required because the healthserver
	// pod uses hostNetwork: true to match KAS static pod behavior.
	By("granting privileged SCC to default service account")
	grantHostNetworkSCC(ctx, cs, ns.Name)

	// t0: deployment created — pods begin scheduling on master nodes
	By("creating healthserver Deployment (scheduled on master nodes, hostNetwork)")
	deploy := buildHealthserverDeployment(ns.Name, deployName, replicas, startupDelay, image, aggregatorURL)
	setupTimes.T0 = time.Now()
	_, err := cs.AppsV1().Deployments(ns.Name).Create(ctx, deploy, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create deployment")

	By("creating NLB Service (master-only targets, cross-zone, /readyz HC)")
	svc := buildHealthTransitionService(ns.Name, svcName, deployName)
	_, err = cs.CoreV1().Services(ns.Name).Create(ctx, svc, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create service")
	cfg.ServiceAnnotations = svc.Annotations

	// Populate environment summary from the cluster's Infrastructure resource
	cfg.Platform = "AWS"
	if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
		cfg.Region = region
	}
	if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
		if isExternal {
			cfg.Topology = "External (HyperShift)"
		} else {
			cfg.Topology = "HighlyAvailable"
		}
	}

	DeferCleanup(func(cleanupCtx context.Context) {
		framework.Logf("cleaning up health transition resources")
		// Clean up all pods/deployments/services created by the test.
		// Order: delete NLB service first (triggers LB deletion), then
		// pods, then wait for LB to be fully removed from AWS.
		_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, svcName, metav1.DeleteOptions{})
		_ = cs.AppsV1().Deployments(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
		_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
		_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
		_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-client", metav1.DeleteOptions{})
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
	// t1: all pods running (startup-delay may still be in progress)
	setupTimes.T1 = time.Now()

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
	// t2: NLB provisioned, DNS assigned
	setupTimes.T2 = time.Now()
	cfg.LBDNS = lbDNS

	By("discovering NLB and target group in AWS")
	elbClient, err := createAWSClientLoadBalancer(ctx)
	framework.ExpectNoError(err, "create ELB client")

	foundLB, err := getAWSLoadBalancerFromDNSName(ctx, elbClient, lbDNS)
	framework.ExpectNoError(err, "find NLB")
	cfg.LBARN = aws.ToString(foundLB.LoadBalancerArn)

	observer = health.NewObserver(elbClient, 1*time.Second)
	err = observer.DiscoverTargetGroup(ctx, cfg.LBARN)
	framework.ExpectNoError(err, "discover target group")
	cfg.TGARN = observer.TargetGroupARN()
	cfg.TGTargetType = observer.TargetType()

	// Wait for ALL registered TG targets to be healthy (not just N replicas).
	// With master-only node targeting, this should be exactly 3 targets.
	// Previously we waited for minHealthy=replicas which could pass with
	// worker-node targets while master-node targets were still "initial".
	By("waiting for ALL TG targets to become healthy")
	err = waitForAllTGTargetsHealthy(ctx, observer, 10*time.Minute)
	framework.ExpectNoError(err, "all TG targets healthy")
	// t3: all TG targets healthy — HC passed and propagated through Hyperplane
	setupTimes.T3 = time.Now()

	// Deploy in-cluster client on a worker node. The client sends requests
	// to the NLB with ~1ms RTT (vs ~430ms from external), achieving much
	// higher throughput for better detection coverage.
	By("deploying in-cluster client on worker node")
	clientPodName = deployInClusterClient(ctx, cs, ns.Name, image, lbDNS, aggregatorURL,
		defaultClientWorkers, defaultClientInterval)

	return lbDNS, observer, cfg, setupTimes, clientPodName
}

// waitForAllTGTargetsHealthy polls DescribeTargetHealth directly (via
// observer.PollOnce) until every registered target reports healthy.
// Logs per-target state every 10s so the operator can see convergence.
// This works both during setup (observer not started) and during the test
// (observer running — PollOnce is independent of the background loop).
func waitForAllTGTargetsHealthy(ctx context.Context, observer *health.Observer, timeout time.Duration) error {
	lastLog := time.Time{}
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		snap, err := observer.PollOnce(ctx)
		if err != nil {
			framework.Logf("[tg-wait] poll error: %v", err)
			return false, nil
		}

		total := snap.HealthyCount + snap.UnhealthyCount + snap.InitialCount + snap.DrainingCount
		allHealthy := total > 0 && snap.UnhealthyCount == 0 && snap.InitialCount == 0 && snap.DrainingCount == 0

		// Log every 10s or on state change, showing per-target detail
		if time.Since(lastLog) >= 10*time.Second || allHealthy {
			var details []string
			for id, state := range snap.Targets {
				details = append(details, fmt.Sprintf("%s=%s", id, state))
			}
			framework.Logf("[tg-wait] healthy=%d unhealthy=%d initial=%d total=%d | %s",
				snap.HealthyCount, snap.UnhealthyCount, snap.InitialCount, total,
				strings.Join(details, ", "))
			lastLog = time.Now()
		}

		if allHealthy {
			framework.Logf("[tg-wait] all %d targets healthy", snap.HealthyCount)
		}
		return allHealthy, nil
	})
}

// waitForTGUnhealthy blocks until at least one TG target reports unhealthy.
// This ensures the NLB HC has detected the failure before we start waiting
// for recovery. Without this, waitForAllTGTargetsHealthy may return
// immediately if called before the HC threshold is met.
func waitForTGUnhealthy(ctx context.Context, observer *health.Observer, timeout time.Duration) {
	_ = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		snap, err := observer.PollOnce(ctx)
		if err != nil {
			return false, nil
		}
		if snap.UnhealthyCount > 0 {
			framework.Logf("[tg-wait] detected %d unhealthy target(s)", snap.UnhealthyCount)
			return true, nil
		}
		return false, nil
	})
}

// startTGSnapshotPusher starts a goroutine that pushes TG health snapshots
// to the aggregator every 2 seconds. This runs the observer's PollOnce and
// sends the result to the aggregator so all TG state changes are captured
// in the aggregator's timeline. Returns a cancel function to stop the goroutine.
func startTGSnapshotPusher(ctx context.Context, cs clientset.Interface, namespace string, observer *health.Observer) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap, err := observer.PollOnce(ctx)
				if err != nil {
					continue
				}
				pushTGSnapshotToAggregator(ctx, cs, namespace, snap)
			}
		}
	}()
	return cancel
}

// populateAWSReportConfig fills cfg.AWS* slices from ELBv2/EC2 Describe* APIs.
// Called at report time so the dump reflects live AWS state (including post-test
// ModifyTargetGroupAttributes such as CAPA or KAS attrs).
func populateAWSReportConfig(ctx context.Context, cfg *serviceConfig) {
	if cfg.LBARN == "" || !strings.HasPrefix(cfg.LBARN, "arn:aws:elasticloadbalancing") {
		return
	}

	elbClient, err := createAWSClientLoadBalancer(ctx)
	if err != nil {
		framework.Logf("WARNING: populateAWSReportConfig: ELB client: %v", err)
		return
	}

	lbOut, err := elbClient.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{cfg.LBARN},
	})
	if err != nil || len(lbOut.LoadBalancers) == 0 {
		framework.Logf("WARNING: populateAWSReportConfig: DescribeLoadBalancers: %v", err)
		return
	}
	lb := lbOut.LoadBalancers[0]

	cfg.AWSLoadBalancer = append(cfg.AWSLoadBalancer,
		awsReportKV("load_balancer.arn", aws.ToString(lb.LoadBalancerArn)),
		awsReportKV("load_balancer.dns_name", aws.ToString(lb.DNSName)),
		awsReportKV("load_balancer.name", aws.ToString(lb.LoadBalancerName)),
		awsReportKV("load_balancer.type", string(lb.Type)),
		awsReportKV("load_balancer.scheme", string(lb.Scheme)),
		awsReportKV("load_balancer.ip_address_type", string(lb.IpAddressType)),
		awsReportKV("load_balancer.vpc_id", aws.ToString(lb.VpcId)),
		awsReportKV("load_balancer.state", string(lb.State.Code)),
	)
	if lb.CreatedTime != nil {
		cfg.AWSLoadBalancer = append(cfg.AWSLoadBalancer,
			awsReportKV("load_balancer.created_time", lb.CreatedTime.UTC().Format(time.RFC3339)),
		)
	}

	for _, az := range lb.AvailabilityZones {
		zone := aws.ToString(az.ZoneName)
		subnetID := aws.ToString(az.SubnetId)
		cfg.AWSLoadBalancer = append(cfg.AWSLoadBalancer,
			awsReportKV(fmt.Sprintf("load_balancer.availability_zone.%s.subnet_id", zone), subnetID),
		)
		if len(az.LoadBalancerAddresses) > 0 {
			var addrs []string
			for _, addr := range az.LoadBalancerAddresses {
				addrs = append(addrs, aws.ToString(addr.IpAddress))
			}
			cfg.AWSLoadBalancer = append(cfg.AWSLoadBalancer,
				awsReportKV(fmt.Sprintf("load_balancer.availability_zone.%s.load_balancer_addresses", zone),
					strings.Join(addrs, ",")),
			)
		}
	}

	sgIDs := append([]string(nil), lb.SecurityGroups...)
	if len(sgIDs) > 0 {
		cfg.AWSLoadBalancer = append(cfg.AWSLoadBalancer,
			awsReportKV("load_balancer.security_groups", strings.Join(sgIDs, ",")),
		)
	}

	attrOut, err := elbClient.DescribeLoadBalancerAttributes(ctx, &elbv2.DescribeLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(cfg.LBARN),
	})
	if err != nil {
		framework.Logf("WARNING: populateAWSReportConfig: DescribeLoadBalancerAttributes: %v", err)
	} else {
		for _, a := range attrOut.Attributes {
			cfg.AWSLoadBalancerAttrs = append(cfg.AWSLoadBalancerAttrs,
				awsReportKV(aws.ToString(a.Key), aws.ToString(a.Value)),
			)
		}
	}

	tgARN := cfg.TGARN
	if tgARN == "" {
		tgOut, tgErr := elbClient.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
			LoadBalancerArn: aws.String(cfg.LBARN),
		})
		if tgErr != nil || len(tgOut.TargetGroups) == 0 {
			framework.Logf("WARNING: populateAWSReportConfig: discover TG: %v", tgErr)
			return
		}
		tgARN = aws.ToString(tgOut.TargetGroups[0].TargetGroupArn)
	}

	tgOut, err := elbClient.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{tgARN},
	})
	if err != nil || len(tgOut.TargetGroups) == 0 {
		framework.Logf("WARNING: populateAWSReportConfig: DescribeTargetGroups: %v", err)
	} else {
		tg := tgOut.TargetGroups[0]
		cfg.AWSTargetGroup = append(cfg.AWSTargetGroup,
			awsReportKV("target_group.arn", aws.ToString(tg.TargetGroupArn)),
			awsReportKV("target_group.name", aws.ToString(tg.TargetGroupName)),
			awsReportKV("target_group.protocol", string(tg.Protocol)),
			awsReportKV("target_group.port", fmt.Sprintf("%d", aws.ToInt32(tg.Port))),
			awsReportKV("target_group.vpc_id", aws.ToString(tg.VpcId)),
			awsReportKV("target_group.target_type", string(tg.TargetType)),
			awsReportKV("target_group.health_check.enabled", fmt.Sprintf("%t", aws.ToBool(tg.HealthCheckEnabled))),
			awsReportKV("target_group.health_check.protocol", string(tg.HealthCheckProtocol)),
			awsReportKV("target_group.health_check.port", aws.ToString(tg.HealthCheckPort)),
			awsReportKV("target_group.health_check.path", aws.ToString(tg.HealthCheckPath)),
			awsReportKV("target_group.health_check.interval_seconds", fmt.Sprintf("%d", aws.ToInt32(tg.HealthCheckIntervalSeconds))),
			awsReportKV("target_group.health_check.healthy_threshold", fmt.Sprintf("%d", aws.ToInt32(tg.HealthyThresholdCount))),
			awsReportKV("target_group.health_check.unhealthy_threshold", fmt.Sprintf("%d", aws.ToInt32(tg.UnhealthyThresholdCount))),
		)
		if tg.Matcher != nil && tg.Matcher.HttpCode != nil {
			cfg.AWSTargetGroup = append(cfg.AWSTargetGroup,
				awsReportKV("target_group.health_check.matcher.http_code", aws.ToString(tg.Matcher.HttpCode)),
			)
		}
	}

	tgAttrOut, err := elbClient.DescribeTargetGroupAttributes(ctx, &elbv2.DescribeTargetGroupAttributesInput{
		TargetGroupArn: aws.String(tgARN),
	})
	if err != nil {
		framework.Logf("WARNING: populateAWSReportConfig: DescribeTargetGroupAttributes: %v", err)
	} else {
		for _, a := range tgAttrOut.Attributes {
			cfg.AWSTargetGroupAttrs = append(cfg.AWSTargetGroupAttrs,
				awsReportKV(aws.ToString(a.Key), aws.ToString(a.Value)),
			)
		}
	}
}

func awsReportKV(key, value string) health.TGAttribute {
	return health.TGAttribute{Key: key, Value: value}
}

func writeAWSReportSection(w func(format string, args ...any), title string, entries []health.TGAttribute) {
	if len(entries) == 0 {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	w("  --- %s ---", title)
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		w("  %s: %s", e.Key, e.Value)
	}
	w("")
}

func writeE2ETestMetadata(w func(format string, args ...any), annotations map[string]string) {
	w("E2E TEST METADATA")
	if len(annotations) == 0 {
		w("  (none)")
		return
	}
	keys := make([]string, 0, len(annotations))
	for k := range annotations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := annotations[k]
		if strings.HasPrefix(k, "service.beta.kubernetes.io/") {
			short := strings.TrimPrefix(k, "service.beta.kubernetes.io/")
			w("  k8s/%s: %s", short, v)
			continue
		}
		w("  e2e/%s: %s", k, v)
	}
}

// ─── Admin API via K8s API server proxy ─────────────────────────────────────

// sendAdminSignal sends a readyz control signal to a healthserver pod via
// the K8s API server pod proxy. With hostNetwork: true, the pod listens on
// the node's IP on healthserverPort. The API server proxy connects to
// podIP:port which equals nodeIP:port — this requires the API server to be
// able to reach the node on that port (same-node for control-plane pods).
func sendAdminSignal(ctx context.Context, cs clientset.Interface, namespace, podName string, ready bool) error {
	readyStr := "false"
	if ready {
		readyStr = "true"
	}
	result := cs.CoreV1().RESTClient().Post().
		AbsPath(fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy/admin/readyz", namespace, podName, healthserverPort)).
		Param("ready", readyStr).
		Timeout(30 * time.Second).
		Do(ctx)
	if err := result.Error(); err != nil {
		return fmt.Errorf("admin signal ready=%s to %s: %w", readyStr, podName, err)
	}
	framework.Logf("[admin] sent readyz=%s to pod %s", readyStr, podName)
	return nil
}

// ─── Pod lifecycle helpers ──────────────────────────────────────────────────

func waitForNewPod(ctx context.Context, cs clientset.Interface, namespace, deployName, oldPodName string) string {
	var newPod string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", deployName),
		})
		if err != nil {
			return false, nil
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Name == oldPodName || p.DeletionTimestamp != nil {
				continue
			}
			if p.Status.Phase == v1.PodRunning {
				newPod = p.Name
				return true, nil
			}
		}
		return false, nil
	})
	framework.ExpectNoError(err, "wait for replacement pod")
	return newPod
}

// waitForNewPodFromSet waits for a Running pod whose name is NOT in knownPods.
// Use this instead of waitForNewPod when the workload is a DaemonSet: unlike a
// Deployment, the other DaemonSet pods are already Running and share the same
// label, so waitForNewPod would immediately return one of them.
func waitForNewPodFromSet(ctx context.Context, cs clientset.Interface, namespace, deployName string, knownPods map[string]bool) string {
	var newPod string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", deployName),
		})
		if err != nil {
			return false, nil
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if knownPods[p.Name] || p.DeletionTimestamp != nil {
				continue
			}
			if p.Status.Phase == v1.PodRunning {
				newPod = p.Name
				return true, nil
			}
		}
		return false, nil
	})
	framework.ExpectNoError(err, "wait for replacement pod (from known set)")
	return newPod
}

// ─── Timeline computation ───────────────────────────────────────────────────

// isUnhealthyState returns true for any unhealthy TG state, including
// "unhealthy.draining" which occurs when connection_termination.enabled=false
// (CAPA fix / OCPBUGS-55626).
func isUnhealthyState(state string) bool {
	return strings.HasPrefix(state, "unhealthy")
}

func parseServerStartTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
	}
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func isRestartProcess(r health.RequestRecord, restartAt time.Time) bool {
	start, ok := parseServerStartTime(r.ServerStartTime)
	if !ok {
		return r.Timestamp.After(restartAt)
	}
	return start.After(restartAt) || start.Equal(restartAt)
}

// computeTimeline builds the full timing model for Scenario 5.5 from raw
// client records and observer events. See transitionTimeline for the t-value
// definitions aligned with the SPLAT-307 state machine.
//
// Pass inPlaceRestart=true when the target pod is restarted in-place via
// ctl (same ServerID/POD_NAME); t71 must be the ctl restart time, after t5.
func computeTimeline(
	oldPod string,
	knownServers map[string]bool,
	t5, t71 time.Time,
	records []health.RequestRecord,
	events []health.HealthEvent,
	inPlaceRestart ...bool,
) transitionTimeline {
	inPlace := len(inPlaceRestart) > 0 && inPlaceRestart[0]
	tl := transitionTimeline{T5: t5, T71: t71}

	// t6: first observer event showing a target transitioning healthy→unhealthy
	// AFTER t5 (when we signaled readyz→503). Excludes initial→unhealthy which
	// are nodes that never had local pods and failed HC from the start.
	for _, e := range events {
		if e.Timestamp.Before(t5) {
			continue
		}
		if isUnhealthyState(e.State) && e.PrevState == "healthy" {
			tl.T6 = e.Timestamp
			break
		}
	}

	// t7: last request served by the target during graceful shutdown (t5→t71).
	// Each request after readyz→503 counts as an "unhealthy" request.
	shutdownEnd := time.Time{}
	if inPlace && t71.After(t5) {
		shutdownEnd = t71
	}
	lateThreshold := t5.Add(time.Duration(float64(kasShutdownDelay) * 0.8))
	for _, r := range records {
		if r.Timestamp.Before(t5) {
			continue
		}
		if !shutdownEnd.IsZero() && !r.Timestamp.Before(shutdownEnd) {
			continue
		}
		if r.ServerID == oldPod {
			tl.T7 = r.Timestamp
			tl.UnhealthyReqCount++
			if r.Timestamp.After(lateThreshold) {
				tl.LateConnectionCount++
			}
		}
	}

	if inPlace {
		tl.NewPod = oldPod
		for _, r := range records {
			if r.ServerID != oldPod || !isRestartProcess(r, t71) {
				continue
			}
			if start, ok := parseServerStartTime(r.ServerStartTime); ok {
				if tl.T73.IsZero() || start.Before(tl.T73) {
					tl.T73 = start
				}
			} else if tl.T73.IsZero() {
				tl.T73 = r.Timestamp
			}
			if r.IsNonReadyReq {
				tl.PreReadyzReqCount++
				if tl.T74.IsZero() {
					tl.T74 = r.Timestamp
				}
			}
			if tl.T8.IsZero() && r.ServerState == "ready" && r.FirstReadyzTime != "never" && r.FirstReadyzTime != "" {
				if parsed, err := time.Parse(time.RFC3339Nano, r.FirstReadyzTime); err == nil {
					tl.T8 = parsed.UTC()
				}
			}
			if tl.T10.IsZero() && r.ServerState == "ready" {
				tl.T10 = r.Timestamp
			}
		}
	} else {
		// Identify the new pod: first ServerID not in knownServers, after t7.1
		for _, r := range records {
			if r.ServerID == "" || knownServers[r.ServerID] || r.Timestamp.Before(t71) {
				continue
			}
			tl.NewPod = r.ServerID
			break
		}

		for _, r := range records {
			if r.ServerID != tl.NewPod || r.Timestamp.Before(t71) {
				continue
			}
			if start, ok := parseServerStartTime(r.ServerStartTime); ok {
				if tl.T73.IsZero() || start.Before(tl.T73) {
					tl.T73 = start
				}
			} else if tl.T73.IsZero() {
				tl.T73 = r.Timestamp
			}
			if r.IsNonReadyReq {
				tl.PreReadyzReqCount++
				if tl.T74.IsZero() {
					tl.T74 = r.Timestamp
				}
			}
			if tl.T8.IsZero() && r.ServerState == "ready" && r.FirstReadyzTime != "never" && r.FirstReadyzTime != "" {
				if parsed, err := time.Parse(time.RFC3339Nano, r.FirstReadyzTime); err == nil {
					tl.T8 = parsed.UTC()
				}
			}
			if tl.T10.IsZero() && r.ServerState == "ready" {
				tl.T10 = r.Timestamp
			}
		}
	}

	// t9: first observer healthy event AFTER t71 (pod restart), not after t8
	// (t8 may be wrong or zero). Look for the healthy transition that corresponds
	// to the new pod coming online.
	for _, e := range events {
		if e.Timestamp.Before(t71) {
			continue
		}
		if e.State == "healthy" && (isUnhealthyState(e.PrevState) || e.PrevState == "initial") {
			tl.T9 = e.Timestamp
			break
		}
	}

	return tl
}

// computeTimeline52 builds the timing model for Scenario 5.2 (no restart).
// The target pod stays alive; we signal readyz→503, observe shutdown propagation,
// then signal readyz→200 and observe recovery.
func computeTimeline52(
	targetPod string,
	t5, t8 time.Time,
	records []health.RequestRecord,
	events []health.HealthEvent,
) transitionTimeline {
	tl := transitionTimeline{T5: t5, T8: t8}

	// t6: first observer event showing a target going unhealthy AFTER t5.
	// Only match healthy→unhealthy transitions (not initial→unhealthy which
	// are nodes that never passed HC, e.g. nodes without local pods).
	for _, e := range events {
		if e.Timestamp.Before(t5) {
			continue
		}
		if isUnhealthyState(e.State) && e.PrevState == "healthy" {
			tl.T6 = e.Timestamp
			break
		}
	}

	// t7: last request served by the target pod after t5 and before t8.
	// Each such request is "unhealthy" because readyz was 503.
	for _, r := range records {
		if r.Timestamp.Before(t5) || r.Timestamp.After(t8) {
			continue
		}
		if r.ServerID == targetPod {
			tl.T7 = r.Timestamp
			tl.UnhealthyReqCount++
		}
	}

	// t9: first observer event showing a target going healthy AFTER t8.
	// Match unhealthy→healthy (recovery after we signaled readyz→200).
	for _, e := range events {
		if e.Timestamp.Before(t8) {
			continue
		}
		if e.State == "healthy" && isUnhealthyState(e.PrevState) {
			tl.T9 = e.Timestamp
			break
		}
	}

	// t10: first request to the target pod after recovery (after t9 if known,
	// otherwise after t8).
	searchAfter := t8
	if !tl.T9.IsZero() {
		searchAfter = tl.T9
	}
	for _, r := range records {
		if r.Timestamp.Before(searchAfter) {
			continue
		}
		if r.ServerID == targetPod {
			tl.T10 = r.Timestamp
			break
		}
	}

	return tl
}

// ─── Report (single block, no per-line logger timestamps) ───────────────────

// fmtT formats a time in UTC to avoid timezone mismatches between the
// test binary (local TZ) and containers (UTC). All timestamps in the
// report use UTC for consistent comparison.
func fmtT(t time.Time) string {
	if t.IsZero() {
		return "N/A"
	}
	return t.UTC().Format(time.RFC3339)
}

func fmtDelta(base, t time.Time) string {
	if t.IsZero() || base.IsZero() {
		return ""
	}
	return fmt.Sprintf("[+%s]", t.Sub(base).Truncate(time.Millisecond))
}

func fmtDur(a, b time.Time) string {
	if a.IsZero() || b.IsZero() {
		return "N/A"
	}
	return b.Sub(a).Truncate(time.Millisecond).String()
}

// timelineEntry is a single row in the unified chronological timeline.
type timelineEntry struct {
	t     time.Time
	label string
	delta string
}

func buildReport(
	scenario string,
	tl transitionTimeline,
	cfg serviceConfig,
	replicas int32,
	startupDelay, shutdownDelay time.Duration,
	records []health.RequestRecord,
	events []health.HealthEvent,
	snapshots []health.TargetSnapshot,
) string {
	populateAWSReportConfig(context.Background(), &cfg)

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	sep := "═══════════════════════════════════════════════════════════════════════════"

	w(sep)
	w("HEALTH TRANSITION REPORT — Scenario %s", scenario)
	w(sep)

	// ── Environment ──
	w("")
	w("ENVIRONMENT")
	w("  Platform:       %s", cfg.Platform)
	w("  Region:         %s", cfg.Region)
	w("  Topology:       %s", cfg.Topology)

	// ── Identity ──
	w("")
	w("TARGET")
	w("  Pod:            %s", tl.TargetPod)
	w("  Node:           %s", tl.TargetNode)
	if tl.NewPod != "" {
		w("  New Pod:        %s", tl.NewPod)
	}

	// ── Test params ──
	w("")
	w("TEST PARAMETERS")
	w("  Replicas:       %d", replicas)
	w("  Startup Delay:  %s", startupDelay)
	if shutdownDelay > 0 {
		w("  Shutdown Delay: %s", shutdownDelay)
	}
	w("  Client Interval: %s (%d parallel workers)", defaultClientInterval, defaultClientWorkers)

	// ── E2E test metadata (intent — not AWS API) ──
	w("")
	writeE2ETestMetadata(w, cfg.ServiceAnnotations)

	// ── AWS infrastructure snapshot (Describe* APIs at report time) ──
	w("")
	w("LOAD BALANCER CONFIGURATION (AWS API)")
	if len(cfg.AWSLoadBalancer) == 0 {
		if cfg.LBDNS != "" {
			w("  load_balancer.dns_name: %s", cfg.LBDNS)
		}
		if cfg.LBARN != "" {
			w("  load_balancer.identifier: %s", cfg.LBARN)
		}
		if cfg.TGTargetType != "" {
			w("  target_type: %s", cfg.TGTargetType)
		}
		w("  note: ELBv2 Describe* snapshot unavailable (Classic Load Balancer or discovery failed)")
		w("")
	} else {
		writeAWSReportSection(w, "DescribeLoadBalancers", cfg.AWSLoadBalancer)
		writeAWSReportSection(w, "DescribeLoadBalancerAttributes", cfg.AWSLoadBalancerAttrs)
		writeAWSReportSection(w, "DescribeTargetGroups", cfg.AWSTargetGroup)
		writeAWSReportSection(w, "DescribeTargetGroupAttributes", cfg.AWSTargetGroupAttrs)
	}

	// ── Timing table ──
	w("")
	w("TIMING TABLE")
	w("%-25s %-14s %-14s %s", "Metric", "Value", "Expected", "Description")
	w("%-25s %-14s %-14s %s", strings.Repeat("─", 25), strings.Repeat("─", 14), strings.Repeat("─", 14), strings.Repeat("─", 30))
	w("%-25s %-14s %-14s %s", "T_deploy_ready", fmtDur(tl.T0, tl.T1), "", "t1-t0: pods scheduled + running")
	w("%-25s %-14s %-14s %s", "T_nlb_provision", fmtDur(tl.T0, tl.T2), "", "t2-t0: NLB provisioned")
	w("%-25s %-14s %-14s %s", "T_tg_initial_healthy", fmtDur(tl.T0, tl.T3), "", "t3-t0: all TG targets healthy")
	w("%-25s %-14s %-14s %s", "T_first_request", fmtDur(tl.T3, tl.T4), "seconds", "t4-t3: first routed request")
	w("%-25s %-14s %-14s %s", "", "", "", "")
	w("%-25s %-14s %-14s %s", "T_tg_unhealthy", fmtDur(tl.T5, tl.T6), "~20s", "t6-t5: HC detect unhealthy")
	w("%-25s %-14s %-14s %s", "T_route_stop", fmtDur(tl.T5, tl.T7), "<shutdown-delay", "t7-t5: last req after readyz→503")
	w("%-25s %-14d %-14s %s", "Unhealthy_reqs", tl.UnhealthyReqCount, "0 ideal", "requests to target after readyz→503")
	w("%-25s %-14d %-14s %s", "Late_conn_reqs", tl.LateConnectionCount, "0 ideal", "requests after 80% shutdown delay")
	if !tl.T71.IsZero() {
		restartMetric := "T_pod_restart"
		restartDesc := "t7.3-t7.1: pod kill→TCP up"
		if cfg.ServiceAnnotations["restart-mode"] == "ctl-in-place" {
			restartMetric = "T_container_restart"
			restartDesc = "t7.3-t7.1: ctl SIGUSR2→TCP up (same pod)"
		}
		w("%-25s %-14s %-14s %s", restartMetric, fmtDur(tl.T71, tl.T73), "seconds", restartDesc)
		preReadyz := effectivePreReadyzBugCount(tl.PreReadyzReqCount)
		preReadyzDesc := "requests before readyz→200 (BUG)"
		if tl.PreReadyzReqCount == 1 && preReadyz == 0 {
			preReadyzDesc = "spurious single req ignored (ctl restart race)"
		}
		w("%-25s %-14d %-14s %s", "Pre_readyz_reqs", preReadyz, "0", preReadyzDesc)
	}
	w("%-25s %-14s %-14s %s", "T_tg_healthy", fmtDur(tl.T8, tl.T9), "~20s", "t9-t8: HC detect healthy")
	w("%-25s %-14s %-14s %s", "T_route_start", fmtDur(tl.T8, tl.T10), "20-120s", "t10-t8: first req after readyz→200")
	w("%-25s %-14s %-14s %s", "T_total_cycle", fmtDur(tl.T5, tl.T10), "", "t10-t5: full cycle")

	// ── Request statistics ──
	// Compute overall and per-phase request counts from client records.
	var totalReqs, reqs2xx, reqs4xx, reqs5xx, reqsErr int
	for _, r := range records {
		totalReqs++
		switch {
		case r.Error != "":
			reqsErr++
		case r.HTTPStatus >= 200 && r.HTTPStatus < 300:
			reqs2xx++
		case r.HTTPStatus >= 400 && r.HTTPStatus < 500:
			reqs4xx++
		case r.HTTPStatus >= 500:
			reqs5xx++
		}
	}

	// Compute average req/s across the full test duration (t3→last record)
	var avgReqsPerSec float64
	var testDuration time.Duration
	if len(records) > 1 {
		testDuration = records[len(records)-1].Timestamp.Sub(records[0].Timestamp)
		if testDuration > 0 {
			avgReqsPerSec = float64(totalReqs) / testDuration.Seconds()
		}
	}

	w("")
	w("REQUEST STATISTICS")
	w("  Total:    %d", totalReqs)
	w("  2xx:      %d", reqs2xx)
	w("  4xx:      %d", reqs4xx)
	w("  5xx:      %d", reqs5xx)
	w("  Errors:   %d (connection/timeout failures)", reqsErr)
	w("  Duration: %s", testDuration.Truncate(time.Second))
	w("  Avg rate: %.1f req/s", avgReqsPerSec)

	// Print unique error messages (deduplicated) to help diagnose routing issues.
	if reqsErr > 0 {
		errCounts := make(map[string]int)
		for _, r := range records {
			if r.Error != "" {
				errCounts[r.Error]++
			}
		}
		w("")
		w("  ERROR SAMPLES (%d unique):", len(errCounts))
		shown := 0
		for msg, count := range errCounts {
			if shown >= 5 {
				w("    ... and %d more unique errors", len(errCounts)-shown)
				break
			}
			// Truncate very long error messages.
			display := msg
			if len(display) > 200 {
				display = display[:200] + "..."
			}
			w("    [%dx] %s", count, display)
			shown++
		}
	}

	// ── Per-phase request breakdown ──
	// Phases are defined by the timeline milestones:
	//   Warmup:           t3→t5  (all targets healthy, steady-state traffic)
	//   GracefulShutdown: t5→t7  (SIGTERM received, readyz→503, NLB still routing)
	//   Restart:          t7→t9  (NLB stopped routing, pod terminated, new pod starting)
	//   Recovery:         t9→end (new target healthy, traffic flowing)
	// For Scenario 5.2 (no restart): GracefulShutdown=t5→t8, Recovery=t8→end
	type phaseStats struct {
		name                   string
		from, to               time.Time
		total, ok, err, preRdz int
	}
	var phases []phaseStats

	classifyPhase := func(name string, from, to time.Time) phaseStats {
		ps := phaseStats{name: name, from: from, to: to}
		for _, r := range records {
			if (!from.IsZero() && r.Timestamp.Before(from)) || (!to.IsZero() && r.Timestamp.After(to)) {
				continue
			}
			ps.total++
			if r.Error != "" {
				ps.err++
			} else if r.HTTPStatus >= 200 && r.HTTPStatus < 300 {
				ps.ok++
			}
			if r.IsNonReadyReq {
				ps.preRdz++
			}
		}
		return ps
	}

	// Warmup: t3 (all healthy) → t5 (readyz→503).  Includes steady state.
	phases = append(phases, classifyPhase("Warmup (t3→t5)", tl.T3, tl.T5))

	if !tl.T71.IsZero() {
		// Scenario 5.5: has restart phase
		phases = append(phases, classifyPhase("GracefulShutdown (t5→t7)", tl.T5, tl.T7))
		phases = append(phases, classifyPhase("Restart (t7→t9)", tl.T7, tl.T9))
		phases = append(phases, classifyPhase("Recovery (t9→end)", tl.T9, time.Time{}))
	} else {
		// Scenario 5.2: no restart
		phases = append(phases, classifyPhase("GracefulShutdown (t5→t8)", tl.T5, tl.T8))
		phases = append(phases, classifyPhase("Recovery (t8→end)", tl.T8, time.Time{}))
	}

	w("")
	w("REQUEST BREAKDOWN BY PHASE")
	w("%-25s %10s %8s %8s %8s %8s %10s", "Phase", "Duration", "Total", "2xx", "Errors", "PreRdz", "Avg req/s")
	w("%-25s %10s %8s %8s %8s %8s %10s", strings.Repeat("─", 25), strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", 8), strings.Repeat("─", 8), strings.Repeat("─", 8), strings.Repeat("─", 10))
	for _, ps := range phases {
		dur := "N/A"
		rps := "N/A"
		var phaseDur time.Duration
		if !ps.from.IsZero() && !ps.to.IsZero() {
			phaseDur = ps.to.Sub(ps.from)
		} else if !ps.from.IsZero() && len(records) > 0 {
			// Open-ended phase (→end): use last record timestamp
			phaseDur = records[len(records)-1].Timestamp.Sub(ps.from)
		}
		if phaseDur > 0 {
			dur = phaseDur.Truncate(time.Second).String()
			rps = fmt.Sprintf("%.1f", float64(ps.total)/phaseDur.Seconds())
		}
		w("%-25s %10s %8d %8d %8d %8d %10s", ps.name, dur, ps.total, ps.ok, ps.err, ps.preRdz, rps)
	}

	// ── Per-server request distribution by phase ──
	// Shows how many requests each backend (ServerID/pod) received in each phase.
	// This is the key metric for detecting routing anomalies: if the target pod
	// receives requests during Restart (after deletion), that's the NLB bug.
	// Collect unique server IDs across all records
	serverSet := make(map[string]bool)
	for _, r := range records {
		if r.ServerID != "" {
			serverSet[r.ServerID] = true
		}
	}
	var serverIDs []string
	for id := range serverSet {
		serverIDs = append(serverIDs, id)
	}
	sort.Strings(serverIDs)

	if len(serverIDs) > 0 {
		// Build per-server per-phase counts
		type serverPhaseCount struct {
			total, preRdz int
		}
		// phaseServerCounts[phaseIdx][serverID] = counts
		phaseServerCounts := make([]map[string]serverPhaseCount, len(phases))
		for i, ps := range phases {
			phaseServerCounts[i] = make(map[string]serverPhaseCount)
			for _, r := range records {
				if r.ServerID == "" {
					continue
				}
				if (!ps.from.IsZero() && r.Timestamp.Before(ps.from)) || (!ps.to.IsZero() && r.Timestamp.After(ps.to)) {
					continue
				}
				sc := phaseServerCounts[i][r.ServerID]
				sc.total++
				if r.IsNonReadyReq {
					sc.preRdz++
				}
				phaseServerCounts[i][r.ServerID] = sc
			}
		}

		w("")
		w("PER-SERVER REQUEST DISTRIBUTION BY PHASE")

		// Annotate server IDs with their role and node name.
		// Format: "pod-name (node-name) ← TARGET"
		serverLabel := func(id string) string {
			node := ""
			if tl.PodNodeMap != nil {
				node = tl.PodNodeMap[id]
			}
			role := ""
			switch id {
			case tl.TargetPod:
				role = " ← TARGET"
			case tl.NewPod:
				role = " ← NEW"
			}
			if node != "" {
				return fmt.Sprintf("%s (%s)%s", id, node, role)
			}
			return id + role
		}

		// Print a sub-table per phase showing each server's request count
		for i, ps := range phases {
			dur := "N/A"
			if !ps.from.IsZero() && !ps.to.IsZero() {
				dur = ps.to.Sub(ps.from).Truncate(time.Second).String()
			} else if !ps.from.IsZero() && len(records) > 0 {
				dur = records[len(records)-1].Timestamp.Sub(ps.from).Truncate(time.Second).String()
			}
			w("  %s (%s):", ps.name, dur)
			for _, sid := range serverIDs {
				sc := phaseServerCounts[i][sid]
				if sc.total == 0 {
					continue
				}
				preRdzNote := ""
				if sc.preRdz > 0 {
					preRdzNote = fmt.Sprintf("  ← %d pre-readyz!", sc.preRdz)
				}
				w("    %-50s  reqs=%d%s", serverLabel(sid), sc.total, preRdzNote)
			}
		}
	}

	// ── Unified chronological timeline ──
	w("")
	w("TIMELINE")
	w("%-27s %-28s %s", "Time", "Event", "Delta")
	w("%-27s %-28s %s", strings.Repeat("─", 27), strings.Repeat("─", 28), strings.Repeat("─", 20))

	var entries []timelineEntry

	addEntry := func(t time.Time, label, delta string) {
		if !t.IsZero() {
			entries = append(entries, timelineEntry{t: t, label: label, delta: delta})
		}
	}

	// Initial registration milestones (t0-t4)
	addEntry(tl.T0, "t0  deploy created", "")
	addEntry(tl.T1, "t1  pods ready", fmtDelta(tl.T0, tl.T1))
	addEntry(tl.T2, "t2  NLB provisioned", fmtDelta(tl.T0, tl.T2))
	addEntry(tl.T3, "t3  TG all healthy", fmtDelta(tl.T0, tl.T3))
	addEntry(tl.T4, "t4  first request", fmtDelta(tl.T3, tl.T4))

	// Shutdown/restart milestones (t5-t10)
	addEntry(tl.T5, "t5  readyz→503", "")
	addEntry(tl.T6, "t6  TG unhealthy", fmtDelta(tl.T5, tl.T6))
	addEntry(tl.T7, "t7  last routed req", fmtDelta(tl.T5, tl.T7))
	t71Label := "t7.1 pod deleted (SIGTERM sent)"
	if cfg.ServiceAnnotations["restart-mode"] == "ctl-in-place" {
		t71Label = "t7.1 ctl restart (SIGUSR2, container exit)"
	}
	addEntry(tl.T71, t71Label, fmtDelta(tl.T5, tl.T71))
	addEntry(tl.T73, "t7.3 TCP up", fmtDelta(tl.T71, tl.T73))
	if !tl.T74.IsZero() {
		t74Label := "t7.4 pre-readyz req ← BUG"
		if effectivePreReadyzBugCount(tl.PreReadyzReqCount) == 0 && tl.PreReadyzReqCount == 1 {
			t74Label = "t7.4 pre-readyz req (spurious, ignored)"
		}
		addEntry(tl.T74, t74Label, fmtDelta(tl.T73, tl.T74))
	}
	addEntry(tl.T8, "t8  readyz→200", fmtDelta(tl.T5, tl.T8))
	addEntry(tl.T9, "t9  TG healthy", fmtDelta(tl.T8, tl.T9))
	addEntry(tl.T10, "t10 first routed req", fmtDelta(tl.T8, tl.T10))

	// TG health events
	for _, e := range events {
		addEntry(e.Timestamp,
			fmt.Sprintf("TG  %s→%s", e.PrevState, e.State),
			fmt.Sprintf("target=%s", e.TargetID))
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].t.Before(entries[j].t) })

	for _, e := range entries {
		w("%-27s %-28s %s", fmtT(e.t), e.label, e.delta)
	}

	// ── Snapshot summary ──
	if len(snapshots) > 0 {
		first := snapshots[0]
		last := snapshots[len(snapshots)-1]
		w("")
		w("TG SNAPSHOTS (%d polls, %s duration)", len(snapshots),
			last.Timestamp.Sub(first.Timestamp).Truncate(time.Second))
		w("  first: %s  healthy=%d unhealthy=%d initial=%d",
			fmtT(first.Timestamp), first.HealthyCount, first.UnhealthyCount, first.InitialCount)
		w("  last:  %s  healthy=%d unhealthy=%d initial=%d",
			fmtT(last.Timestamp), last.HealthyCount, last.UnhealthyCount, last.InitialCount)
	}

	w(sep)
	return b.String()
}

// ─── Resource builders ──────────────────────────────────────────────────────

// ─── Verdict builders ───────────────────────────────────────────────────────

// effectivePreReadyzBugCount treats a lone pre-readyz response as measurement noise
// from the ctl in-place restart race, not OCPBUGS-86789 NLB routing.
func effectivePreReadyzBugCount(n int) int {
	if n == 1 {
		return 0
	}
	return n
}

// effectiveRestartUnhealthyCount ignores a lone [RESTART] unhealthy count when
// pre-readyz is also absent or spurious (shutdown tail during restart window).
func effectiveRestartUnhealthyCount(restartReqs, preReadyz int) int {
	if restartReqs == 1 && preReadyz <= 1 {
		return 0
	}
	return restartReqs
}

// buildVerdict55 produces the verdict string for Scenario 5.5 (pre-readyz routing).
// It checks both client-side (X-Server-State: pre-readyz) and server-side
// (did the target pod receive requests during Shutdown/Restart phases).
func buildVerdict55(tl transitionTimeline, records []health.RequestRecord) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	// Count requests to the target pod AFTER readyz→503 (shutdown phase)
	var targetAfterShutdown int
	for _, r := range records {
		if r.Timestamp.Before(tl.T5) || r.ServerID != tl.TargetPod {
			continue
		}
		targetAfterShutdown++
	}

	// Count requests to the target pod's node during Restart phase (t7→t9).
	// Start from t7 (last routed request), NOT t7.1 (pod delete/SIGTERM),
	// because requests between t5→t7 are expected GracefulShutdown traffic
	// (NLB propagation delay) and are already reported by [SHUTDOWN].
	// Requests AFTER t7 mean the LB re-routed to the target unexpectedly.
	var targetDuringRestart int
	if !tl.T7.IsZero() {
		end := tl.T9
		if end.IsZero() {
			end = tl.T10
		}
		for _, r := range records {
			if r.Timestamp.Before(tl.T7) {
				continue
			}
			if !end.IsZero() && r.Timestamp.After(end) {
				continue
			}
			// Match the target pod OR the new pod
			if r.ServerID == tl.TargetPod || r.ServerID == tl.NewPod {
				if r.ServerState == "pre-readyz" || r.ServerState == "draining" || r.ServerState == "shutdown" {
					targetDuringRestart++
				}
			}
		}
	}

	preReadyz := effectivePreReadyzBugCount(tl.PreReadyzReqCount)
	restartUnhealthy := effectiveRestartUnhealthyCount(targetDuringRestart, tl.PreReadyzReqCount)

	w("")
	w("VERDICT")

	if preReadyz > 0 {
		w("  [BUG] NLB routed %d request(s) with X-Server-State: pre-readyz", preReadyz)
		w("        This reproduces OCPBUGS-86789 — NLB routes before /readyz passes")
	} else if tl.PreReadyzReqCount == 1 {
		w("  [INFO] Ignored 1 spurious pre-readyz request (ctl restart race, not OCPBUGS-86789)")
	}

	if targetAfterShutdown > 0 {
		w("  [SHUTDOWN] Target pod received %d request(s) after readyz→503 (T_route_stop=%s)",
			targetAfterShutdown, fmtDur(tl.T5, tl.T7))
		w("             NLB continued routing to unhealthy target for %s", fmtDur(tl.T5, tl.T7))
	}

	if restartUnhealthy > 0 {
		w("  [RESTART] Target node received %d unhealthy/pre-readyz request(s) during Restart phase", restartUnhealthy)
	} else if targetDuringRestart == 1 {
		w("  [INFO] Ignored 1 spurious Restart-phase request (shutdown tail, not OCPBUGS-86789)")
	}

	if preReadyz == 0 && restartUnhealthy == 0 {
		w("  [OK] No pre-readyz routing detected")
		w("       NLB correctly waited for HC to pass before routing to restarted target")
	}

	if targetAfterShutdown > 0 {
		w("  [INFO] Shutdown propagation: %d requests routed to target after readyz→503 (expected: NLB propagation delay)",
			targetAfterShutdown)
	}

	if tl.LateConnectionCount > 0 {
		w("  [LATE-CONN] %d request(s) routed after 80%% of shutdown delay (pod kill imminent)",
			tl.LateConnectionCount)
	}

	return b.String()
}

// buildVerdict52 produces the verdict string for Scenario 5.2 (shutdown propagation).
func buildVerdict52(tl transitionTimeline) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("")
	w("VERDICT")
	w("  NLB routed %d request(s) to unhealthy target after readyz→503", tl.UnhealthyReqCount)
	if !tl.T7.IsZero() && !tl.T5.IsZero() {
		w("  T_route_stop = %s (NLB kept routing after readyz→503)",
			tl.T7.Sub(tl.T5).Truncate(time.Second))
	}
	if !tl.T10.IsZero() && !tl.T8.IsZero() {
		w("  T_route_start = %s (NLB started routing after readyz→200)",
			tl.T10.Sub(tl.T8).Truncate(time.Second))
	}

	return b.String()
}

// ─── Resource builders ──────────────────────────────────────────────────────

// buildHealthserverDeployment creates a Deployment spec that schedules pods on
// master/control-plane nodes to match KAS topology. Includes tolerations for
// both master and control-plane taints, and topologySpreadConstraints to
// distribute pods across nodes.
func buildHealthserverDeployment(namespace, name string, replicas int32, startupDelay time.Duration, image string, aggregatorURL ...string) *appsv1.Deployment {
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
					// hostNetwork: pod binds directly on the node's network
					// interface, exactly like KAS static pods. The NLB health
					// check hits nodeIP:19443/readyz directly — no kube-proxy
					// mediation. This is essential for reproducing OCPBUGS-86789.
					HostNetwork: true,
					DNSPolicy:   v1.DNSClusterFirstWithHostNet,
					// Schedule on control-plane nodes to match KAS topology.
					// OCP 5.x uses control-plane; OCP 4.x has both labels.
					NodeSelector: map[string]string{
						"node-role.kubernetes.io/control-plane": "",
					},
					// terminationGracePeriodSeconds matches KAS
					// shutdown-delay-duration. After SIGTERM, the healthserver
					// sets readyz→503 and keeps serving for this duration.
					TerminationGracePeriodSeconds: ptrInt64(int64(kasShutdownDelay.Seconds())),
					// Tolerate master and control-plane taints
					Tolerations: []v1.Toleration{
						{Key: "node-role.kubernetes.io/master", Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoSchedule},
						{Key: "node-role.kubernetes.io/control-plane", Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoSchedule},
					},
					TopologySpreadConstraints: []v1.TopologySpreadConstraint{{
						MaxSkew:           1,
						TopologyKey:       "kubernetes.io/hostname",
						WhenUnsatisfiable: v1.ScheduleAnyway,
						LabelSelector:     &metav1.LabelSelector{MatchLabels: labels},
					}},
					Containers: []v1.Container{{
						Name:  "healthserver",
						Image: image,
						Args: func() []string {
							// Use the unified binary with "serve" subcommand.
							// If aggregatorURL is provided, pass it so the server
							// pushes lifecycle events to the aggregator.
							args := []string{
								"serve",
								fmt.Sprintf("--port=%d", healthserverPort),
								fmt.Sprintf("--startup-delay=%s", startupDelay),
							}
							if len(aggregatorURL) > 0 && aggregatorURL[0] != "" {
								args = append(args, fmt.Sprintf("--aggregator=%s", aggregatorURL[0]))
							}
							return args
						}(),
						Ports: []v1.ContainerPort{{
							Name:          "http",
							ContainerPort: healthserverPort,
							HostPort:      healthserverPort,
						}},
						// SecurityContext: let OpenShift assign the UID from the
						// namespace range. The privileged SCC handles hostNetwork.
						SecurityContext: &v1.SecurityContext{
							AllowPrivilegeEscalation: ptrBool(false),
							Capabilities: &v1.Capabilities{
								Drop: []v1.Capability{"ALL"},
							},
							SeccompProfile: &v1.SeccompProfile{
								Type: v1.SeccompProfileTypeRuntimeDefault,
							},
						},
						Env: []v1.EnvVar{
							{
								Name: "POD_NAME",
								ValueFrom: &v1.EnvVarSource{
									FieldRef: &v1.ObjectFieldSelector{FieldPath: "metadata.name"},
								},
							},
							{
								// POD_IP is used to register with the aggregator
								// using the real node IP (hostNetwork pod).
								Name: "POD_IP",
								ValueFrom: &v1.EnvVarSource{
									FieldRef: &v1.ObjectFieldSelector{FieldPath: "status.podIP"},
								},
							},
						},
					}},
				},
			},
		},
	}
}

// buildHealthserverDaemonSet creates a DaemonSet spec with the same pod
// template as buildHealthserverDeployment. A DaemonSet guarantees one pod per
// matching node and same-node replacement on pod deletion, which matches KAS
// static pod rollout behavior for NLB health transition testing.
func buildHealthserverDaemonSet(namespace, name string, startupDelay time.Duration, image string, aggregatorURL ...string) *appsv1.DaemonSet {
	labels := map[string]string{"app": name}
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: v1.PodSpec{
					HostNetwork: true,
					DNSPolicy:   v1.DNSClusterFirstWithHostNet,
					NodeSelector: map[string]string{
						"node-role.kubernetes.io/control-plane": "",
					},
					TerminationGracePeriodSeconds: ptrInt64(int64(kasShutdownDelay.Seconds())),
					Tolerations: []v1.Toleration{
						{Key: "node-role.kubernetes.io/master", Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoSchedule},
						{Key: "node-role.kubernetes.io/control-plane", Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoSchedule},
					},
					Containers: []v1.Container{{
						Name:  "healthserver",
						Image: image,
						Args: func() []string {
							args := []string{
								"serve",
								fmt.Sprintf("--port=%d", healthserverPort),
								fmt.Sprintf("--startup-delay=%s", startupDelay),
							}
							if len(aggregatorURL) > 0 && aggregatorURL[0] != "" {
								args = append(args, fmt.Sprintf("--aggregator=%s", aggregatorURL[0]))
							}
							return args
						}(),
						Ports: []v1.ContainerPort{{
							Name:          "http",
							ContainerPort: healthserverPort,
							HostPort:      healthserverPort,
						}},
						SecurityContext: &v1.SecurityContext{
							AllowPrivilegeEscalation: ptrBool(false),
							Capabilities: &v1.Capabilities{
								Drop: []v1.Capability{"ALL"},
							},
							SeccompProfile: &v1.SeccompProfile{
								Type: v1.SeccompProfileTypeRuntimeDefault,
							},
						},
						Env: []v1.EnvVar{
							{
								Name: "POD_NAME",
								ValueFrom: &v1.EnvVarSource{
									FieldRef: &v1.ObjectFieldSelector{FieldPath: "metadata.name"},
								},
							},
							{
								Name: "POD_IP",
								ValueFrom: &v1.EnvVarSource{
									FieldRef: &v1.ObjectFieldSelector{FieldPath: "status.podIP"},
								},
							},
						},
					}},
				},
			},
		},
	}
}

// buildHealthserverDaemonSetTLS is like buildHealthserverDaemonSet but serves
// traffic and /readyz over TLS using a mounted self-signed cert (KAS-like).
func buildHealthserverDaemonSetTLS(namespace, name string, startupDelay time.Duration, image, aggregatorURL string) *appsv1.DaemonSet {
	ds := buildHealthserverDaemonSet(namespace, name, startupDelay, image, aggregatorURL)
	c := &ds.Spec.Template.Spec.Containers[0]
	c.Args = append(c.Args,
		"--tls",
		fmt.Sprintf("--tls-cert=%s/%s", healthserverTLSMountPath, healthserverTLSCertFile),
		fmt.Sprintf("--tls-key=%s/%s", healthserverTLSMountPath, healthserverTLSKeyFile),
	)
	c.VolumeMounts = append(c.VolumeMounts, v1.VolumeMount{
		Name:      "healthserver-tls",
		MountPath: healthserverTLSMountPath,
		ReadOnly:  true,
	})
	ds.Spec.Template.Spec.Volumes = append(ds.Spec.Template.Spec.Volumes, v1.Volume{
		Name: "healthserver-tls",
		VolumeSource: v1.VolumeSource{
			ConfigMap: &v1.ConfigMapVolumeSource{
				LocalObjectReference: v1.LocalObjectReference{Name: healthserverTLSConfigMap},
			},
		},
	})
	return ds
}

// waitForDaemonSetReady polls the DaemonSet status until NumberReady equals
// DesiredNumberScheduled (and DesiredNumberScheduled > 0), or the timeout
// is reached. Returns the DesiredNumberScheduled count.
func waitForDaemonSetReady(ctx context.Context, cs clientset.Interface, namespace, name string, timeout time.Duration) (int32, error) {
	var desired int32
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		ds, err := cs.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		desired = ds.Status.DesiredNumberScheduled
		framework.Logf("daemonset ready: %d/%d", ds.Status.NumberReady, desired)
		return desired > 0 && ds.Status.NumberReady == desired, nil
	})
	return desired, err
}

// buildHealthTransitionService creates a Service spec for an NLB that:
// - Targets only master/control-plane nodes (target-node-labels annotation)
// - Enables cross-zone load balancing for HA
// - Uses HTTP /readyz health check with 10s interval and threshold=2
// - Uses externalTrafficPolicy: Local for per-node health tracking
func buildHealthTransitionService(namespace, name, deployName string) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-type":                              "nlb",
				"service.beta.kubernetes.io/aws-load-balancer-target-node-labels":                "node-role.kubernetes.io/control-plane=",
				"service.beta.kubernetes.io/aws-load-balancer-cross-zone-load-balancing-enabled": "true",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol":              "HTTP",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-path":                  "/readyz",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-port":                  fmt.Sprintf("%d", healthserverPort),
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval":              "10",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-healthy-threshold":     "2",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-unhealthy-threshold":   "2",
			},
		},
		Spec: v1.ServiceSpec{
			Type:                  v1.ServiceTypeLoadBalancer,
			ExternalTrafficPolicy: v1.ServiceExternalTrafficPolicyLocal,
			Selector:              map[string]string{"app": deployName},
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   v1.ProtocolTCP,
				Port:       int32(healthserverPort),
				TargetPort: intstr.FromInt(healthserverPort),
			}},
		},
	}
}

func waitForLBDeletion(ctx context.Context, lbDNS string) {
	elbClient, err := createAWSClientLoadBalancer(ctx)
	if err != nil {
		return
	}
	_ = wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		lb, err := findAWSLoadBalancerByDNSName(ctx, elbClient, lbDNS)
		if err != nil {
			return false, nil
		}
		return lb == nil, nil
	})
}

// grantHostNetworkSCC creates a RoleBinding that grants the default service
// account in the given namespace access to the privileged SCC. This is
// required on OpenShift for pods with hostNetwork: true. The privileged SCC
// allows hostNetwork, hostPort, and any UID — matching what static pods
// (like KAS) use on control-plane nodes.
func grantHostNetworkSCC(ctx context.Context, cs clientset.Interface, namespace string) {
	rbName := "healthserver-privileged"
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbName,
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      "default",
			Namespace: namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:openshift:scc:privileged",
		},
	}
	_, err := cs.RbacV1().RoleBindings(namespace).Create(ctx, rb, metav1.CreateOptions{})
	framework.ExpectNoError(err, "grant privileged SCC to default SA")
}

// ─── In-cluster aggregator + client deployment ─────────────────────────────

// deployAggregator creates a Pod and ClusterIP Service for the aggregator
// on a worker node. Returns the service DNS name for other pods to connect.
func deployAggregator(ctx context.Context, cs clientset.Interface, namespace, image string) string {
	svcName := "healthtest-aggregator"
	podName := "healthtest-aggregator"

	// Pod
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels:    map[string]string{"app": "healthtest-aggregator"},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{
				Name:  "aggregator",
				Image: image,
				Args:  []string{"aggregator", fmt.Sprintf("--port=%d", aggregatorPort), "--scrape-interval=1s"},
				Ports: []v1.ContainerPort{{
					Name:          "http",
					ContainerPort: int32(aggregatorPort),
				}},
				ReadinessProbe: &v1.Probe{
					ProbeHandler: v1.ProbeHandler{
						HTTPGet: &v1.HTTPGetAction{
							Path: "/healthz",
							Port: intstr.FromInt(aggregatorPort),
						},
					},
					PeriodSeconds: 2,
				},
			}},
		},
	}
	_, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create aggregator pod")

	// ClusterIP Service so servers and client can reach the aggregator by DNS
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: namespace,
		},
		Spec: v1.ServiceSpec{
			Selector: map[string]string{"app": "healthtest-aggregator"},
			Ports: []v1.ServicePort{{
				Port:       int32(aggregatorPort),
				TargetPort: intstr.FromInt(aggregatorPort),
			}},
		},
	}
	_, err = cs.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create aggregator service")

	// Wait for aggregator pod ready
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		p, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, c := range p.Status.Conditions {
			if c.Type == v1.PodReady && c.Status == v1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	framework.ExpectNoError(err, "aggregator pod ready")

	// Return the in-cluster DNS name for the aggregator service
	return fmt.Sprintf("http://%s.%s.svc:%d", svcName, namespace, aggregatorPort)
}

// deployInClusterClient creates a Pod on a worker node that sends HTTP
// requests to the NLB. Returns the pod name for result fetching.
func deployInClusterClient(ctx context.Context, cs clientset.Interface, namespace, image, nlbDNS, aggregatorURL string, workers int, interval time.Duration) string {
	podName := "healthtest-client"

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels:    map[string]string{"app": "healthtest-client"},
		},
		Spec: v1.PodSpec{
			// Schedule on worker nodes (NOT control-plane)
			Affinity: &v1.Affinity{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{{
							MatchExpressions: []v1.NodeSelectorRequirement{{
								Key:      "node-role.kubernetes.io/worker",
								Operator: v1.NodeSelectorOpExists,
							}},
						}},
					},
				},
			},
			Containers: []v1.Container{{
				Name:  "client",
				Image: image,
				// POD_IP is used by the client to register with the
				// aggregator using its real pod IP (not localhost).
				Env: []v1.EnvVar{{
					Name: "POD_IP",
					ValueFrom: &v1.EnvVarSource{
						FieldRef: &v1.ObjectFieldSelector{FieldPath: "status.podIP"},
					},
				}},
				Args: []string{
					"client",
					fmt.Sprintf("--url=http://%s:%d/", nlbDNS, healthserverPort),
					fmt.Sprintf("--workers=%d", workers),
					fmt.Sprintf("--interval=%s", interval),
					fmt.Sprintf("--port=%d", clientPort),
					fmt.Sprintf("--aggregator=%s", aggregatorURL),
				},
				Ports: []v1.ContainerPort{{
					Name:          "http",
					ContainerPort: int32(clientPort),
				}},
				ReadinessProbe: &v1.Probe{
					ProbeHandler: v1.ProbeHandler{
						HTTPGet: &v1.HTTPGetAction{
							Path: "/healthz",
							Port: intstr.FromInt(clientPort),
						},
					},
					PeriodSeconds: 2,
				},
			}},
		},
	}
	_, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create client pod")

	// Wait for client pod ready (starts sending requests immediately)
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		p, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, c := range p.Status.Conditions {
			if c.Type == v1.PodReady && c.Status == v1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	framework.ExpectNoError(err, "client pod ready")
	framework.Logf("[client-pod] started on worker node, sending requests to NLB")

	return podName
}

// deployClientDaemonSet creates a DaemonSet of client pods, one per worker
// node. Because each pod has a different source IP, the NLB distributes
// traffic across all targets even with preserve_client_ip.enabled=true.
// Returns the list of pod names created by the DaemonSet.
// Pass useTLS=true to use https:// against the NLB with --tls-insecure.
func deployClientDaemonSet(ctx context.Context, cs clientset.Interface, namespace, image, nlbDNS, aggregatorURL string, workers int, interval time.Duration, useTLS ...bool) []string {
	dsName := "healthtest-client"
	labels := map[string]string{"app": dsName}

	scheme := "http"
	if len(useTLS) > 0 && useTLS[0] {
		scheme = "https"
	}
	clientArgs := []string{
		"client",
		fmt.Sprintf("--url=%s://%s:%d/", scheme, nlbDNS, healthserverPort),
		fmt.Sprintf("--workers=%d", workers),
		fmt.Sprintf("--interval=%s", interval),
		fmt.Sprintf("--port=%d", clientPort),
		fmt.Sprintf("--aggregator=%s", aggregatorURL),
	}
	if scheme == "https" {
		clientArgs = append(clientArgs, "--tls-insecure")
	}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: dsName, Namespace: namespace},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: v1.PodSpec{
					// Worker nodes only — do not land on control-plane.
					NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
					Containers: []v1.Container{{
						Name:  "client",
						Image: image,
						Env: []v1.EnvVar{{
							Name: "POD_IP",
							ValueFrom: &v1.EnvVarSource{
								FieldRef: &v1.ObjectFieldSelector{FieldPath: "status.podIP"},
							},
						}},
						Args: clientArgs,
						Ports: []v1.ContainerPort{{
							Name:          "http",
							ContainerPort: int32(clientPort),
						}},
						ReadinessProbe: &v1.Probe{
							ProbeHandler: v1.ProbeHandler{
								HTTPGet: &v1.HTTPGetAction{
									Path: "/healthz",
									Port: intstr.FromInt(clientPort),
								},
							},
							PeriodSeconds: 2,
						},
					}},
				},
			},
		},
	}

	_, err := cs.AppsV1().DaemonSets(namespace).Create(ctx, ds, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create client DaemonSet")

	// Wait for all pods ready.
	var podNames []string
	err = wait.PollUntilContextTimeout(ctx, 3*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		d, err := cs.AppsV1().DaemonSets(namespace).Get(ctx, dsName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		framework.Logf("[client-ds] ready: %d/%d", d.Status.NumberReady, d.Status.DesiredNumberScheduled)
		if d.Status.DesiredNumberScheduled == 0 || d.Status.NumberReady < d.Status.DesiredNumberScheduled {
			return false, nil
		}
		// Collect pod names.
		pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", dsName),
		})
		if err != nil {
			return false, nil
		}
		podNames = nil
		for _, p := range pods.Items {
			if p.DeletionTimestamp == nil {
				podNames = append(podNames, p.Name)
			}
		}
		return true, nil
	})
	framework.ExpectNoError(err, "client DaemonSet ready")
	framework.Logf("[client-ds] %d client pods ready on worker nodes: %v", len(podNames), podNames)
	return podNames
}

// fetchMergedClientRecords fetches records from all client pods and merges
// them into a single slice sorted by timestamp. Use this when multiple client
// pods (DaemonSet) are deployed; each pod tracks only its own requests.
func fetchMergedClientRecords(ctx context.Context, cs clientset.Interface, namespace string, podNames []string) []health.RequestRecord {
	var merged []health.RequestRecord
	for _, pod := range podNames {
		recs := fetchClientRecords(ctx, cs, namespace, pod)
		framework.Logf("[client-ds] fetched %d records from %s", len(recs), pod)
		merged = append(merged, recs...)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Timestamp.Before(merged[j].Timestamp)
	})
	framework.Logf("[client-ds] merged %d total records from %d pods", len(merged), len(podNames))
	return merged
}

// fetchClientRecords retrieves all request records from the in-cluster
// client pod via the K8s API server proxy. The client pod runs on a worker
// node with normal networking, so the API proxy works.
func fetchClientRecords(ctx context.Context, cs clientset.Interface, namespace, clientPodName string) []health.RequestRecord {
	result := cs.CoreV1().RESTClient().Get().
		AbsPath(fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy/records", namespace, clientPodName, clientPort)).
		Timeout(30 * time.Second).
		Do(ctx)
	if err := result.Error(); err != nil {
		framework.Logf("warning: failed to fetch client records: %v", err)
		return nil
	}
	raw, err := result.Raw()
	if err != nil {
		framework.Logf("warning: failed to read client records: %v", err)
		return nil
	}

	// The client returns ClientRecord (types from the unified binary).
	// Map to health.RequestRecord for compatibility with existing analysis.
	type clientRecord struct {
		Timestamp       time.Time `json:"timestamp"`
		TargetIP        string    `json:"target_ip"`
		TCPDialDuration int64     `json:"tcp_dial_ms"`
		HTTPStatus      int       `json:"http_status"`
		ServerState     string    `json:"server_state"`
		ServerID        string    `json:"server_id"`
		ServerStartTime string    `json:"server_start_time"`
		FirstReadyzTime string    `json:"first_readyz_time"`
		IsNonReadyReq   bool      `json:"is_non_ready_req"`
		Error           string    `json:"error,omitempty"`
	}
	var crs []clientRecord
	if err := json.Unmarshal(raw, &crs); err != nil {
		framework.Logf("warning: failed to parse client records: %v", err)
		return nil
	}

	records := make([]health.RequestRecord, len(crs))
	for i, cr := range crs {
		records[i] = health.RequestRecord{
			Timestamp:       cr.Timestamp,
			TargetIP:        cr.TargetIP,
			TCPDialDuration: time.Duration(cr.TCPDialDuration) * time.Millisecond,
			HTTPStatus:      cr.HTTPStatus,
			ServerState:     cr.ServerState,
			ServerID:        cr.ServerID,
			ServerStartTime: cr.ServerStartTime,
			FirstReadyzTime: cr.FirstReadyzTime,
			IsNonReadyReq:   cr.IsNonReadyReq,
			Error:           cr.Error,
		}
	}
	framework.Logf("[client-pod] fetched %d records from in-cluster client", len(records))
	return records
}

// pushTGSnapshotToAggregator sends a TG health snapshot to the aggregator
// via K8s API proxy. Non-blocking — errors are logged but don't fail the test.
func pushTGSnapshotToAggregator(ctx context.Context, cs clientset.Interface, namespace string, snap health.TargetSnapshot) {
	payload := struct {
		Timestamp      time.Time         `json:"timestamp"`
		Targets        map[string]string `json:"targets"`
		HealthyCount   int               `json:"healthy_count"`
		UnhealthyCount int               `json:"unhealthy_count"`
		InitialCount   int               `json:"initial_count"`
	}{
		Timestamp:      snap.Timestamp,
		Targets:        snap.Targets,
		HealthyCount:   snap.HealthyCount,
		UnhealthyCount: snap.UnhealthyCount,
		InitialCount:   snap.InitialCount,
	}
	data, _ := json.Marshal(payload)
	cs.CoreV1().RESTClient().Post().
		AbsPath(fmt.Sprintf("/api/v1/namespaces/%s/pods/healthtest-aggregator:%d/proxy/tg-snapshot", namespace, aggregatorPort)).
		Body(data).
		Do(ctx)
}

// fetchAggregatorTimeline retrieves the merged event timeline from the aggregator.
func fetchAggregatorTimeline(ctx context.Context, cs clientset.Interface, namespace string) []map[string]interface{} {
	result := cs.CoreV1().RESTClient().Get().
		AbsPath(fmt.Sprintf("/api/v1/namespaces/%s/pods/healthtest-aggregator:%d/proxy/timeline", namespace, aggregatorPort)).
		Timeout(30 * time.Second).
		Do(ctx)
	raw, _ := result.Raw()
	var timeline []map[string]interface{}
	json.Unmarshal(raw, &timeline)
	return timeline
}

// ─── CLB (Classic Load Balancer) support ────────────────────────────────────

// setupHealthTransitionCLB creates the same infrastructure as setupHealthTransition
// but uses a Classic Load Balancer instead of NLB. The CLB observer uses the
// ELB v1 DescribeInstanceHealth API. Everything else (healthserver deployment,
// aggregator, in-cluster client) is identical.
func setupHealthTransitionCLB(
	ctx context.Context,
	cs clientset.Interface,
	ns *v1.Namespace,
	deployName, svcName, image string,
	replicas int32,
	startupDelay time.Duration,
) (lbDNS string, clbObserver *health.CLBObserver, cfg serviceConfig, setupTimes transitionTimeline, clientPodName string) {

	// Deploy aggregator first
	By("deploying aggregator pod + service on worker node")
	aggregatorURL := deployAggregator(ctx, cs, ns.Name, image)
	framework.Logf("[aggregator] ready at %s", aggregatorURL)

	// SCC for hostNetwork
	By("granting privileged SCC to default service account")
	grantHostNetworkSCC(ctx, cs, ns.Name)

	// Healthserver deployment (same as NLB)
	By("creating healthserver Deployment (scheduled on master nodes, hostNetwork)")
	deploy := buildHealthserverDeployment(ns.Name, deployName, replicas, startupDelay, image, aggregatorURL)
	setupTimes.T0 = time.Now()
	_, err := cs.AppsV1().Deployments(ns.Name).Create(ctx, deploy, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create deployment")

	// CLB Service (no nlb annotation = CLB default)
	By("creating CLB Service (master-only targets, cross-zone, /readyz HC)")
	svc := buildHealthTransitionServiceCLB(ns.Name, svcName, deployName)
	_, err = cs.CoreV1().Services(ns.Name).Create(ctx, svc, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create CLB service")
	cfg.ServiceAnnotations = svc.Annotations

	cfg.Platform = "AWS"
	if region, rErr := common.GetRegionFromInfrastructure(ctx); rErr == nil {
		cfg.Region = region
	}
	if isExternal, tErr := common.IsExternalTopology(ctx); tErr == nil {
		if isExternal {
			cfg.Topology = "External (HyperShift)"
		} else {
			cfg.Topology = "HighlyAvailable"
		}
	}

	DeferCleanup(func(cleanupCtx context.Context) {
		framework.Logf("cleaning up CLB health transition resources")
		_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, svcName, metav1.DeleteOptions{})
		_ = cs.AppsV1().Deployments(ns.Name).Delete(cleanupCtx, deployName, metav1.DeleteOptions{})
		_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
		_ = cs.CoreV1().Services(ns.Name).Delete(cleanupCtx, "healthtest-aggregator", metav1.DeleteOptions{})
		_ = cs.CoreV1().Pods(ns.Name).Delete(cleanupCtx, "healthtest-client", metav1.DeleteOptions{})
		if lbDNS != "" {
			// CLB deletion is handled by cloud-provider-aws when the Service is deleted
			waitForLBDeletion(cleanupCtx, lbDNS)
		}
	})

	// Wait for deployment
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
	setupTimes.T1 = time.Now()

	// Wait for CLB provisioning
	By("waiting for CLB provisioning")
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
	framework.ExpectNoError(err, "CLB provisioning")
	setupTimes.T2 = time.Now()
	cfg.LBDNS = lbDNS

	// Discover CLB by DNS name
	By("discovering CLB by DNS name")
	elbClient, err := createAWSClientCLB(ctx)
	framework.ExpectNoError(err, "create CLB client")

	lbName, err := getCLBByDNSNameWithRetry(ctx, elbClient, lbDNS)
	framework.ExpectNoError(err, "find CLB")
	cfg.LBARN = lbName // CLB uses name, not ARN
	cfg.TGTargetType = "instance (CLB)"

	// Create CLB observer
	clbObserver = health.NewCLBObserver(elbClient, lbName, 1*time.Second)

	// Wait for all instances healthy
	By("waiting for ALL CLB instances to become healthy")
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		snap, pollErr := clbObserver.PollOnce(ctx)
		if pollErr != nil {
			return false, nil
		}
		total := snap.HealthyCount + snap.UnhealthyCount + snap.InitialCount
		allHealthy := total > 0 && snap.UnhealthyCount == 0 && snap.InitialCount == 0
		if time.Now().Second()%10 == 0 {
			var details []string
			for id, state := range snap.Targets {
				details = append(details, fmt.Sprintf("%s=%s", id, state))
			}
			framework.Logf("[clb-wait] healthy=%d unhealthy=%d initial=%d total=%d | %s",
				snap.HealthyCount, snap.UnhealthyCount, snap.InitialCount, total,
				strings.Join(details, ", "))
		}
		if allHealthy {
			framework.Logf("[clb-wait] all %d instances healthy", snap.HealthyCount)
		}
		return allHealthy, nil
	})
	framework.ExpectNoError(err, "all CLB instances healthy")
	setupTimes.T3 = time.Now()

	// Deploy in-cluster client
	By("deploying in-cluster client on worker node")
	clientPodName = deployInClusterClient(ctx, cs, ns.Name, image, lbDNS, aggregatorURL,
		defaultClientWorkers, defaultClientInterval)

	return lbDNS, clbObserver, cfg, setupTimes, clientPodName
}

// buildHealthTransitionServiceCLB creates a Service for a Classic Load Balancer.
// CLB is the default when no aws-load-balancer-type annotation is set.
// HC annotations are set to match the NLB test for fair comparison:
// HTTP /readyz on port 19443, interval=10s, threshold=2/2.
func buildHealthTransitionServiceCLB(namespace, name, deployName string) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				// NO aws-load-balancer-type annotation = CLB (default)
				"service.beta.kubernetes.io/aws-load-balancer-target-node-labels":                "node-role.kubernetes.io/control-plane=",
				"service.beta.kubernetes.io/aws-load-balancer-cross-zone-load-balancing-enabled": "true",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol":              "HTTP",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-path":                  "/readyz",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-port":                  fmt.Sprintf("%d", healthserverPort),
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval":              "10",
				"service.beta.kubernetes.io/aws-load-balancer-healthcheck-healthy-threshold":     "2",
				// CLB default unhealthy threshold is 6 — set to 2 for fair comparison with NLB
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
				Port:       int32(healthserverPort),
				TargetPort: intstr.FromInt(healthserverPort),
			}},
		},
	}
}

// waitForCLBUnhealthy blocks until at least one CLB instance reports OutOfService.
func waitForCLBUnhealthy(ctx context.Context, observer *health.CLBObserver, timeout time.Duration) {
	_ = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		snap, err := observer.PollOnce(ctx)
		if err != nil {
			return false, nil
		}
		if snap.UnhealthyCount > 0 {
			framework.Logf("[clb-wait] detected %d unhealthy instance(s)", snap.UnhealthyCount)
			return true, nil
		}
		return false, nil
	})
}

// startCLBSnapshotPusher pushes CLB health snapshots to the aggregator every 2s.
func startCLBSnapshotPusher(ctx context.Context, cs clientset.Interface, namespace string, observer *health.CLBObserver) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap, err := observer.PollOnce(ctx)
				if err != nil {
					continue
				}
				pushTGSnapshotToAggregator(ctx, cs, namespace, snap)
			}
		}
	}()
	return cancel
}

func ptrBool(b bool) *bool    { return &b }
func ptrInt64(i int64) *int64 { return &i }
