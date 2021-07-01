# Managing cloud-config for CCMs

# Intro

Some cloud providers,
such as [Azure](https://kubernetes-sigs.github.io/cloud-provider-azure/install/configs/) or [vSphere](https://cloud-provider-vsphere.sigs.k8s.io/cloud_config.html),
require a config file which contains various platform-specific parameters (e.g. api endpoints, resource group name or so).

In OpenShift terms this config is represented as ConfigMap defined during the installation procedure and managing by the [cluster-config-operator](https://github.com/openshift/cluster-config-operator).

There are two places where this config map is stored on a running cluster at the moment (OCP 4.8):
1. `kube-cloud-config` ConfigMap in `openshift-config-managed` namespace
2. ConfigMap with arbitrary name in `openshift-config` namespace. Such name might be taken from `cluster` Infrastructure resource spec.


This ConfigMap should be copied from one of the places described above and kept in sync within CCCMO managed namespace for further mounting onto cloud provider pods.
For such purposes a separate controller within CCCMO was introduced (see `controllers/cloud_config_sync_controller.go`).

# Implementation Description
Implementation is being inspired by `library-go`.

Brief description:

    1. In case of changing/deletion/creation one of the following resources
        - `kube-cloud-config` ConfigMap in `openshift-config-managed` namespace
        - `cloud-config` ConfigMap in CCCMO managed namespace
        - `cluster` Infrastructure resource
       
    2. Sync CCCMO's `cloud-config` content with `openshift-config-managed/kube-cloud-config`
    
    3. If `openshift-config-managed/kube-cloud-config` does not exists - fallback to sync with ConfigMap from `openshift-config` namespace
        - during sync procedure replace key in target ConfigMap to `cloud.conf` which is default one for OpenShift

# Links
- [library-go implementation](https://github.com/openshift/library-go/blob/master/pkg/operator/configobserver/cloudprovider/observe_cloudprovider.go#L82)
- [cluster-config-operator repository](https://github.com/openshift/cluster-config-operator)