# Cluster Cloud Controller Manager Operator (CCCMO) - Hacking guide

## How to run the component locally for testing

Prerequisites:

```bash
git clone github.com/openshift/cluster-cloud-controller-manager-operator
cd cluster-cloud-controller-manager-operator
```

Make sure your `$KUBECONFIG` is set properly, because it will be used to interact with your cluster.

First step is to scale down the cluster version operator.

> “At the heart of OpenShift is an operator called the Cluster Version Operator. This operator watches the deployments and images related to the core OpenShift services, and will prevent a user from changing these details. If I want to replace the core OpenShift services I will need to scale this operator down.”

```bash
oc scale --replicas=0 deployment/cluster-version-operator -n openshift-cluster-version
```

Second step is scaling down the CCCMO. The operator watches for deployed resources to be running and will prevent them from scaling down.

```bash
oc scale --replicas=0 deployment/cluster-cloud-controller-manager-operator -n openshift-cloud-controller-manager-operator
```

Finally, once all has been scaled down, you can compile and run the operator. If you need to use your custom images, you can copy and edit `hack/example-images.json` to substitute the images you want to deploy.

```bash
make build
./bin/cluster-controller-manager-operator --images-json=hack/example-images.json
```

## How to build the operator in a container for remote testing

Prerequisites:

```bash
git clone github.com/openshift/cluster-cloud-controller-manager-operator
cd cluster-cloud-controller-manager-operator
```

First, you’ll need to build an image with your changes.

```bash
export IMG="quay.io/your-repo/cluster-cloud-controller-manager-operator:<branch-name>"
make image
docker push $IMG
```

Second step, similar to when you run the component locally, we need to scale down the cluster version operator.

```bash
oc scale --replicas=0 deployment/cluster-version-operator -n openshift-cluster-version
```

Next, edit `cloud-controller-manager-images` ConfigMap in `openshift-cloud-controller-manager-operator` namespace and place the link to your modified CCM image there.

The `openshift-cloud-controller-manager-operator` namespace contains all the resources and components that are used by the CCCMO. There is a ConfigMap named `cloud-controller-manager-images`, which contains references to all the images used by the CCCMO. It uses these images to deploy the CCM for various platforms and ensure that the used images are from the correct release payload.

The ConfigMap looks something like this:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cloud-controller-manager-images
  namespace: openshift-cloud-controller-manager-operator
data:
  images.json: |
    {
      "cloudControllerManagerAWS": "quay.io/openshift/origin-aws-cloud-controller-manager",
      "cloudControllerManagerOpenStack": "quay.io/openshift/origin-openstack-cloud-controller-manager"
    }
```

You can edit the existing ConfigMap or create a copy with your custom images and apply it to the cluster.

```bash
oc edit configmap/cloud-controller-manager-images -n openshift-cloud-controller-manager-operator
```

With the new image information loaded into the ConfigMap, the next thing you might want to do is to replace the CCCMO image itself. This operator controls how the specific cloud controllers are deployed and coordinated. You only need to change this component if there is something you are testing.

The easiest way to change this operator is to change the image reference in the deployment. You can use the following command to edit image references or other things in the deployment:

```bash
oc edit deployment/cluster-cloud-controller-manager-operator -n openshift-cloud-controller-manager-operator
```

If you want to reconcile the images ConfigMap changes, you need to cause creation of new replicaSet for CCM, which will ensure the new content of the ConfigMap is mounted.

After changing the image reference in the images ConfigMap, an easy way to swap out the controller is to let the CCCMO do it for you. You can delete the deployment associated with the cloud provider CCM, and then the CCCMO will create a new one for you, like this:

```bash
oc delete deployment/aws-cloud-controller-manager -n openshift-cloud-controller-manager
```

## How to build a release image with custom CCCMO

At the current stage of development it is frequent that for testing a feature, another openshift operator change should be in place to make the feature work in CCCMO.

To achieve this you need to build custom release payload. To do that you should use `oc adm release new`.

Here is an example how to build a release with changes in CCCMO. Precondition for this step - you have to build the operator image and push to a public registry.

```bash
oc adm release new --server=https://api.ci.l2s4.p1.openshiftapps.com:6443 \ # CI server to pull existing release payload from
  --from-release=registry.ci.openshift.org/ocp/release:4.10.0-0.ci-<example> \ # Your release tag to pull the rest of the images from
  --registry-config=~/pull-secret \ # Your pull-secret with permissions to read from CI server
  --to-image=quay.io/<your-repo>/release:<your-branch> \ # Where the release image will be pushed
  cluster-cloud-controller-manager-operator=quay.io/<your-repo>/cluster-cloud-controller-manager-operator:<your-branch>
```

You can pick one of available CI releases from the list here: https://amd64.ocp.releases.ci.openshift.org/#4.10.0-0.ci.

CI builds are pruned regularly and as such, you will have to create a new release every few days with an updated base release. Follow the process above and run `oc adm release new` once again.

Tags for image overrides are coming from the operator manifests itself. The image tags could be found in `manifests/image-references`. Example: https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/master/manifests/image-references
```

## How to deploy a cluster with a custom release image and enabled CCM configuration from the start

To deploy a cluster with all changes built in the previous step, all you need to do is to specify the `OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE` env variable to the installer binary.

```bash
OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE=quay.io/<your-repo>/release:<your-branch> openshift-install create cluster
```

However, in order for the cluster to run the CCM from the start, you need to add an additional step to the installation.

By specifying the `CustomNoUpgrade` feature gate in the cluster with `ExternalCloudProvider` you are enabling CCCMO logic for providers, which are currently in Technical Preview state.

```bash
export OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE=quay.io/<your-repo>/release:<your-branch> 

# This step would stop after creation of ./manifests folder. All resources placed there will be created in the cluster
# during bootstrap and would override the default state of the resource in the cluster.
openshift-install create manifests

cat <<EOF > manifests/manifest_feature_gate.yaml
apiVersion: config.openshift.io/v1
kind: FeatureGate
metadata:
  annotations:
    include.release.openshift.io/self-managed-high-availability: "true"
    include.release.openshift.io/single-node-developer: "true"
    release.openshift.io/create-only: "true"
  name: cluster
spec:
  customNoUpgrade:
    enabled:
    - ExternalCloudProvider
    - CSIMigrationAWS
    - CSIMigrationOpenStack
  featureSet: CustomNoUpgrade
EOF

# Now you could create a cluster with your custom release image
openshift-install create cluster
```

If you additionally want to include some installer changes, you need to build an installer binary from https://github.com/openshift/installer

To do that:

```bash
git clone https://github.com/openshift/installer.git
cd installer
./hack/build.sh
# This would build openshift-install binary in ./bin/openshift-install

# Now you can use it in combination with release image override
OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE=quay.io/<your-repo>/release:<your-branch> ./bin/openshift-install create cluster
```
