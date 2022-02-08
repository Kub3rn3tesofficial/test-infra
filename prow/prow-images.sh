#!/usr/bin/env bash
# Copyright 2022 The Kubernetes Authors.
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

set -o nounset
set -o errexit
set -o pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd $REPO_ROOT
source hack/build/setup-go.sh

readonly DEFAULT_ARCH="linux/amd64"
readonly PROW_IMAGES_DEF_FILE="prow/.prow-images"
GIT_TAG="$(date +v%Y%m%d)-$(git describe --always --dirty)"
TAG_SET=(
  "latest"
  "latest-root"
  "${GIT_TAG}"
)
ALL_ARCHES=("arm64" "s390x" "ppc64le")

# push or local tar?
PUSH="${PUSH:-false}"

# takes comma separated list of arch
# returns space separated list of tags basesd on arch
tags-arg() {
  local tags=(${TAG_SET[@]})
  local arches="$1"
  if [[ "${arches}" == "all" ]]; then
    for arch in "${ALL_ARCHES[@]}"; do
      tags+=("${arch}")
      for base in "${TAG_SET[@]}"; do
        tags+=("${arch}-${base}")
      done
    done
  fi

  for tag in "${tags[@]}"; do
    echo "--tags=${tag}"
  done
}

IMAGES=()
while IFS= read -r image; do
  IMAGES+=($image)
done < "$PROW_IMAGES_DEF_FILE"

# overridable registry to use
KO_DOCKER_REPO="${KO_DOCKER_REPO:-}"
if [[ -z "${KO_DOCKER_REPO}" ]]; then
  echo "KO_DOCKER_REPO must be provided"
  exit 1
fi
export KO_DOCKER_REPO

# build ko
cd 'hack/tools'
go build -o "${REPO_ROOT}/_bin/ko" github.com/google/ko
cd "${REPO_ROOT}"

echo "Images: ${IMAGES[@]}"

for image in "${IMAGES[@]}"; do
    echo "Building $image"
    parts=(${image//;/ })
    image_dir="${parts[0]}"
    arch="${DEFAULT_ARCH}"
    if [[ "${#parts[@]}" -gt 1 ]]; then
      arch="${parts[1]}"
    fi
    name="$(basename "${image_dir}")"
    # gather static files if there is any
    gather_static_file_script="${image_dir}/gather-static.sh"
    if [[ -f $gather_static_file_script ]]; then
      source $gather_static_file_script
    fi
    # push or local tarball
    publish_args=(--tarball=_bin/"${name}".tar --push=false)
    if [[ "${PUSH}" != 'false' ]]; then
        publish_args=(--push=true)
    fi
    # specify tag
    push_tags="$(tags-arg $arch)"
    publish_args+=(--base-import-paths ${push_tags} --platform="${arch}")
    # actually build
    failed=0
    (set -x; _bin/ko publish "${publish_args[@]}" ./"${image_dir}") || failed=1
    if [[ -f $gather_static_file_script ]]; then
      CLEAN=true $gather_static_file_script
    fi
    if (( failed )); then
      echo "Failed building image: ${image_dir}"
      exit 1
    fi
done
