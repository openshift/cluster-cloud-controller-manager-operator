# Managing trusted ca for CCMs

## Intro

On some, mainly on-prem platforms, such as OpenStack, vSphere, or Azure Stack, 
privately or self-signed SSL certificates might be used for its endpoints.

In order to be able to communicate with the underlying platform, CA certificates should be
propagated into CCMs pods trust store.

Openshift documentation [prescribes](https://docs.openshift.com/container-platform/4.8/networking/configuring-a-custom-pki.html)
to use cluster-wide **_Proxy_** object as a common way to configure trusted CA for an entire cluster.

All necessary logic around `trustedCA` management already exists in Openshift platform, but can not be leveraged by CCM/CCCMO
in its current state due to CCCMO's role in the cluster bootstrap process.
All necessary steps are performing by [cluster-network-operator](https://github.com/openshift/cluster-network-operator/), only once control plane nodes have been initialized by the CCM.
Such node initialization might require communication with the underlying platform (OpenStack, vSphere, ASH),
which would not be successful without configured trust to platform endpoint certificates.

For solving this 'chicken-and-egg' problem with a minimum amount of risk, a separate `trusted_ca_bundle_controller` was introduced.

## Implementation Description

Implementation mostly replicates logic from `cluster-network-operator`.

The controller has been [introduced](https://github.com/openshift/cluster-cloud-controller-manager-operator/pull/136) as a part of `config-sync-controllers` binary in CCCMO pod and lives as separate control loop along with [cloud-config-sync](cloud-config-sync.md) controller. 

The controller performs sync and merges CA from user defined ConfigMap
(located in `openshift-config` and referenced by cluster scoped Proxy resource) with system bundle.
Merged CA bundle will be written to `ccm-trusted-ca` ConfigMap in `openshift-cloud-controller-manager` namespace and intended to be mounted in all CCM pods.

Top-level overview:
- In case when Proxy resource contains the `trustedCA` parameter in its spec, user's CA will be taken from a config map with a name specified by `trustedCA` parameter.
- In case if Proxy resource does not contain the `trustedCA` parameter, only the system bundle from the CCCMO pod will be used.
- In case if user defined CA is invalid (PEM can not be parsed, ConfigMap format is unexpected) only the system bundle from the CCCMO pod will be used

# Links
- [cluster-network-operator implementation](https://github.com/openshift/cluster-network-operator/blob/master/pkg/controller/proxyconfig/controller.go#L91)
- [related openshift documentation](https://docs.openshift.com/container-platform/4.8/networking/configuring-a-custom-pki.html)