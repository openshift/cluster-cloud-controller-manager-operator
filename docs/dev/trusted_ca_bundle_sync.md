# Managing trusted ca for CCMs

## Intro

On some, mainly on-prem platforms, such as OpenStack, vSphere, or Azure Stack, privately or self-signed SSL certificates might be used for its endpoints.

In order to be able to communicate with the underlying platform, CA certificates should be propagated into CCMs pods trust store.

OpenShift documentation [prescribes](https://docs.openshift.com/container-platform/4.8/networking/configuring-a-custom-pki.html) to use cluster-wide **_Proxy_** object as a common way to configure trusted CA for an entire cluster.

All necessary logic around `trustedCA` management already exists in Openshift platform, but can not be leveraged by CCM/CCCMO in its current state due to CCCMO's role in the cluster bootstrap process. All necessary steps are performing by [cluster-network-operator](https://github.com/openshift/cluster-network-operator/), only once control plane nodes have been initialized by the CCM. Such node initialization might require communication with the underlying platform (OpenStack, vSphere, ASH), which would not be successful without configured trust to platform endpoint certificates.

For solving this 'chicken-and-egg' problem with a minimum amount of risk, a separate `trusted_ca_bundle_controller` was introduced.

Moreover, [historically](https://github.com/openshift/installer/pull/5251) `additionalTrustBundle` from installer-config does not always end up in the **_Proxy_** object, instead this CA goes to cloud-config for future consuming by cloud provider.

## Implementation Description

Implementation mostly replicates logic from `cluster-network-operator`.

The controller has been [introduced](https://github.com/openshift/cluster-cloud-controller-manager-operator/pull/136) as a part of `config-sync-controllers` binary in the CCCMO pod and lives as a separate control loop along with the [cloud-config-sync](cloud-config-sync.md) controller. 

The controller performs sync and merges CA from user defined ConfigMap (located in `openshift-config` and referenced by the cluster scoped Proxy resource) and `ca-bundle.pem` key of [synced cloud-config configmap](cloud-config-sync.md) with the system bundle.
Merged CA bundle will be written to `ccm-trusted-ca` ConfigMap in `openshift-cloud-controller-manager` namespace and intended to be mounted in all CCM pods.

Top-level overview:
- In case when Proxy resource contains the `trustedCA` parameter in its spec, user's CA will be taken from a ConfigMap with a name specified by `trustedCA` parameter.
- In case if `ca-bundle.pem` key is presented in `cloud-config` ConfigMap within CCMs namespace, it would be added to merged CA as well.
- In case if Proxy resource does not contain the `trustedCA` parameter, CA bundle from `cloud-config` pod will be used along with system one.
- In case if user defined CAs is invalid (PEM can not be parsed, ConfigMap format is unexpected) or not presented only the system bundle from the CCCMO pod will be used

# Links
- [cluster-network-operator implementation](https://github.com/openshift/cluster-network-operator/blob/master/pkg/controller/proxyconfig/controller.go#L91)
- [related openshift documentation](https://docs.openshift.com/container-platform/4.8/networking/configuring-a-custom-pki.html)
- [installer part with cloud-config shaping](https://github.com/openshift/installer/blob/master/pkg/asset/manifests/cloudproviderconfig.go#L99)