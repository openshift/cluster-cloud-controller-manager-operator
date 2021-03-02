#!/bin/bash

# A tool to rebuild and update kube-controller-manager-operator and
# kube-apiserver-operator in a running cluster against changes in a local
# library-go repo.
#
# Usage: update-library-go.sh <library-go path> <operator source directory>
#
# <library-go path> is a local directory library-go
#
# <operator source directory> is a local directory where operators will be
# checked out if they don't exist already. The directory will be created if it
# doesn't exist.

set -e -o pipefail

# Ensure the cluster registry's default route is enabled, and return the route
function get_image_registry {
    local url

    oc patch configs.imageregistry.operator.openshift.io/cluster --type=merge \
        --patch '{"spec":{"defaultRoute":true}}' >/dev/null

    while ! url=$(oc get -n openshift-image-registry route/default-route -o json | jq -re .spec.host); do
        echo Waiting for default route
        sleep 1
    done

    echo $url
}

# Login to the cluster's internal registry using a token
function registry_login {
    local url=$1
    local token=$2

    podman login --tls-verify=false -u kubeadmin -p "${token}" $url
}

# Rebuild the operator image, push it to the cluster's internal registry,
# unmanage the operator in CVO, and update the operator deployment to use the
# new image.
function update_operator {
    local name=$1

    local namespace="openshift-${name}"
    local image="${namespace}/${name}"

    # This dance is so we get build output during execution while also
    # capturing it
    mkfifo /tmp/buildah.$$
    buildah bud -t "${image}" Dockerfile.rhel7 | tee /tmp/buildah.$$ &
    imageid=$(tail -n -1 /tmp/buildah.$$)
    rm /tmp/buildah.$$

    # FIXME(mdbooth): I intended to push to a devel tag, then reference the image by
    # digest, but I couldn't make it work: the pod can't pull the image. Here
    # I'm just using image id as a unique tag. We need something which changes
    # on every invocation to ensure that the deployment pokes its pod, and so we
    # can verify that we're running the image we expect.
    podman push --tls-verify=false "${image}" "${registry}"/"${image}:${imageid}"

    ${scriptdir}/cvo-unmanage.py "${namespace}" "${name}"

    oc -n "${namespace}" patch "deploy/${name}" --type=json --patch '
    [{
        "op": "replace",
        "path": "/spec/template/spec/containers/0/image",
        "value": "image-registry.openshift-image-registry.svc:5000/'${image}':'${imageid}'"
    }]'
}

function usage {
    echo "Usage: $0 [-l] [-m] <source dir>"
    echo "    -l : Rebuild operators using library-go"
    echo "    -m : Rebuild MCO"
    echo "    <source dir> : a directory containing git repos"
}

while getopts ":lmh" opt; do
    case ${opt} in
        l ) librarygo=1 ;;
        m ) mco=1 ;;
        h )
            usage
            exit 0
            ;;
        \? )
            echo "Invalid option: $OPTARG" 1>&2
            usage 1>&2
            exit 1
            ;;
    esac
done
shift $((OPTIND - 1))

sourcedir=$1
scriptdir=$(dirname $0)

if [ -z "$librarygo" -a -z "$mco" -o -z "$sourcedir" ]; then
    usage 1>&2 exit 1
fi

set -x

# Canonicalize directory paths
sourcedir=$(readlink -m $sourcedir)
scriptdir=$(readlink -e $scriptdir)

while ! token=$(oc whoami -t); do
    # This is interactive! Not executed if we're already logged in.
    oc login -u kubeadmin
done

registry=$(get_image_registry)
registry_login $registry $token

if [ $librarygo == 1 ]; then
    librarygodir="${sourcedir}/library-go"

    if [ ! -d "${librarygodir}" ]; then
        echo "$librarygodir not found" 1>&2
        exit 1
    fi

    for operator in kube-controller-manager-operator kube-apiserver-operator; do
        repo="cluster-${operator}"
        repodir="${sourcedir}/${repo}"
        [ ! -d "${repodir}" ] && \
            git clone "https://github.com/openshift/${repo}.git" "${repodir}"
        pushd "${repodir}"
            go mod edit --replace "github.com/openshift/library-go=$librarygodir"
            go mod vendor

            update_operator $operator
        popd
    done
fi

if [ $mco == 1 ]; then
    name=machine-config-operator
    mcodir="${sourcedir}/${name}"

    if [ ! -d "${mcodir}" ]; then
        echo "$mcodir not found" 1>&2
        exit 1
    fi

    pushd "${mcodir}"
        update_operator $name
    popd
fi
