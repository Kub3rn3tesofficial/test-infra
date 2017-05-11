#!/bin/bash
# Copyright 2016 The Kubernetes Authors.
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

set -o errexit
set -o nounset
set -o pipefail

# Cache location.
export TEST_TMPDIR="/root/.cache/bazel"

bazel clean --expunge

make bazel-build && rc=$? || rc=$?

if [[ "${rc}" == 0 ]]; then
  make bazel-release && rc=$? || rc=$?
fi

if [[ "${rc}" == 0 ]]; then
  version=$(cat bazel-genfiles/version || true)
  if [[ -z "${version}" ]]; then
    echo "Kubernetes version missing; not uploading ci artifacts."
    rc=1
  else
    push_build="../release/push-build.sh"
    if [[ -x "${push_build}" ]]; then
      "${push_build}" --bucket=kubernetes-release-dev --nomock --verbose --ci \
        --gcs-suffix=-bazel && rc=$? || rc=$?
    else
      echo "release repository missing; using Bazel gcs upload rule directly"
      bazel run //:ci-artifacts -- "gs://kubernetes-release-dev/bazel/${version}" && rc=$? || rc=$?
    fi
  fi
fi

# Coalesce test results into one file for upload.
"$(dirname "${0}")/../images/pull_kubernetes_bazel/coalesce.py"

exit "${rc}"
