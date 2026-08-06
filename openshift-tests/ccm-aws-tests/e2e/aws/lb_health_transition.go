package aws

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
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

	// kasShutdownDelay matches the KAS shutdown-delay-duration (135s graceful +
	// margin), simulating how long KAS keeps serving after /readyz→503 before
	// the process exits.  CKAO sets 135s; we add buffer for HC propagation.
	kasShutdownDelay = 192 * time.Second
)

// transitionTimeline captures all timing milestones from the SPLAT-307 state
// machine extended with restart-phase timers (t7.1–t7.4) for OCPBUGS-86789.
//
// A zero time.Time means the milestone was not observed.
type transitionTimeline struct {
	// Shutdown phase (SPLAT-307 path)
	T5  time.Time // readyz→503 signal sent
	T6  time.Time // first observer event: target unhealthy
	T7  time.Time // last client request routed to target after t5

	// Restart phase (Scenario 5.5 only; zero for 5.2)
	T71 time.Time // pod delete sent
	T73 time.Time // new pod TCP up (from X-Server-Start-Time header)
	T74 time.Time // first pre-readyz request from new pod (BUG if present)

	// Startup phase
	T8  time.Time // readyz→200 (from X-First-Readyz-Time header or admin signal)
	T9  time.Time // first observer event: target healthy after t8
	T10 time.Time // first client request to target after t9

	// Counters
	UnhealthyReqCount int // requests served by target between t5 and t7
	PreReadyzReqCount int // requests with X-Server-State: pre-readyz

	// Identity
	TargetPod  string
	TargetNode string
	NewPod     string
}

// serviceConfig records Service and TG configuration for the report.
type serviceConfig struct {
	ServiceAnnotations map[string]string
	TGAttributes       []health.TGAttribute
	TGARN              string
	TGTargetType       string
	LBARN              string
	LBDNS              string
}

