#!/bin/bash

# Copyright 2024 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Provision envtest binaries (etcd, kube-apiserver, kubectl) for `make unit`
# WITHOUT the legacy setup-envtest GCS bucket listing, which now returns HTTP 401
# for unauthenticated clients and breaks the unit tests.
#
# This downloads the envtest asset bundle for the requested Kubernetes minor
# version from the kubernetes-sigs/controller-tools GitHub releases (resolved via
# the envtest-releases.yaml index), verifies its sha512 checksum, extracts it
# into the given bin dir, and prints ONLY the resulting bin dir on stdout so the
# Makefile can set KUBEBUILDER_ASSETS to it. All diagnostics go to stderr.

set -o errexit
set -o nounset
set -o pipefail

K8S_VERSION="${1:?usage: setup-envtest-assets.sh <k8s-minor-version> <bin-dir>}"
BIN_DIR="${2:?usage: setup-envtest-assets.sh <k8s-minor-version> <bin-dir>}"

GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"
INDEX_URL="https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/HEAD/envtest-releases.yaml"

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

# Fetch the release index once.
INDEX="${TMP}/envtest-releases.yaml"
curl -fsSL --retry 3 -o "${INDEX}" "${INDEX_URL}"

# Resolve the newest patch release for the requested minor version.
VERSION="$(grep -oE "v${K8S_VERSION}\.[0-9]+" "${INDEX}" | sort -V | tail -n1)"
if [ -z "${VERSION}" ]; then
  echo "ERROR: no envtest release found for k8s ${K8S_VERSION} in ${INDEX_URL}" >&2
  exit 1
fi

DEST="${BIN_DIR}/k8s/${VERSION#v}-${GOOS}-${GOARCH}"
APISERVER="${DEST}/kube-apiserver"

# Cache: reuse already-extracted assets.
if [ -x "${APISERVER}" ]; then
  echo "${DEST}"
  exit 0
fi

ASSET="envtest-${VERSION}-${GOOS}-${GOARCH}.tar.gz"
URL="https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-${VERSION}/${ASSET}"

# Extract the expected sha512 for this asset from the index.
EXPECTED_SHA="$(awk -v asset="${ASSET}" '
  $0 ~ "^    " asset ":$" { found = 1; next }
  found && /^      hash:/ { gsub(/[ \t]/, "", $2); print $2; exit }
  found && /^    [^ ]/    { exit }
' "${INDEX}")"
if [ -z "${EXPECTED_SHA}" ]; then
  echo "ERROR: no sha512 hash found for ${ASSET} in ${INDEX_URL}" >&2
  exit 1
fi

echo "Downloading envtest assets: ${URL}" >&2
curl -fsSL --retry 3 -o "${TMP}/${ASSET}" "${URL}"

echo "Verifying sha512 checksum for ${ASSET}" >&2
echo "${EXPECTED_SHA}  ${TMP}/${ASSET}" | sha512sum --check --status || {
  echo "ERROR: sha512 mismatch for ${ASSET} (expected ${EXPECTED_SHA})" >&2
  exit 1
}

# Tarball layout: controller-tools/envtest/{etcd,kube-apiserver,kubectl}
tar -xzf "${TMP}/${ASSET}" -C "${TMP}"
mkdir -p "${DEST}"
cp "${TMP}/controller-tools/envtest/"* "${DEST}/"

if [ ! -x "${APISERVER}" ]; then
  echo "ERROR: kube-apiserver not found after extraction in ${DEST}" >&2
  exit 1
fi

echo "${DEST}"
