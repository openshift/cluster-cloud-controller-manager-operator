
# Image URL to use all building/pushing image targets
IMG ?= controller:latest

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.32.1

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
CONTROLLER_GEN = go run ${PROJECT_DIR}/vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
ENVTEST = go run ${PROJECT_DIR}/vendor/sigs.k8s.io/controller-runtime/tools/setup-envtest
GOLANGCI_LINT = go run ${PROJECT_DIR}/vendor/github.com/golangci/golangci-lint/cmd/golangci-lint

HOME ?= /tmp/kubebuilder-testing
ifeq ($(HOME), /)
HOME = /tmp/kubebuilder-testing
endif

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

verify-%:
	make $*
	./hack/verify-diff.sh

all: build

verify: fmt vet lint

# Run tests
test: generate verify manifests unit

unit:
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path --bin-dir $(PROJECT_DIR)/bin --index https://raw.githubusercontent.com/openshift/api/master/envtest-releases.yaml)" ./hack/ci-test.sh

# Build operator binaries
build: operator config-sync-controllers azure-config-credentials-injector

operator:
	go build -o bin/cluster-controller-manager-operator cmd/cluster-cloud-controller-manager-operator/main.go

config-sync-controllers:
	go build -o bin/config-sync-controllers cmd/config-sync-controllers/main.go

azure-config-credentials-injector:
	go build -o bin/azure-config-credentials-injector cmd/azure-config-credentials-injector/main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: verify manifests
	go run cmd/cluster-cloud-controller-manager-operator/main.go

# Generate manifests e.g. CRD, RBAC etc.
manifests:
	$(CONTROLLER_GEN) crd rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Run go fmt against code
.PHONY: fmt
fmt:
	go fmt ./...

# Run go vet against code
.PHONY: vet
vet:
	go vet ./...

# Run golangci-lint against code
.PHONY: lint
lint:
	( GOLANGCI_LINT_CACHE=$(PROJECT_DIR)/.cache $(GOLANGCI_LINT) run --timeout 10m -v)

# Run go mod
.PHONY: vendor
vendor:
	go mod tidy
	go mod vendor
	go mod verify

# Generate code
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# Build the docker image
.PHONY: image
image:
	docker build -t ${IMG} .

# Push the docker image
.PHONY: push
push:
	docker push ${IMG}

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
