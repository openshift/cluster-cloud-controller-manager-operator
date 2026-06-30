FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.26-openshift-5.0 AS builder
WORKDIR /go/src/github.com/openshift/cluster-cloud-controller-manager-operator
COPY . .
RUN make build &&\
    gzip /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/bin/cloud-controller-manager-aws-tests-ext &&\
    gzip /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/bin/cloud-controller-manager-operator-tests-ext

FROM registry.ci.openshift.org/ocp/5.0:base-rhel9
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/bin/cluster-controller-manager-operator .
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/bin/config-sync-controllers .
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/bin/azure-config-credentials-injector .
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/manifests manifests
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/bin/cloud-controller-manager-aws-tests-ext.gz /usr/bin/cloud-controller-manager-aws-tests-ext.gz
COPY --from=builder /go/src/github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/bin/cloud-controller-manager-operator-tests-ext.gz /usr/bin/cloud-controller-manager-operator-tests-ext.gz

LABEL io.openshift.release.operator true
