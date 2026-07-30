# Bumping Kubernetes and Go Versions for cluster-cloud-controller-manager-operator

This document outlines the process for updating Kubernetes and Go versions across this project, designed for both AI agents and manual use.

## Key Phases

### Prerequisites (Must Complete First)

These OpenShift repositories must have updated `release-4.XX` branches **before** starting:
- `openshift/api` → record latest commit SHA on `release-4.XX`
- `openshift/client-go` → record latest commit SHA on `release-4.XX`
- `openshift/library-go` → record latest commit SHA on `release-4.XX`
- `openshift/cluster-api-actuator-pkg` → record latest commit SHA on `release-4.XX`
- `openshift/controller-runtime-common` → record latest commit SHA on `release-4.XX`

Also verify:
- Upstream `k8s.io/api`, `k8s.io/client-go`, etc. have the target `v0.XX.Y` tags published
- `sigs.k8s.io/controller-runtime` has a compatible version for the target k8s minor
- Cloud provider modules are available: `k8s.io/cloud-provider-aws`, `sigs.k8s.io/cloud-provider-azure`, `k8s.io/cloud-provider-vsphere`
- `openshift/onsi-ginkgo` has a `v2.XX.Y-openshift-4.XX` branch (needed by `openshift-tests/operator-tests` replace directive)

### Research Phase (Step 1)

Gather these values before touching any files:
- `K8S_VERSION` — latest stable patch release (e.g. `v0.36.3` for k8s 1.36)
- `GO_MINOR` — Go minor version matching the Dockerfile builder image (e.g. `1.26`)
- `CONTROLLER_RUNTIME_VERSION` — compatible version (controller-runtime vN aligns with k8s 1.N+12, e.g. v0.24.x for k8s 1.36)
- `CONTROLLER_TOOLS_VERSION` — compatible version (e.g. v0.21.0)
- `CLOUD_PROVIDER_AWS_VERSION` — e.g. `v1.36.1`
- `CLOUD_PROVIDER_AZURE_VERSION` — e.g. `v1.36.3`
- `CLOUD_PROVIDER_AZURE_AZCLIENT_VERSION` — check azure's go.mod for its azclient dep
- `CLOUD_PROVIDER_VSPHERE_VERSION` — e.g. `v1.36.0`
- `OCP_RELEASE` — mapped from k8s minor (e.g. k8s 1.36 → `release-4.23`)

Verify cloud-provider-vsphere's go.mod: if it properly vendors the target k8s version, the `replace` directive in go.mod can be **removed**. If it vendors a different k8s version (alpha/beta), a replace to `openshift-cloud-team/cloud-provider-vsphere` may be needed.

### Modules in This Repo

This repo uses a Go workspace (`go.work`) with:
- **Root module** (`.`) — the main operator, uses workspace
- **operator-tests** (`openshift-tests/operator-tests`) — in workspace, has k8s.io/kubernetes replace directives
- **ccm-aws-tests** (`openshift-tests/ccm-aws-tests`) — **outside** workspace (`GOWORK=off`), vendored separately

### Core Changes (Steps 2–4)

#### Step 2: Update root `go.mod`
- Bump `go` directive to `GO_MINOR` (e.g. `go 1.26.0`)
- Add or remove `replace` directives as needed (see vsphere note above)
- Use `go get` to bump all direct k8s.io deps to `K8S_VERSION`
- Use `go get` to bump controller-runtime, controller-tools, cloud providers
- Use `go get` to bump OpenShift deps to `release-4.XX` branches:
  ```
  go get github.com/openshift/api@release-4.XX
  go get github.com/openshift/client-go@release-4.XX
  go get github.com/openshift/library-go@release-4.XX
  go get github.com/openshift/cluster-api-actuator-pkg/testutils@release-4.XX
  go get github.com/openshift/controller-runtime-common@release-4.XX
  ```

#### Step 3: Update `openshift-tests/operator-tests/go.mod`
- Bump `go` directive
- Bump k8s.io direct deps (`api`, `apimachinery`, `client-go`, `component-base`)
- Bump `k8s.io/kubernetes` to `v1.XX.Y`
- Update **all** `replace` directives from `v0.OLD.0` to `v0.NEW.0` — these are required because `k8s.io/kubernetes` references staging modules at `v0.0.0`
- Update the `openshift/onsi-ginkgo/v2` replace to the `v2.XX.Y-openshift-4.XX` branch pseudo-version (check what `openshift-tests/ccm-aws-tests/go.mod` uses as a reference)
- Check for new staging modules in k8s.io/kubernetes that need replace directives:
  ```
  curl -s "https://proxy.golang.org/k8s.io/kubernetes/@v/v1.XX.Y.mod" | grep 'v0.0.0' | awk '{print $1}' | sort
  ```

#### Step 4: Tidy and sync
```bash
go work sync
go mod tidy
cd openshift-tests/operator-tests && go mod tidy
```

Do **NOT** vendor yet — that's a separate commit.

#### Step 5: Vendor
```bash
bash hack/go-mod.sh
```
This script handles:
1. `openshift-tests/ccm-aws-tests` vendoring (outside workspace, `GOWORK=off`)
2. Workspace module discovery, two-pass `go mod tidy`, `go work sync`, `go mod verify`
3. Unified `go work vendor -v`

### Build Infrastructure (Step 6)

Check and update if needed:
- `Dockerfile` — builder image tag (e.g. `rhel-9-golang-1.26-openshift-5.0`)
- `Makefile` — `ENVTEST_K8S_VERSION` if newer envtest assets are available

### Common Code Breakages (Step 7)

Historical bumps have required fixes for:
- `go vet` stricter checks in newer Go versions (e.g. non-constant format strings in `Eventf` calls)
- Ginkgo fork version mismatches (k8s.io/kubernetes test framework requires features from newer ginkgo)
- Cloud provider API changes
- controller-runtime interface changes

### Commit Structure (Step 8)

Three separate commits, matching the standard pattern (see PR #439 for prior art):

1. **Version bump** — `go.mod`, `go.sum`, `go.work.sum`, and `openshift-tests/operator-tests/go.mod` + `go.sum` only
   ```
   JIRA-ID: Bump k8s to X.XX.Y dependencies
   ```

2. **Vendor** — `vendor/` directory and any `go.work.sum` changes only
   ```
   JIRA-ID: Vendor
   ```

3. **Code fixes** (if needed) — source code changes only, no go.mod or vendor changes
   ```
   JIRA-ID: Fix build after K8s X.XX bump
   ```

> **Note:** Do NOT push or create a PR unless the user asks.

### Verification (Step 9)

1. `make build` — all binaries compile
2. `go vet ./...` — no vet errors
3. `make test` — unit tests pass (integration tests via envtest are heavy, prefer CI)
4. `make lint` — no lint violations
