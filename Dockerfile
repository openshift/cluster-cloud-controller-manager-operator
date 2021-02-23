FROM registry.ci.openshift.org/openshift/release:golang-1.16 AS builder
WORKDIR /go/src/github.com/openshift/cluster-cloud-controller-manager-operator
COPY . .
RUN make build

FROM registry.ci.openshift.org/openshift/origin-v4.0:base
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/bin/cluster-controller-manager-operator .
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/manifests manifests
