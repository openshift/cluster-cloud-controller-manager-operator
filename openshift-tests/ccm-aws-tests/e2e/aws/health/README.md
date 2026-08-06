# NLB Health Transition E2E Test Framework

Measures NLB target health state transition timing during pod lifecycle
events (shutdown, restart) to reproduce and characterize AWS NLB routing
behavior documented in [OCPBUGS-86789](https://redhat.atlassian.net/browse/OCPBUGS-86789)
and [SPLAT-307](https://redhat.atlassian.net/browse/SPLAT-307).

## Problem

During HA KAS rollouts on AWS, the NLB routes **new** TCP connections to a
freshly restarted KAS target whose TCP port is open but `/readyz` has not
yet returned HTTP 200 — even though other healthy KAS targets exist.  This
is **not** fail-open behavior (SNO is excluded).

The reverse direction (NLB keeps routing to an unhealthy target after
`/readyz` returns 503) was characterized in SPLAT-307 (2021-2022) and
mitigated with `shutdown-delay-duration=135s` in CKAO.

## Architecture

```text
openshift-tests/ccm-aws-tests/
├── cmd/healthserver/           # Standalone health-controllable HTTP server
│   ├── main.go                 # /readyz control, X-Server-State headers, admin API
│   └── Dockerfile              # Multi-stage scratch build (~10MB)
├── e2e/aws/
│   ├── lb_health_transition.go # Ginkgo test scenarios (5.5, 5.5-CAPA, 5.2)
│   └── health/                 # Extractable package (zero parent-path imports)
│       ├── types.go            # HealthEvent, RequestRecord, TargetSnapshot
│       ├── observer.go         # TG health polling, PollOnce, TG attribute R/W
│       ├── client.go           # HTTP client with httptrace (new TCP per request)
│       └── README.md           # This file
```

### Design constraints

- `health/` imports only stdlib, `k8s.io/*`, AWS SDK — no parent paths.
  This is the extractable unit if the framework moves to a standalone repo.
- `cmd/healthserver/` is a standalone binary — no K8s, no OCP dependencies.
  Built FROM scratch, ~10MB.  Used as the test workload inside the cluster.
- Tests reuse existing CCCMO OTE infrastructure: `loadAWSConfig`,
  `createAWSClientLoadBalancer`, `getAWSLoadBalancerFromDNSName`, etc.

## Timing Model (t0–t10)

Every test reports the same set of timers regardless of scenario.  Based on
the SPLAT-307 state machine extended with restart-phase timers for
OCPBUGS-86789.

```text
INITIAL REGISTRATION:
  t0    Deployment created (pods scheduling on control-plane nodes)
  t1    All pods Running
  t2    NLB provisioned (DNS assigned)
  t3    All TG targets healthy (HC passed + Hyperplane propagated)
  t4    First client request received

SHUTDOWN PHASE (SPLAT-307):
  t5    /readyz → 503 (admin signal via K8s API server pod proxy)
  t6    AWS API reports target unhealthy (DescribeTargetHealth)
  t7    Last client request routed to target

RESTART PHASE (Scenario 5.5 only):
  t7.1  Pod delete sent
  t7.3  New pod TCP up (first NLB-routed response)
  t7.4  First pre-readyz request from new pod (BUG if present)

STARTUP PHASE:
  t8    /readyz → 200 (from X-First-Readyz-Time header or admin signal)
  t9    AWS API reports target healthy
  t10   First client request to target
```

### Computed metrics

| Metric           | Formula     | Expected        | Description                          |
|------------------|-------------|-----------------|--------------------------------------|
| T_deploy_ready   | t1 - t0     |                 | Pod scheduling + startup             |
| T_nlb_provision  | t2 - t0     |                 | NLB creation in AWS                  |
| T_tg_initial     | t3 - t0     |                 | Full initial registration            |
| T_first_request  | t4 - t3     | seconds         | First routed request after healthy   |
| T_tg_unhealthy   | t6 - t5     | ~20s            | HC detect unhealthy (2×10s)          |
| T_route_stop     | t7 - t5     | < shutdown-delay| Hyperplane propagation (shutdown)    |
| T_pod_restart    | t7.3 - t7.1 | seconds         | Pod kill → new pod TCP up            |
| T_tg_healthy     | t9 - t8     | ~20s            | HC detect healthy (2×10s)            |
| T_route_start    | t10 - t8    | 20-120s         | Hyperplane propagation (startup)     |
| T_total_cycle    | t10 - t5    |                 | Full shutdown → healthy cycle        |

### SPLAT-307 correspondence

| SPLAT-307 Metric (2021)                        | New Timer       |
|------------------------------------------------|-----------------|
| Row 0: Total time to NLB transition Unhealthy  | T_tg_unhealthy  |
| Row 1: Total time receiving requests unhealthy | T_route_stop    |
| Row 2: Total requests received unhealthy       | Unhealthy_reqs  |
| Row 4: Total time to transition to Healthy     | T_tg_healthy    |
| Row 5: Total time to receive requests healthy  | T_route_start   |

## Test Scenarios

### Scenario 5.5 — Pre-Readyz Routing (OCPBUGS-86789)

Reproduces the NLB routing to a target before `/readyz` returns 200 while
other healthy targets exist.

```text
Test name (OTE):
  [cloud-provider-aws-e2e-openshift] loadbalancer health-transition NLB
  pre-readyz routing detection (OCPBUGS-86789)
  should not route to pre-readyz targets when healthy targets are available

Flow:
  1. Deploy 3 healthserver pods on control-plane nodes (--startup-delay=60s)
  2. Create NLB: control-plane-only targets, cross-zone, HTTP /readyz HC
  3. Wait for ALL TG targets healthy (zero initial/unhealthy)
  4. Start observer (1s) + client (200ms × 4 parallel workers)
  5. Observe 90s steady state (confirm all replicas receiving traffic)
  6. Signal target pod readyz→503 via K8s API proxy (t5)
  7. Wait 192s shutdown-delay (simulates KAS shutdown-delay-duration)
  8. Delete pod (t7.1), wait for replacement
  9. Wait for restarted target healthy + 90s post-recovery observation
  10. Report: timing table, request stats, phase breakdown, timeline

Detection: any response with X-Server-State: pre-readyz = BUG reproduced
```

### Scenario 5.5-CAPA — Pre-Readyz with CAPA TG Attributes

Same as 5.5 but applies TG attributes via `ModifyTargetGroupAttributes`
after TG creation:

```text
target_health_state.unhealthy.connection_termination.enabled = false
target_health_state.unhealthy.draining_interval_seconds = 300
```

These are the CAPA fix attributes (OCPBUGS-55626).  Tests whether they
affect the unhealthy→healthy routing transition.

**Finding (confirmed):** with `connection_termination.enabled=false`, the
NLB TG transitions through `unhealthy.draining` instead of `unhealthy`.
This answers v5 open question Q5 — `unhealthy.draining` occurs during
HC-driven transitions, not just during deregistration.  The timeline
computation uses `isUnhealthyState()` to match both states.

### Scenario 5.2 — Shutdown Propagation (SPLAT-307)

Measures how long the NLB routes to a target after `/readyz` fails (no pod
restart).  Revalidates the SPLAT-307 measurements with current AWS
infrastructure.

```text
Test name (OTE):
  [cloud-provider-aws-e2e-openshift] loadbalancer health-transition NLB
  shutdown propagation measurement (SPLAT-307)
  should stop routing within shutdown-delay after readyz starts failing

Flow:
  1. Setup (same as 5.5)
  2. Signal readyz→503 (t5), observe 3min shutdown propagation
  3. Signal readyz→200 (t8), observe 3min recovery
  4. Report timing table
```

## Components

### Healthserver (`cmd/healthserver/`)

Standalone Go HTTP server deployed as the test workload.

| Endpoint               | Purpose                                         |
|-------------------------|-------------------------------------------------|
| `GET /`                 | Main endpoint.  Returns `X-Server-State` header |
| `GET /readyz`           | Health check.  200 (ready) or 503 (not ready)   |
| `POST /admin/readyz`    | Control readyz: `?ready=true` or `?ready=false` |
| `POST /admin/shutdown`  | Graceful shutdown with optional `?delay=Ns`     |
| `GET /admin/lifecycle`  | JSON lifecycle timestamps                       |

Response headers on every `GET /`:

```text
X-Server-State: pre-readyz | ready | draining | shutdown
X-Server-ID: <pod-name>
X-Server-Start-Time: <RFC3339Nano>
X-First-Readyz-Time: <RFC3339Nano or "never">
```

Flags: `--port` (default 8080), `--startup-delay` (default 30s).

### Observer (`health/observer.go`)

Polls `DescribeTargetHealth` at configurable interval.  Records:

- **Transition events** (`HealthEvent`): state changes per target
- **Per-poll snapshots** (`TargetSnapshot`): full TG state with counts

Also provides:

- `PollOnce(ctx)` — single live API call (works without background loop)
- `DescribeTGAttributes(ctx)` — read TG config for report
- `ModifyTGAttributes(ctx, attrs)` — set TG attributes (CAPA variant)
- `WaitForAllHealthy(ctx, min, timeout)` — block until min targets healthy

### Client (`health/client.go`)

HTTP client with `httptrace` hooks.  Creates a **new TCP connection** per
request (`DisableKeepAlives`) to match NLB per-connection routing.

Runs **multiple parallel worker goroutines** (default: 4) so that
high-latency links (e.g., test runner in South America → NLB in us-east-1)
don't bottleneck throughput.  Each worker fires independently on its own
200ms ticker.  With ~1.8s RTT, 4 workers achieve ~2 req/s; closer to
the cluster, the same config gives ~20 req/s.

Captures per request: target IP, TCP dial duration, HTTP status,
`X-Server-State`, `X-Server-ID`, `X-First-Readyz-Time`.

Detection: `IsNonReadyReq = true` when `X-Server-State == "pre-readyz"`.

## Infrastructure

### Pod scheduling

Pods schedule on control-plane nodes to match KAS topology:

```yaml
nodeSelector:
  node-role.kubernetes.io/control-plane: ""
tolerations:
  - key: node-role.kubernetes.io/master
    effect: NoSchedule
  - key: node-role.kubernetes.io/control-plane
    effect: NoSchedule
```

### NLB Service annotations

```yaml
aws-load-balancer-type: nlb
aws-load-balancer-target-node-labels: node-role.kubernetes.io/control-plane=
aws-load-balancer-cross-zone-load-balancing-enabled: "true"
aws-load-balancer-healthcheck-protocol: HTTP
aws-load-balancer-healthcheck-path: /readyz
aws-load-balancer-healthcheck-port: traffic-port
aws-load-balancer-healthcheck-interval: "10"
aws-load-balancer-healthcheck-healthy-threshold: "2"
aws-load-balancer-healthcheck-unhealthy-threshold: "2"
```

`externalTrafficPolicy: Local` ensures per-node health tracking.

### Graceful shutdown simulation

The test signals the target pod via the K8s API server pod proxy (the
healthserver container is FROM scratch — no shell for exec):

```text
POST /api/v1/namespaces/{ns}/pods/{pod}:8080/proxy/admin/readyz?ready=false
```

Then waits 192s (KAS shutdown-delay-duration) before deleting the pod.

## How to Run

```sh
# 1. Build healthserver image
cd openshift-tests/ccm-aws-tests
podman build -t quay.io/<user>/healthserver:latest ./cmd/healthserver/
podman push quay.io/<user>/healthserver:latest

# 2. Build OTE binary
cd ../..
make cloud-controller-manager-aws-tests-ext

# 3. Run
export KUBECONFIG=/path/to/kubeconfig
export AWS_REGION=us-east-1
export HEALTHSERVER_IMAGE=quay.io/<user>/healthserver:latest
BIN=./openshift-tests/bin/cloud-controller-manager-aws-tests-ext

# Run all health-transition tests
while IFS= read -r t; do
  echo "=== Running: $t"
  $BIN run-test "$t" < /dev/null
done < <($BIN list tests 2>/dev/null \
  | grep -v -E '(I0|INFO)' \
  | jq -r '.[].name' \
  | grep "health-transition")

# Run a specific scenario
$BIN run-test "...(OCPBUGS-86789) should not route to pre-readyz targets..."
$BIN run-test "...(SPLAT-307) should stop routing within shutdown-delay..."
$BIN run-test "...(OCPBUGS-86789) should not route to pre-readyz targets with connection-termination..."
```

## Report Output

Each test produces a single-block report (one `framework.Logf` call to
avoid per-line logger timestamps) containing:

- **ENVIRONMENT**: platform, region, topology
- **TARGET**: pod name, node, new pod (if restart)
- **TEST PARAMETERS**: replicas, startup/shutdown delay, client interval
  and worker count
- **SERVICE CONFIGURATION**: LB DNS/ARN, all Service annotations
- **TARGET GROUP CONFIGURATION**: TG ARN, target type, all TG attributes
  including health check config
- **TIMING TABLE**: all computed metrics (t0–t10) with expected values
- **REQUEST STATISTICS**: total count, 2xx/4xx/5xx breakdown, errors
- **REQUEST BREAKDOWN BY PHASE**: per-phase (Warmup, Shutdown, Restart,
  Recovery) duration, request count, 2xx, errors, pre-readyz count
- **TIMELINE**: chronological merge of test milestones (t0–t10) and TG
  health events, full RFC3339 timestamps with deltas
- **TG SNAPSHOTS**: first/last poll with healthy/unhealthy/initial counts
- **VERDICT**: detection result

## Related Issues

| Issue         | Status        | Relevance                                    |
|---------------|---------------|----------------------------------------------|
| OCPBUGS-86789 | Critical, POST| NLB routing to unhealthy target (primary)    |
| OCPBUGS-55626 | Closed/Done   | CAPA TG attribute fix (conn termination)     |
| OCPBUGS-87972 | Major         | CF template TG attribute fix                 |
| SPLAT-307     | Closed/Done   | Original NLB investigation (2021-2022)       |
| SPLAT-443     | Closed        | Phase 2 follow-up (never completed)          |

## Next Steps

- Multiple iterations per scenario (configurable repeat count)
- Configurable delays via environment variables
- Per-second CSV output matching SPLAT-307 format
- CLB comparison variant (control group)
- JSON machine-readable report for cross-run comparison
- EC2 instance ID → node name mapping in observer events
- Scenario 5.6: node replacement (deregistration path)
- Scenario 5.7: connection termination regression guard (OCPBUGS-55626)
- Periodic CI job in CCCMO
- Multi-region runs (us-west-2, eu-west-1)
