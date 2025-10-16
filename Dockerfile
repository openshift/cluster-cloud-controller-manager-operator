FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.24-openshift-4.21 AS builder
WORKDIR /go/src/github.com/openshift/cluster-cloud-controller-manager-operator
COPY . .
RUN make build

FROM registry.ci.openshift.org/ocp/4.21:base-rhel9
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/bin/cluster-controller-manager-operator .
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/bin/config-sync-controllers .
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/bin/azure-config-credentials-injector .
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/manifests manifests

LABEL io.openshift.release.operator true
