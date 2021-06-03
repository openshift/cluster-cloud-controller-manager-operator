# CCM hacking tools

## Prerequisites

It's critical to correctly configure the credentials here or the building process will fail with permission errors.

### Accessing registry.ci.openshift.org

For registry.ci.openshift.org, you first need to copy the token from https://oauth-openshift.apps.ci.l2s4.p1.openshiftapps.com/oauth/token/request (top left, "Display Token") and run this command:

```sh
podman login -u <username> -p <token> https://registry.ci.openshift.org
```

### Obtaining OpenShift's quay.io credentials

For OpenShift's quay.io, the credentials need to be taken from https://cloud.redhat.com/openshift/create/local. Click `Download pull secret` and store `pull-secret.txt` in a safe location on your computer.

**NOTE:** These are not your personal credentials, you won't be able to use them to access your personal quay.io account. These credentials are required to obtain the base release image only.

## Build an operator image with your custom changes

```txt
Usage: ./build_operator_image.sh [options]
Options:
-h, --help        show this message
-o, --operator    operator name to build, examples: machine-config-operator, cluster-kube-controller-manager-operator
-i, --id          id of your pull request to apply on top of the master branch
-u, --username    registered username in quay.io
-t, --tag         push to a custom tag in your origin release image repo, default: latest
-d, --dockerfile  non-default Dockerfile name, default: Dockerfile
```

For instance, if you want to build a Machine Config Operator image with your custom change specified by PR [\#2606](https://github.com/openshift/machine-config-operator/pull/2606) and then push it into your personal quay.io, execute

```sh
$ ./build_operator_image.sh --username johndow --operator machine-config-operator --id 2606
```

**Note**: since Quay doesn't publish images by default, to successfully use the image, you need to make it public manually in the registry web console.

## Build a release image with your custom changes

```txt
Usage: ./build_release_image.sh [options] -u <quay.io username>
Options:
-h, --help      show this message
-u, --username  registered username in quay.io
-t, --tag       push to a custom tag in your origin release image repo, default: latest
-r, --release   openshift release version, default: 4.9
-a, --auth      path of registry auth file, default: ./pull-secret.txt
--cccmo         custom cluster-cloud-controller-manager-operator image name, default: quay.io/openshift/origin-cluster-cloud-controller-manager-operator:4.9
--aws-ccm       custom aws cloud-controller-manager image name, default: quay.io/openshift/origin-aws-cloud-controller-manager:4.9
--azure-ccm     custom azure cloud-controller-manager image name, default: quay.io/openshift/origin-azure-cloud-controller-manager:4.9
--azure-node    custom azure node manager image name, default: quay.io/openshift/origin-azure-cloud-node-manager:4.9
--openstack-ccm custom openstack cloud-controller-manager image name, default: quay.io/openshift/origin-openstack-cloud-controller-manager:4.9
--kapio         custom kube-apiserver-operator image name, default: current kube-apiserver-operator image from the release payload
--kcmo          custom kube-controller-manager-operator image name, default: current kube-controller-manager-operator image from the release payload
--mco           custom machine-config-operator image name, default: current machine-config-operator image from the release payload
```

To build an actual release image with your custom Machine Config Operator image, that was created in the previous step, execute

```sh
$ ./build_release_image.sh --username johndow --auth ~/pull-secret.txt --mco quay.io/johndow/machine-config-operator:latest
```

To replace other component images use the related command line options (`--kcmo` for Kube Controller Manager Operator, `--aws-ccm` for AWS Cloud Controller manager, and so on). It is possible to replace several images at once.

When the building process is over, the script will upload the release image in your personal quay account, so it can be consumed by the installer.
