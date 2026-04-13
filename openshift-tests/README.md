# OpenShift Cloud Controller Manager Operator Tests

This directory contains OpenShift Tests Extension (OTE) binaries for testing the cluster-cloud-controller-manager-operator.

## Structure

The test suite is organized into separate sub-projects, each with independent dependencies:

- **`aws-tests/`** - AWS-specific cloud controller manager tests
  - `e2e/aws/` - AWS-specific test implementations
  - `e2e/common/` - Common helper functions (feature gate checks)
  - `main.go` - Binary entry point
  - `go.mod` - Independent dependencies (includes AWS SDK)
- **`operator-tests/`** - Multi-platform operator conformance tests
  - `e2e/operator/` - Operator test implementations
  - `e2e/common/` - Common helper functions (client config)
  - `main.go` - Binary entry point
  - `go.mod` - Independent dependencies (no AWS SDK)
- **`bin/`** - Compiled test binaries

## Test Binaries

### 1. `cluster-cloud-controller-manager-operator-tests-ext`

**Purpose:** General cloud controller manager operator tests that run on multiple platforms

**Suites:**
- `ccm/operator/conformance/parallel` - Parallel conformance tests
- `ccm/operator/conformance/serial` - Serial conformance tests

**Test Selection:**
- Platform-agnostic tests that work across cloud providers
- OpenShift-specific CCM operator functionality tests
- Automatically filters tests based on platform labels (e.g., `[platform:vsphere]`)

**Use Cases:**
- General operator conformance testing
- Multi-platform test runs
- OpenShift-specific feature validation (e.g., VSphereMixedNodeEnv)

### 2. `cloud-controller-manager-aws-tests-ext`

**Purpose:** AWS-specific cloud controller manager tests

**Suite:** `ccm/aws/conformance/parallel`

**Test Selection:**
- AWS load balancer tests (`[cloud-provider-aws-e2e] loadbalancer`)
- AWS node tests (`[cloud-provider-aws-e2e] nodes`)
- OpenShift AWS-specific tests (`[cloud-provider-aws-e2e-openshift]`)
- Filters out ECR tests and Serial tests

**Platform Filter:** AWS only (`PlatformEquals("aws")`)

**Topology Exclusions:**
- On SingleReplica topology, excludes:
  - "nodes should label nodes with topology network info if instance is supported"
  - "nodes should set zone-id topology label"


## Test Organization

### AWS Tests (`aws-tests/e2e/`)

- `aws/helper.go` - AWS helper functions (feature gate checks, AWS clients)
- `aws/loadbalancer.go` - AWS NLB and load balancer tests
- `common/helper.go` - Feature gate checking (`IsFeatureEnabled`)

### Operator Tests (`operator-tests/e2e/`)

- `operator/vsphere_mixed_node.go` - VSphereMixedNodeEnv feature gate tests
- `common/helper.go` - Client configuration (`NewClientConfigForTest`)

### Test Prefixes

- `[cloud-provider-aws-e2e-openshift]` - OpenShift-specific tests

### Feature Gates Tested

- `AWSServiceLBNetworkSecurityGroup` - Managed security groups for NLBs
- `VSphereMixedNodeEnv` - Platform-type node labels on vSphere

## Development

### Adding New Tests

1. Add test files to the appropriate sub-project's `e2e/` directory:
   - `aws-tests/e2e/aws/` - AWS-specific tests
   - `aws-tests/e2e/common/` - AWS-specific shared helpers
   - `operator-tests/e2e/operator/` - Operator tests
   - `operator-tests/e2e/common/` - Operator-specific shared helpers

2. Import the test package in the binary's `main.go` (blank import for side effects):
   ```go
   import _ "github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/aws-tests/e2e/aws"
   ```

3. Tests are automatically discovered by the OTE framework via Ginkgo suite scanning

**Note:** The `e2e/common/` directories are independent in each sub-project and contain only the helper functions needed by that specific binary.

### Building Binaries

```bash
# From project root - build both binaries
make cloud-controller-manager-aws-tests-ext cluster-cloud-controller-manager-operator-tests-ext

# Or build all test binaries
make build

# Or build individually from within each sub-project
cd openshift-tests/aws-tests && go build -o ../bin/cloud-controller-manager-aws-tests-ext .
cd openshift-tests/operator-tests && go build -o ../bin/cluster-cloud-controller-manager-operator-tests-ext .
```

Binaries are built to `openshift-tests/bin/`:
- `cloud-controller-manager-aws-tests-ext` (~95MB)
- `cluster-cloud-controller-manager-operator-tests-ext` (~85MB)

### Running Tests

The test binaries are OpenShift Tests Extension (OTE) binaries and follow the OTE command structure:

```bash
# List available test suites
./openshift-tests/bin/cloud-controller-manager-aws-tests-ext list

# Run a specific suite
./openshift-tests/bin/cloud-controller-manager-aws-tests-ext run ccm/aws/conformance/parallel

# List operator tests
./openshift-tests/bin/cluster-cloud-controller-manager-operator-tests-ext list

# Run operator tests (parallel or serial)
./openshift-tests/bin/cluster-cloud-controller-manager-operator-tests-ext run ccm/operator/conformance/parallel
./openshift-tests/bin/cluster-cloud-controller-manager-operator-tests-ext run ccm/operator/conformance/serial
```

**Prerequisites:**
- `KUBECONFIG` environment variable must be set
- For AWS tests: AWS credentials configured (via `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` or instance profile)
- For AWS tests: `AWS_REGION` or `LEASED_RESOURCE` environment variable set

### Updating Dependencies

Each sub-project has its own `go.mod` file, allowing independent dependency management:

```bash
# From project root - update all modules in the workspace
./hack/go-mod.sh

# Or update a specific sub-project independently
cd openshift-tests/aws-tests && go mod tidy
cd openshift-tests/operator-tests && go mod tidy
```

The `hack/go-mod.sh` script (run from project root):
- Tidies all modules in the workspace (root, aws-tests, operator-tests)
- Syncs workspace dependencies via `go work sync`
- Re-tidies after sync to apply version bumps
- Verifies all modules
- Creates unified vendor directory

**Benefits of separate modules:**
- AWS SDK updates only affect `aws-tests/` - operator tests remain stable
- Each binary can update dependencies independently without blocking the other
- Faster, more focused dependency updates
- Reduced dependency bloat (operator tests don't pull in AWS SDK)

## Architecture

### Go Workspace

This directory contains multiple Go modules organized as a workspace:
- Root module: `github.com/openshift/cluster-cloud-controller-manager-operator`
- AWS tests: `github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/aws-tests`
- Operator tests: `github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/operator-tests`

All modules are coordinated via the workspace defined in `../go.work`.

### OTE Framework

Uses OpenShift Tests Extension framework:
- Test discovery via Ginkgo suite scanning
- Extension registration and suite management
- Platform and topology filtering
- Kubernetes E2E framework integration

## References

- [OpenShift Tests Extension](https://github.com/openshift-eng/openshift-tests-extension)
- [Kubernetes E2E Framework](https://github.com/kubernetes/kubernetes/tree/master/test/e2e/framework)
- [Cloud Provider AWS Tests](https://github.com/kubernetes/cloud-provider-aws/tree/master/tests/e2e)
