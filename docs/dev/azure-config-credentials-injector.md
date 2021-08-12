# Azure credentials injector

## Motivation

There are various auth mechanisms for cloud-controller-manager(CCM) and cloud-node-manager(CNM) within azure.

Namely: 
  * [Using](https://github.com/openshift/cloud-provider-azure/blob/master/pkg/auth/azure_auth.go#L95) the [managed identity extension](https://docs.microsoft.com/en-us/azure/active-directory/managed-identities-azure-resources/overview) 
    * this is what openshift leveraging for public Azure installations at the moment (4.8)
  * Store part of cloud config in a secret, configure CCM accordingly (point it to appropriate secret key)
    * Secret should contain json object with represents a part of [cloud-config](https://kubernetes-sigs.github.io/cloud-provider-azure/install/configs/) 
    * It would be merged with `cloud-config` during CCM startup procedure
    * CNM does not support such merge behaviour at the moment
  * Pass credentials via cloud-config directly as corresponding parameters
  
Due to existing secret format described in [OCP documentation](https://docs.openshift.com/container-platform/4.8/installing/installing_azure/manually-creating-iam-azure.html#admin-credentials-root-secret-formats_manually-creating-iam-azure) which is not directly applicable for CCM/CNM,
`azure-config-credentials-injector` was introduced.

## azure-config-credentials-injector 

Due to no support of [managed identity extension](https://docs.microsoft.com/en-us/azure/active-directory/managed-identities-azure-resources/overview) on Azure StackHub we need to pass credentials to CCM/CNM somehow.
There are couple of issues with this process at the moment:
* Secret with credentials created by [cloud-credential-operator](https://github.com/openshift/cloud-credential-operator) can not be used directly due to different format
* CNM does not support merging behaviour (part of cloud config in a `secret` resource in other words)


As temporary solution of necessity to pass credentials to CCM/CNM on Azure StackHub platform separate binary within CCCMO operator image was introduced.
This tool intented to run as an [initContainer](https://kubernetes.io/docs/concepts/workloads/pods/init-containers/)
 right before CCM/CNM in a same pod and simply takes credentials values from environment variables, then inject it into `cloud-config` for azure CCM and CNM. `cloud-config` passing performed via shared `emptyDir` volume.

### Notes and links

* `azure-config-credentials-injector` source code placed in separate module within `cmd` folder in this repository
*  intended to be deployed as `initContainer` within CCM/CNM pods. Init container MUST be called `azure-inject-credentials` for successfull image substitution by operator.
*  Secret which uses as source for credentials expected to be created as a response to `CredentialsRequest` during [manual process](https://docs.openshift.com/container-platform/4.8/installing/installing_azure/manually-creating-iam-azure.html#admin-credentials-root-secret-formats_manually-creating-iam-azure), or by CCO/ccoctl tool.
*  `CredentialRequest`s manifests stored in `manifests` folder in this repository and should be handled manually during cluster installation or by cloud-credentials-operator.

### Future plans
* Add support for merge behaviour to CNM
* Define possibility to create a secret with needed format (json file with part of [cloud-config](https://kubernetes-sigs.github.io/cloud-provider-azure/install/configs/)) by CCO/ccoctl tool.