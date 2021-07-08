# Integrating a new cloud provider in Cluster-cloud-controller-manager-operator (CCCMO)

## Overview

This document is describing the changes required within the OpenShift project, to add a new Cloud Controller Manager, specifically how to integrate with CCCMO. It describes in detail the changes needed in CCCMO, then follows with other OpenShift repositories on the surface, on which CCCMO is dependent. The content will describe how you should provide your CCM manifests to the operator, the requirements to follow, and how to structure your provider manifests and code.

Each chapter describes its own step in the cloud provider addition, and the order is important in some of them. Notably [Cloud-provider fork on openshift side](#cloud-provider-fork-on-openshift-side) should be handled before [Add provider image tags to image-references](#add-provider-image-tags-to-image-references).

## Adding cloud provider manifests

You can take AWS as an [example](https://github.com/openshift/cluster-cloud-controller-manager-operator/tree/master/pkg/cloud/aws). It contains manifests for post-install or the operator phase.

All the operator does here is apply the resources, which you can see in the example. So you shouldn't dynamically create new manifests and put any new additions to that list in runtime. Place your manifests there and that is going to be all you need. After that the next part is going to be to create an image reference in the manifest folder. The operator will handle the rest for you, assuming the manifests are correctly structured.

We use a golang 1.16 feature - [embed](https://golang.org/pkg/embed/), to read manifests from the binary.

Cloud provider manifests are located under `pkg/cloud/<your-cloud-provider>`

Typically there is a folder `assets` for your cloud provider manifests.

Add these references in cloud selection [switch](https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/master/pkg/cloud/cloud.go) logic and the operator will be ready to use them. 

## Operator provisioned CCM manifests

Here is an example of Deployment manifest for AWS:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
 labels:
   k8s-app: aws-cloud-controller-manager
 name: aws-cloud-controller-manager
 namespace: openshift-cloud-controller-manager
spec:
 # You have to specify more than 1 replica for achieving HA in the cluster
 replicas: 2 
 selector:
   matchLabels:
     k8s-app: aws-cloud-controller-manager
 template:
   metadata:
     annotations:
       target.workload.openshift.io/management: '{"effect": "PreferredDuringScheduling"}'
     labels:
       k8s-app: aws-cloud-controller-manager
   spec:
     priorityClassName: system-cluster-critical
     containers:
     - args:
       - --cloud-provider=aws
       - --use-service-account-credentials=true
       - -v=2
       image: gcr.io/k8s-staging-provider-aws/cloud-controller-manager:v1.19.0-alpha.1
       imagePullPolicy: IfNotPresent
       # Container is required to specify name in order for image substitution
       # to take place. Cloud-controller-manager is substituted with
 # real CCM image from the cluster
       name: cloud-controller-manager
       # You are required to set pod requests, but not limits to satisfy CI 
       # requirements for OpenShift on every scheduled workload in the cluster
       resources:
         requests:
           cpu: 200m
           memory: 50Mi
     hostNetwork: true
     nodeSelector:
       node-role.kubernetes.io/master: ""
     # We run CCM in Deployments, so in order to achieve replica scheduling on
     # different Nodes you need to set affinity settings
     affinity:
       podAntiAffinity:
         requiredDuringSchedulingIgnoredDuringExecution:
         - topologyKey: "kubernetes.io/hostname"
           labelSelector:
             matchLabels:
               k8s-app: aws-cloud-controller-manager
     # All CCMs are currently using cloud-controller-manager ServiceAccount
     # with permissions copying in-tree counterparts.
     serviceAccountName: cloud-controller-manager
     tolerations:
     # CCM pod is one of the core components of the cluster, so it
     # has to tolerate the default set of taints which might occur
     # on a Node at some moment of cluster installation
     - effect: NoSchedule
       key: node-role.kubernetes.io/master
       operator: Exists
     - effect: NoExecute
       key: node.kubernetes.io/unreachable
       operator: Exists
       tolerationSeconds: 120
     # This taint is strictly required in order for CCM to be scheduled
     # on the master Node at the moment when the cluster
     # lacks CCM replicas to untaint the Node
     - effect: NoSchedule
       key: node.cloudprovider.kubernetes.io/uninitialized
       value: "true"
```

Our operator is responsible for synchronization of `cloud-config` ConfigMap from `openshift-config` and `openshift-config-managed` namespace to the namespace where the CCM resources are provisioned.  The ConfigMap is named `cloud-conf `and could be mounted into a CCM pod for later use if your cloud provider requires it.

Credentials secret serving to `openshift-cloud-controller-manager` namespace is carried by [https://github.com/openshift/cloud-credential-operator](https://github.com/openshift/cloud-credential-operator) for us. You need to implement your cloud provider support there, and add a `CredentialsRequest` resource in `manifests` directory. 

## Cloud-provider fork on OpenShift side

You are required to create your cloud-provider fork under OpenShift organization. This fork will be responsible for building and resolving your provider images, as well as following OpenShift release branching cadence.That repository has to be added into CI system and will run post submit and periodic jobs with e2e tests on your cloud-provider.

You should request repository addition to [openshift/release](https://github.com/openshift/release). Here is a [PR](https://github.com/openshift/release/pull/17199) for AWS [openshift/cloud-provider-aws](https://github.com/openshift/cloud-provider-aws) repository, establishing initial set of CI jobs, including `images` job responsible for building and tagging `registry.ci.openshift.org/openshift:aws-cloud-controller-manager`

Once an image is added to the CI system, it will be present in CI builds but not in release builds. You should follow ART guidelines and create tickets to add your cloud-provider repository into release. See [https://docs.ci.openshift.org/docs/how-tos/onboarding-a-new-component/#product-builds-and-becoming-part-of-an-openshift-release-image](https://docs.ci.openshift.org/docs/how-tos/onboarding-a-new-component/#product-builds-and-becoming-part-of-an-openshift-release-image). For this part you need to request openshift engineering help to set this up.

It is very important to get this step done before the next one, an addition to image-references could only be fulfilled only after the ART request has been resolved, or it would break nightly releases. We currently track relevant requests in this [doc](https://docs.google.com/document/d/1XB_-I9wTBwsGw-a7rwiCmOlnxTVnUdbw5odkyLKwp1k/edit?usp=sharing).

## Add provider image tags to image-references

You should add your cloud provider image reference tag to the list. The list is in the [image-references](https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/master/manifests/image-references) file. This tag addition will complete the image addition to the CI build system. Make sure the tag is resolvable in CI, and is referencing your Docker build file. Here is an AWS [example](https://github.com/openshift/release/blob/master/ci-operator/config/openshift/cloud-provider-aws/openshift-cloud-provider-aws-master.yaml#L18-L24).

```yaml
- name: aws-cloud-controller-manager
  from:
   kind: DockerImage
   name: quay.io/openshift/origin-aws-cloud-controller-manager
```

You will have to extend the config [images](https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/master/manifests/0000_26_cloud-controller-manager-operator_01_images.configmap.yaml) list with image name:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
 name: cloud-controller-manager-images
 namespace: openshift-cloud-controller-manager-operator
data:
 images.json: >
   {
     "cloudControllerManagerAWS": "quay.io/openshift/origin-aws-cloud-controller-manager",
   … # Other images 
   }
```

Make ensure the `imageReferences` [contains](https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/c161640ef47e232df5c9a4c9298ba95551fa48d9/pkg/config/config.go#L18-L23) your image, and is later used in substitution. 

## Manifests representation

We use a couple of substitutions in manifests you place for the files in the `assets` folder.

1. Each namespaced manifest you place there is substituted with namespace where operator is pointing at (default is `openshift-cloud-controller-manager`)
2. Each schedulable workload manifest (`Pod, Deployment, DaemonSet`) containing container definition is looking for container names matching:
    1. `cloud-controller-manager` - image in manifest is substituted by correlated cloud controller manager image collected from [config images](https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/master/manifests/0000_26_cloud-controller-manager-operator_01_images.configmap.yaml) for provider.
    2. `cloud-node-manager` -  image in manifest is substituted by correlated cloud node manager image collected from [config images](https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/master/manifests/0000_26_cloud-controller-manager-operator_01_images.configmap.yaml) for provider.

Add substitution logic to the substitution [package](https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/252a13d96bd22be3c2d28ab7256750ae85a7a451/pkg/substitution/substitution.go) after that.

Your cloud provider implementation should only expose one method:

* `GetResources() []client.Object`: This should return a list of unmarshalled objects which are required to run CCM. CCCMO will provision those in a running cluster. Objects should be returned as copies, to ensure immutability.

## Required external repository changes

### API

[https://github.com/openshift/api](https://github.com/openshift/api) - Initial API change across openshift required to support your platform. Example of a field addition for a new cloud provider might be [https://github.com/openshift/api/pull/860](https://github.com/openshift/api/pull/860). A change in this repository requires revendor in all other repositories which one way or another interacts with your changes.

### Installer

[https://github.com/openshift/installer](https://github.com/openshift/installer) - Here you would be required to add terraform scripts to provision initial cluster infrastructure from install-config. This is also the place where your initial `Infrastructure` cluster object is created.

### Cluster-config-operator

[https://github.com/openshift/cluster-config-operator](https://github.com/openshift/cluster-config-operator) - This operator exists in both bootstrap and post-install phase in the cluster, and is responsible for populating the `cloud-config` file for your cloud provider, in case your provider needs it. This is the place where `Infrastructure.Status.PlatformStatus` is populated with platform dependent data.

### Library-go

Particularly the [cloudprovider](https://github.com/openshift/library-go/blob/master/pkg/cloudprovider/external.go) package, where your `Infrastructure.Status.PlatformStatus.Type `is evaluated as external and therefore allows correct set of `--cloud-provider=external` flags on legacy k/k binaries - kubelet, KCM and KAPI. Change in this repository requires revendoring in:

* [https://github.com/openshift/machine-config-operator](https://github.com/openshift/machine-config-operator) for kubelet
* [https://github.com/openshift/cluster-kube-controller-manager-operator](https://github.com/openshift/cluster-kube-controller-manager-operator) for KCM
* [https://github.com/openshift/cluster-kube-apiserver-operator](https://github.com/openshift/cluster-kube-apiserver-operator) for KAPI
* [https://github.com/openshift/cluster-cloud-controller-manager-operator](https://github.com/openshift/cluster-cloud-controller-manager-operator) for CCM itself.

### Machine-config-operator

[https://github.com/openshift/machine-config-operator](https://github.com/openshift/machine-config-operator) - for customizing ignition files generated by the installer, and adding configuration changes to machine, configuring kubelet, etc.

### Cloud-credentials-manager

[https://github.com/openshift/cloud-credential-operator](https://github.com/openshift/cloud-credential-operator) - is needed in case your cloud provider requires credentials stored as a secret to access cloud services. You could refer to [docs](https://github.com/openshift/cloud-credential-operator/blob/master/docs/adding-new-cloud-provider.md) for the cloud provider addition procedure.

### Machine-api-operator

[https://github.com/openshift/machine-api-operator](https://github.com/openshift/machine-api-operator) - All Node objects in OpenShift are managed by Machines backed up by MachineSets. Cluster could not operate until all created machines get into `Ready` state. Detailed overview and cloud provider addition could be found in the [docs](https://github.com/openshift/machine-api-operator/blob/master/docs/user/machine-api-operator-overview.md).
