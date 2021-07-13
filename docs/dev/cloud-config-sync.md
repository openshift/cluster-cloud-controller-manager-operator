# Managing cloud-config for CCMs

## Intro

Some cloud providers, such as [Azure](https://kubernetes-sigs.github.io/cloud-provider-azure/install/configs/) or [vSphere](https://cloud-provider-vsphere.sigs.k8s.io/cloud_config.html), require a config file, which contains various platform-specific parameters (e.g. api endpoints, resource group name, and so on).

In OpenShift this config is stored in a ConfigMap defined during the installation procedure and managed by the [cluster-config-operator](https://github.com/openshift/cluster-config-operator).

There are two places where this ConfigMap is stored on a running cluster at the moment (OCP 4.9):
1. `kube-cloud-config` ConfigMap in `openshift-config-managed` namespace.
2. ConfigMap with an arbitrary name in `openshift-config` namespace. Such name might be taken from the `cluster` Infrastructure resource spec.

This ConfigMap should be copied from one of the places described above and kept in sync within the CCCMO managed namespace for further mounting onto cloud provider pods. For such purposes `cloud-config-sync-controller` has been introduced as a [separate binary](https://github.com/openshift/cluster-cloud-controller-manager-operator/pull/86) in CCCMO pod.

## Implementation Description

Implementation is being inspired by [library-go](https://github.com/openshift/library-go).

The controller performs a sync of the CCM's `cloud-config` content with `openshift-config-managed/kube-cloud-config` in case of changing/deletion/creation one of the following resources:
   - `kube-cloud-config` ConfigMap in `openshift-config-managed` namespace;
   - `cloud-config` ConfigMap in the CCCMO managed namespace;
   - `cluster` Infrastructure resource.

If `openshift-config-managed/kube-cloud-config` does not exists - the controller fallbacks to sync with the ConfigMap from `openshift-config` namespace. Also during the sync procedure it replaces key in the target ConfigMap to `cloud.conf`, which is default one for OpenShift.

## Links
- [library-go implementation](https://github.com/openshift/library-go/blob/master/pkg/operator/configobserver/cloudprovider/observe_cloudprovider.go#L82)
- [cluster-config-operator repository](https://github.com/openshift/cluster-config-operator)
