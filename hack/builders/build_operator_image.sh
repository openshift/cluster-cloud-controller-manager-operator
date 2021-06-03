#!/bin/bash

set -e

help() {
    echo "Build an operator image with custom changes to support external cloud providers"
    echo ""
    echo "Usage: ./build_operator_image.sh [options]"
    echo "Options:"
    echo "-h, --help        show this message"
    echo "-o, --operator    operator name to build, examples: machine-config-operator, cluster-kube-controller-manager-operator"
    echo "-i, --id          id of your pull request to apply on top of the master branch"
    echo "-u, --username    registered username in quay.io"
    echo "-t, --tag         push to a custom tag in your origin release image repo, default: latest"
    echo "-d, --dockerfile  non-default Dockerfile name, default: Dockerfile"
    echo ""
}

TAG="latest"
DOCKERFILE="Dockerfile"

# Parse Options
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            help
            exit 0
            ;;

        -u|--username)
            USERNAME=$2
            shift 2
            ;;

        -t|--tag)
            TAG=$2
            shift 2
            ;;

        -o|--operator)
            OPERATOR_NAME=$2
            shift 2
            ;;

        -i|--id)
            PRID=$2
            shift 2
            ;;

        -d|--dockerfile)
            DOCKERFILE=$2
            shift 2
            ;;

        *)
            echo "Invalid option $1"
            help
            exit 0
            ;;
    esac
done

if [ -z "$USERNAME" ]; then
    echo "No quay.io username provided, exiting ..."
    exit 1
fi

if [ -z "$OPERATOR_NAME" ]; then
    echo "No operator name provided, exiting ..."
    exit 1
fi

OPERATOR_IMAGE=quay.io/$USERNAME/$OPERATOR_NAME:$TAG
GITHUB_REPO="https://github.com/openshift/$OPERATOR_NAME"

git ls-remote $GITHUB_REPO 1>/dev/null

echo "Cloning repo $GITHUB_REPO"
rm -rf $OPERATOR_NAME
git clone $GITHUB_REPO

pushd $OPERATOR_NAME

echo "Applying your changes"
git fetch origin pull/$PRID/head:changes
git checkout changes
git rebase master

echo "Setting operator image to $OPERATOR_IMAGE"

echo "Start building operator image"
podman build --no-cache -t $OPERATOR_IMAGE -f $DOCKERFILE

echo "Pushing operator image to quay.io"
podman push $OPERATOR_IMAGE

popd

echo "Successfully pushed $OPERATOR_IMAGE"

echo "Cleaning up"
rm -rf $OPERATOR_NAME
