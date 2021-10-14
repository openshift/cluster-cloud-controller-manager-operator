
# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:trivialVersions=true,preserveUnknownFields=false"

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
	hack/unit-tests.sh

# Build operator binaries
build: operator cloud-config-sync-controller azure-config-credentials-injector

operator:
	go build -o bin/cluster-controller-manager-operator cmd/cluster-cloud-controller-manager-operator/main.go

cloud-config-sync-controller:
	go build -o bin/cloud-config-sync-controller cmd/cloud-config-sync-controller/main.go

azure-config-credentials-injector:
	go build -o bin/azure-config-credentials-injector cmd/azure-config-credentials-injector/main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: verify manifests
	go run cmd/cluster-cloud-controller-manager-operator/main.go

# Generate manifests e.g. CRD, RBAC etc.
manifests: controller-gen
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Run go fmt against code
.PHONY: fmt
fmt:
	hack/goimports.sh

# Run go vet against code
.PHONY: vet
vet:
	go vet ./...

# Run golangci-lint against code
.PHONY: lint
lint: golangci-lint
	( GOLANGCI_LINT_CACHE=$(PROJECT_DIR)/.cache $(GOLANGCI_LINT) run --timeout 10m )

# Run go mod
.PHONY: vendor
vendor:
	go mod tidy
	go mod vendor
	go mod verify

# Generate code
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# Build the docker image
.PHONY: image
image:
	docker build -t ${IMG} .

# Push the docker image
.PHONY: push
push:
	docker push ${IMG}

# Download controller-gen locally if necessary
CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
controller-gen:
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.4.1)

# Download golangci-lint locally if necessary
GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
golangci-lint:
	$(call go-get-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint)

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go get $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef
