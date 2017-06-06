#!/bin/bash
# Copyright 2015 The Kubernetes Authors.
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

# Run e2e tests using environment variables exported in e2e.sh.

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

export PS4='+(${BASH_SOURCE}:${LINENO}): ${FUNCNAME[0]:+${FUNCNAME[0]}(): }'

# Have cmd/e2e run by goe2e.sh generate JUnit report in ${WORKSPACE}/junit*.xml
ARTIFACTS=${WORKSPACE}/_artifacts
mkdir -p ${ARTIFACTS}

: ${KUBE_GCS_RELEASE_BUCKET:="kubernetes-release"}
: ${KUBE_GCS_DEV_RELEASE_BUCKET:="kubernetes-release-dev"}

# Explicitly set config path so staging gcloud (if installed) uses same path
export CLOUDSDK_CONFIG="${WORKSPACE}/.config/gcloud"

echo "--------------------------------------------------------------------------------"
echo "Test Environment:"
printenv | sort
echo "--------------------------------------------------------------------------------"

# When run inside Docker, we need to make sure all files are world-readable
# (since they will be owned by root on the host).
trap "chmod -R o+r '${ARTIFACTS}'" EXIT SIGINT SIGTERM
export E2E_REPORT_DIR=${ARTIFACTS}

e2e_go_args=( \
  -v \
  --dump="${ARTIFACTS}" \
)

# TODO(fejta): delete all of these soon, as RAW_EXTRACT is the only supported
# option. Meaning call to e2e-runner.sh needs an --extract flag
if [[ -n "${RAW_EXTRACT:-}" ]]; then
  echo 'RAW_EXTRACT is set, --extract set by $@'
  echo 'Note that RAW_EXTRACT is no longer required, feel free to remove'
elif [[ "${JENKINS_USE_EXISTING_BINARIES:-}" =~ ^[yY]$ ]]; then
  echo 'ERROR: JENKINS_USE_EXISTING_BINARIES no longer supported'
  echo 'Send --extract=none to scenarios/kubernetes_e2e.py'
  exit 1
elif [[ "${JENKINS_USE_LOCAL_BINARIES:-}" =~ ^[yY]$ ]]; then
  echo 'ERROR: JENKINS_USE_LOCAL_BINARIES no longer supported.'
  echo 'Send --extract=local to scenarios/kubernetes_e2e.py'
  exit 1
elif [[ "${JENKINS_USE_SERVER_VERSION:-}" =~ ^[yY]$ ]]; then
  echo 'ERROR: JENKINS_USE_SERVER_VERSION no longer supported.'
  echo 'Send --extract=gke to scenarios/kubernetes_e2e.py'
  exit 1
elif [[ "${JENKINS_USE_GCI_VERSION:-}" =~ ^[yY]$ ]]; then
  echo 'ERROR: JENKINS_USE_GCI_VERSION no longer supported'
  echo 'Send --extract=gci/FAMILY to scenarios/kubernetes_e2e.py'
  exit 1
elif [[ -n "${JENKINS_PUBLISHED_VERSION:-}" ]]; then
  echo 'ERROR: JENKINS_PUBLISHED_VERSION no longer supported'
  echo 'Send --extract=ci/latest or appropriate kubetest value to scenarios/kubernetes_e2e.py'
  exit 1
else
  echo 'RAW_EXTRACT is unset, which is probably fine.'
  echo 'Ensure kubetest gets an --extract flag (via scenarios/kubernetes_e2e.py)'
fi

if [[ "${FAIL_ON_GCP_RESOURCE_LEAK:-true}" == "true" ]]; then
  case "${KUBERNETES_PROVIDER}" in
    gce|gke)
      e2e_go_args+=(--check-leaked-resources)
      ;;
  esac
fi

if [[ "${E2E_TEST:-}" == "true" ]]; then
  e2e_go_args+=(--test)
  if [[ "${SKEW_KUBECTL:-}" == 'y' ]]; then
      GINKGO_TEST_ARGS="${GINKGO_TEST_ARGS:-} --kubectl-path=$(pwd)/kubernetes_skew/cluster/kubectl.sh"
  fi
  if [[ -n "${GINKGO_TEST_ARGS:-}" ]]; then
    e2e_go_args+=(--test_args="${GINKGO_TEST_ARGS}")
  fi
fi

# Optionally run upgrade tests before other tests.
if [[ "${E2E_UPGRADE_TEST:-}" == "true" ]]; then
  e2e_go_args+=(--upgrade_args="${GINKGO_UPGRADE_TEST_ARGS}")
fi

kubetest ${E2E_OPT:-} "${e2e_go_args[@]}" "${@}"
