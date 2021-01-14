FROM registry.svc.ci.openshift.org/openshift/release:golang-1.15 AS builder
WORKDIR /go/src/github.com/openshift/cluster-cloud-controller-manager-operator
COPY . .
RUN make build