var _ = Describe(healthTransitionTestPrefix+" NLB", func() {
	f := framework.NewDefaultFramework("cloud-provider-aws")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	var cs clientset.Interface
	var ns *v1.Namespace

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
	})

	// ── Scenario 5.5 ───────────────────────────────────────────────────
	Context("pre-readyz routing detection (OCPBUGS-86789)", func() {
		It("should not route to pre-readyz targets "+
			"when healthy targets are available", func(ctx context.Context) {

			image := os.Getenv(envHealthserverImage)
			if image == "" {
				Skip(fmt.Sprintf("%s not set", envHealthserverImage))
			}

			replicas := int32(3)
			startupDelay := 60 * time.Second
			shutdownDelay := kasShutdownDelay
			clientInterval := 500 * time.Millisecond
			observerInterval := 1 * time.Second
			steadyStateDuration := 30 * time.Second

			deployName := "healthserver"
			svcName := "healthserver-lb"

			lbDNS, observer, svcCfg := setupHealthTransition(
				ctx, cs, ns, deployName, svcName, image,
				replicas, startupDelay, observerInterval,
			)

			observer.Start(ctx)
			client := health.NewClient(fmt.Sprintf("http://%s/", lbDNS), clientInterval)
			client.Start(ctx)
			defer func() { client.Stop(); observer.Stop() }()

			By(fmt.Sprintf("verifying steady state for %s", steadyStateDuration))
			time.Sleep(steadyStateDuration)

			steadyRecords := client.Records()
			steadyNonReady := 0
			for _, r := range steadyRecords {
				if r.IsNonReadyReq {
					steadyNonReady++
				}
			}
			Expect(steadyNonReady).To(Equal(0), "pre-readyz responses during steady state")

			pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app=%s", deployName),
			})
			framework.ExpectNoError(err)
			Expect(len(pods.Items)).To(BeNumerically(">=", int(replicas)))

			// Build knownServers from ALL existing pods (not client records,
			// which may miss pods due to NLB routing distribution).
			knownServers := make(map[string]bool)
			for _, p := range pods.Items {
				knownServers[p.Name] = true
			}

			targetPod := pods.Items[0].Name
			targetNode := pods.Items[0].Spec.NodeName

			By("signaling target pod readyz→503 (t5)")
			t5 := time.Now()
			err = sendAdminSignal(ctx, cs, ns.Name, targetPod, false)
			framework.ExpectNoError(err, "signal readyz→false")

			By(fmt.Sprintf("waiting %s shutdown-delay before pod deletion", shutdownDelay))
			time.Sleep(shutdownDelay)

			By("deleting target pod (t7.1)")
			t71 := time.Now()
			err = cs.CoreV1().Pods(ns.Name).Delete(ctx, targetPod, metav1.DeleteOptions{})
			framework.ExpectNoError(err)

			By("waiting for replacement pod")
			newPod := waitForNewPod(ctx, cs, ns.Name, deployName, targetPod)

			observeDuration := startupDelay + 3*time.Minute
			By(fmt.Sprintf("observing for %s (startup-delay + propagation buffer)", observeDuration))
			time.Sleep(observeDuration)

			allRecords := client.Records()
			allEvents := observer.Events()

			tl := computeTimeline(targetPod, knownServers, t5, t71, allRecords, allEvents)
			tl.TargetPod = targetPod
			tl.TargetNode = targetNode
			tl.NewPod = newPod

			report := buildReport("5.5 (Pre-Readyz Routing / OCPBUGS-86789)",
				tl, svcCfg, replicas, startupDelay, shutdownDelay,
				allEvents, observer.Snapshots())

			if tl.PreReadyzReqCount > 0 {
				report += fmt.Sprintf("\nVERDICT: NLB routed %d request(s) to pre-readyz target(s) — OCPBUGS-86789 reproduced\n", tl.PreReadyzReqCount)
			} else {
				report += "\nVERDICT: No pre-readyz routing detected in this iteration\n"
			}

			framework.Logf("\n%s", report)
		})
	})

	// ── Scenario 5.2 ───────────────────────────────────────────────────
	Context("shutdown propagation measurement (SPLAT-307)", func() {
		It("should stop routing within shutdown-delay after "+
			"readyz starts failing", func(ctx context.Context) {

			image := os.Getenv(envHealthserverImage)
			if image == "" {
				Skip(fmt.Sprintf("%s not set", envHealthserverImage))
			}

			replicas := int32(3)
			startupDelay := 60 * time.Second
			clientInterval := 500 * time.Millisecond
			observerInterval := 1 * time.Second
			steadyStateDuration := 30 * time.Second
			shutdownObserveDuration := 3 * time.Minute
			recoveryObserveDuration := 3 * time.Minute

			deployName := "healthserver"
			svcName := "healthserver-lb"

			lbDNS, observer, svcCfg := setupHealthTransition(
				ctx, cs, ns, deployName, svcName, image,
				replicas, startupDelay, observerInterval,
			)

			observer.Start(ctx)
			client := health.NewClient(fmt.Sprintf("http://%s/", lbDNS), clientInterval)
			client.Start(ctx)
			defer func() { client.Stop(); observer.Stop() }()

			By(fmt.Sprintf("verifying steady state for %s", steadyStateDuration))
			time.Sleep(steadyStateDuration)

			pods, err := cs.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app=%s", deployName),
			})
			framework.ExpectNoError(err)
			targetPod := pods.Items[0].Name
			targetNode := pods.Items[0].Spec.NodeName

			By("signaling target pod readyz→503 (t5)")
			t5 := time.Now()
			err = sendAdminSignal(ctx, cs, ns.Name, targetPod, false)
			framework.ExpectNoError(err)

			By(fmt.Sprintf("observing shutdown propagation for %s", shutdownObserveDuration))
			time.Sleep(shutdownObserveDuration)

			By("signaling target pod readyz→200 (t8)")
			t8 := time.Now()
			err = sendAdminSignal(ctx, cs, ns.Name, targetPod, true)
			framework.ExpectNoError(err)

			By(fmt.Sprintf("observing recovery for %s", recoveryObserveDuration))
			time.Sleep(recoveryObserveDuration)

			allRecords := client.Records()
			allEvents := observer.Events()

			tl := computeTimeline52(targetPod, t5, t8, allRecords, allEvents)
			tl.TargetPod = targetPod
			tl.TargetNode = targetNode

			report := buildReport("5.2 (Shutdown Propagation / SPLAT-307)",
				tl, svcCfg, replicas, startupDelay, 0,
				allEvents, observer.Snapshots())

			report += fmt.Sprintf("\nVERDICT: NLB routed %d request(s) to unhealthy target after readyz→503\n", tl.UnhealthyReqCount)
			if !tl.T7.IsZero() && !tl.T5.IsZero() {
				report += fmt.Sprintf("T_route_stop = %s (NLB kept routing after readyz→503)\n",
					tl.T7.Sub(tl.T5).Truncate(time.Second))
			}

			framework.Logf("\n%s", report)
		})
	})
})

