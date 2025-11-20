#!/bin/bash

set -o errexit
set -o pipefail

REPO_ROOT=$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")

function runGoimports() {
  local GOIMPORTS_PATH=$REPO_ROOT/bin/goimports
  GOBIN="$REPO_ROOT"/bin go install -mod=readonly golang.org/x/tools/cmd/goimports@v0.24.0

  local LOCAL_PACKAGE="github.com/openshift/cluster-cloud-controller-manager-operator"
  local GOIMPORTS_ARGS=("-local $LOCAL_PACKAGE -w $REPO_ROOT/cmd $REPO_ROOT/pkg")

  if [[ $# -ne 0 ]] ; then
      GOIMPORTS_ARGS="$@"
  fi
  echo "Run goimports:"
  (set -x; $GOIMPORTS_PATH $GOIMPORTS_ARGS)
}

runGoimports "$@"