// ─── Setup helper ───────────────────────────────────────────────────────────

func setupHealthTransition(
	ctx context.Context,
	cs clientset.Interface,
	ns *v1.Namespace,
	deployName, svcName, image string,
	replicas int32,
	startupDelay time.Duration,
	observerInterval time.Duration,
) (lbDNS string, observer *health.Observer, cfg serviceConfig) {

	By("creating healthserver Deployment")
	deploy := buildHealthserverDeployment(ns.Name, deployName, replicas, startupDelay, image)
	_, err := cs.AppsV1().Deployments(ns.Name).Create(ctx, deploy, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create deployment")

	By("creating NLB Service with /readyz health check")
	svc := buildHealthTransitionService(ns.Name, svcName, deployName)
	_, err = cs.CoreV1().Services(ns.Name).Create(ctx, svc, metav1.CreateOptions{})
	framework.ExpectNoError(err, "create service")
	cfg.ServiceAnnotations = svc.Annotations

	DeferCleanup(func(cleanupCtx context.Context) {
		framework.Logf("cleaning up health transition resources")
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
	cfg.LBDNS = lbDNS

	By("discovering NLB and target group in AWS")
	elbClient, err := createAWSClientLoadBalancer(ctx)
	framework.ExpectNoError(err, "create ELB client")

	foundLB, err := getAWSLoadBalancerFromDNSName(ctx, elbClient, lbDNS)
	framework.ExpectNoError(err, "find NLB")
	cfg.LBARN = aws.ToString(foundLB.LoadBalancerArn)

	observer = health.NewObserver(elbClient, observerInterval)
	err = observer.DiscoverTargetGroup(ctx, cfg.LBARN)
	framework.ExpectNoError(err, "discover target group")
	cfg.TGARN = observer.TargetGroupARN()
	cfg.TGTargetType = observer.TargetType()

	tgAttrs, err := observer.DescribeTGAttributes(ctx)
	if err == nil {
		cfg.TGAttributes = tgAttrs
	}

	// Fetch TG health check config from the TG itself
	fetchTGHealthCheckConfig(ctx, elbClient, &cfg)

	By("waiting for all TG targets to become healthy")
	err = observer.WaitForAllHealthy(ctx, int(replicas), 10*time.Minute)
	framework.ExpectNoError(err, "targets healthy")

	return lbDNS, observer, cfg
}

func fetchTGHealthCheckConfig(ctx context.Context, elbClient *elbv2.Client, cfg *serviceConfig) {
	out, err := elbClient.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{cfg.TGARN},
	})
	if err != nil || len(out.TargetGroups) == 0 {
		return
	}
	tg := out.TargetGroups[0]
	cfg.TGAttributes = append(cfg.TGAttributes,
		health.TGAttribute{Key: "_hc_protocol", Value: string(tg.HealthCheckProtocol)},
		health.TGAttribute{Key: "_hc_port", Value: aws.ToString(tg.HealthCheckPort)},
		health.TGAttribute{Key: "_hc_path", Value: aws.ToString(tg.HealthCheckPath)},
		health.TGAttribute{Key: "_hc_interval_seconds", Value: fmt.Sprintf("%d", aws.ToInt32(tg.HealthCheckIntervalSeconds))},
		health.TGAttribute{Key: "_hc_healthy_threshold", Value: fmt.Sprintf("%d", aws.ToInt32(tg.HealthyThresholdCount))},
		health.TGAttribute{Key: "_hc_unhealthy_threshold", Value: fmt.Sprintf("%d", aws.ToInt32(tg.UnhealthyThresholdCount))},
	)
}

// ─── Admin API via K8s API server proxy ─────────────────────────────────────

func sendAdminSignal(ctx context.Context, cs clientset.Interface, namespace, podName string, ready bool) error {
	readyStr := "false"
	if ready {
		readyStr = "true"
	}
	result := cs.CoreV1().RESTClient().Post().
		AbsPath(fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:8080/proxy/admin/readyz", namespace, podName)).
		Param("ready", readyStr).
		Do(ctx)
	return result.Error()
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

// ─── Timeline computation ───────────────────────────────────────────────────

func computeTimeline(
	oldPod string,
	knownServers map[string]bool,
	t5, t71 time.Time,
	records []health.RequestRecord,
	events []health.HealthEvent,
) transitionTimeline {
	tl := transitionTimeline{T5: t5, T71: t71}

	// t6: first observer event showing a target transitioning healthy→unhealthy
	// AFTER t5 (when we signaled readyz→503). Excludes initial→unhealthy which
	// are nodes that never had local pods and failed HC from the start.
	for _, e := range events {
		if e.Timestamp.Before(t5) {
			continue
		}
		if e.State == "unhealthy" && e.PrevState == "healthy" {
			tl.T6 = e.Timestamp
			break
		}
	}

	// t7: last request served by the OLD target pod after t5.
	// Each request after readyz→503 counts as an "unhealthy" request.
	for _, r := range records {
		if r.Timestamp.Before(t5) {
			continue
		}
		if r.ServerID == oldPod {
			tl.T7 = r.Timestamp
			tl.UnhealthyReqCount++
		}
	}

	// Identify the new pod: first ServerID not in knownServers, after t7.1
	for _, r := range records {
		if r.ServerID == "" || knownServers[r.ServerID] || r.Timestamp.Before(t71) {
			continue
		}
		tl.NewPod = r.ServerID
		break
	}

	// Now process only responses from the identified new pod
	for _, r := range records {
		if r.ServerID != tl.NewPod || r.Timestamp.Before(t71) {
			continue
		}

		// t7.3: first response from new pod (approximates TCP up)
		if tl.T73.IsZero() {
			tl.T73 = r.Timestamp
		}

		// t7.4: first pre-readyz request from new pod
		if r.IsNonReadyReq {
			tl.PreReadyzReqCount++
			if tl.T74.IsZero() {
				tl.T74 = r.Timestamp
			}
		}

		// t8: when the new pod's /readyz first returned 200 (from header, local time)
		if tl.T8.IsZero() && r.ServerState == "ready" && r.FirstReadyzTime != "never" && r.FirstReadyzTime != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, r.FirstReadyzTime); err == nil {
				tl.T8 = parsed.Local()
			}
		}

		// t10: first client request where new pod reports "ready"
		if tl.T10.IsZero() && r.ServerState == "ready" {
			tl.T10 = r.Timestamp
		}
	}

	// t9: first observer healthy event AFTER t71 (pod restart), not after t8
	// (t8 may be wrong or zero). Look for the healthy transition that corresponds
	// to the new pod coming online.
	for _, e := range events {
		if e.Timestamp.Before(t71) {
			continue
		}
		if e.State == "healthy" && (e.PrevState == "unhealthy" || e.PrevState == "initial") {
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
		if e.State == "unhealthy" && e.PrevState == "healthy" {
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
		if e.State == "healthy" && e.PrevState == "unhealthy" {
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

func fmtT(t time.Time) string {
	if t.IsZero() {
		return "N/A"
	}
	return t.Format(time.RFC3339)
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
	events []health.HealthEvent,
	snapshots []health.TargetSnapshot,
) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	sep := "═══════════════════════════════════════════════════════════════════════════"

	w(sep)
	w("HEALTH TRANSITION REPORT — Scenario %s", scenario)
	w(sep)

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

	// ── Service config ──
	w("")
	w("SERVICE CONFIGURATION")
	w("  LB DNS:         %s", cfg.LBDNS)
	w("  LB ARN:         %s", cfg.LBARN)
	for k, v := range cfg.ServiceAnnotations {
		short := strings.TrimPrefix(k, "service.beta.kubernetes.io/aws-load-balancer-")
		w("  svc/%s: %s", short, v)
	}

	// ── TG config ──
	w("")
	w("TARGET GROUP CONFIGURATION")
	w("  TG ARN:         %s", cfg.TGARN)
	w("  Target Type:    %s", cfg.TGTargetType)
	for _, a := range cfg.TGAttributes {
		if a.Key == "" {
			continue
		}
		w("  %s: %s", a.Key, a.Value)
	}

	// ── Timing table ──
	w("")
	w("TIMING TABLE")
	w("%-25s %-14s %-14s %s", "Metric", "Value", "Expected", "Description")
	w("%-25s %-14s %-14s %s", strings.Repeat("─", 25), strings.Repeat("─", 14), strings.Repeat("─", 14), strings.Repeat("─", 30))
	w("%-25s %-14s %-14s %s", "T_tg_unhealthy", fmtDur(tl.T5, tl.T6), "~20s", "t6-t5: HC detect unhealthy")
	w("%-25s %-14s %-14s %s", "T_route_stop", fmtDur(tl.T5, tl.T7), "<shutdown-delay", "t7-t5: last req after readyz→503")
	w("%-25s %-14d %-14s %s", "Unhealthy_reqs", tl.UnhealthyReqCount, "0 ideal", "requests to target after readyz→503")
	if !tl.T71.IsZero() {
		w("%-25s %-14s %-14s %s", "T_pod_restart", fmtDur(tl.T71, tl.T73), "seconds", "t7.3-t7.1: pod kill→TCP up")
		w("%-25s %-14d %-14s %s", "Pre_readyz_reqs", tl.PreReadyzReqCount, "0", "requests before readyz→200 (BUG)")
	}
	w("%-25s %-14s %-14s %s", "T_tg_healthy", fmtDur(tl.T8, tl.T9), "~20s", "t9-t8: HC detect healthy")
	w("%-25s %-14s %-14s %s", "T_route_start", fmtDur(tl.T8, tl.T10), "20-120s", "t10-t8: first req after readyz→200")
	w("%-25s %-14s %-14s %s", "T_total_cycle", fmtDur(tl.T5, tl.T10), "", "t10-t5: full cycle")

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

	addEntry(tl.T5, "t5  readyz→503", "")
	addEntry(tl.T6, "t6  TG unhealthy", fmtDelta(tl.T5, tl.T6))
	addEntry(tl.T7, "t7  last routed req", fmtDelta(tl.T5, tl.T7))
	addEntry(tl.T71, "t7.1 pod deleted", fmtDelta(tl.T5, tl.T71))
	addEntry(tl.T73, "t7.3 new TCP up", fmtDelta(tl.T71, tl.T73))
	if !tl.T74.IsZero() {
		addEntry(tl.T74, "t7.4 pre-readyz req ← BUG", fmtDelta(tl.T73, tl.T74))
	}
	addEntry(tl.T8, "t8  readyz→200", fmtDelta(tl.T5, tl.T8))
	addEntry(tl.T9, "t9  TG healthy", fmtDelta(tl.T8, tl.T9))
	addEntry(tl.T10, "t10 first routed req", fmtDelta(tl.T8, tl.T10))

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
