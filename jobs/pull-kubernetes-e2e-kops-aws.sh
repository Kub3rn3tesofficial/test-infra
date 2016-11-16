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
set -o xtrace

readonly testinfra="$(dirname "${0}")/.."

# TODO(fejta): remove this
case "${ghprbTargetBranch:-}" in
  "release-1.0"|"release-1.1"|"release-1.2"|"release-1.3")
    echo "PR AWS kops job disabled for legacy branches."
    exit
    ;;
esac

export KUBE_GCS_RELEASE_BUCKET="${KUBE_GCS_RELEASE_BUCKET:-kubernetes-release-pull}"
export KUBE_GCS_RELEASE_SUFFIX="/${JOB_NAME}"
export KUBE_GCS_UPDATE_LATEST=n
export JENKINS_USE_LOCAL_BINARIES=y
export KUBE_FASTBUILD=true

if [[ -z "${SKIP_BUILD:-}" ]]; then
  ./hack/jenkins/build.sh
fi

# TODO(spxtr): once we no longer have any PRs against release-1.4,
# remove the first branch.
if [[ -e build/util.sh ]]; then
  readonly version=$(source build/util.sh && echo $(kube::release::semantic_version))
elif [[ -e build-tools/util.sh ]]; then
  readonly version=$(source build-tools/util.sh && echo $(kube::release::semantic_version))
fi
if [[ -z "{version:-}" ]]; then
  echo "Could not find build/util.sh or build-tools/util.sh, or kube::release::semantic_version failed." >&2
  exit 1
fi
export KUBERNETES_PROVIDER="kops-aws"

export KOPS_STATE_STORE="${KOPS_STATE_STORE:-s3://k8s-kops-jenkins/}"
export KOPS_CLUSTER_DOMAIN="${KOPS_CLUSTER_DOMAIN:-test-aws.k8s.io}"
export E2E_NAME="aws-kops-${NODE_NAME}-${EXECUTOR_NUMBER:-0}"
export E2E_OPT="${E2E_OPT:-}\
  --kops-cluster ${E2E_NAME}.${KOPS_CLUSTER_DOMAIN}\
  --kops-kubernetes-version https://storage.googleapis.com/${KUBE_GCS_RELEASE_BUCKET}/ci${KUBE_GCS_RELEASE_SUFFIX}/${version}\
  --kops-nodes 4\
  --kops-state ${KOPS_STATE_STORE}"
export E2E_MIN_STARTUP_PODS="1"

export AWS_CONFIG_FILE="/workspace/.aws/credentials"
export AWS_SHARED_CREDENTIALS_FILE="/workspace/.aws/credentials"
export KUBE_SSH_USER=admin
export LOG_DUMP_USE_KUBECTL=yes
export LOG_DUMP_SSH_KEY=/workspace/.ssh/kube_aws_rsa
export LOG_DUMP_SSH_USER=admin
export LOG_DUMP_SAVE_LOGS=cloud-init-output

# Flake detection. Individual tests get a second chance to pass.
export GINKGO_TOLERATE_FLAKES="y"
export GINKGO_PARALLEL="y"
# This list should match the list in kubernetes-e2e-kops-aws.
export GINKGO_TEST_ARGS='--ginkgo.skip=\[Slow\]|\[Serial\]|\[Disruptive\]|\[Flaky\]|\[Feature:.+\]|\[HPA\]|NodeProblemDetector|Dashboard|Services.*functioning.*NodePort'
# GINKGO_PARALLEL_NODES should match kubernetes-e2e-kops-aws.
export GINKGO_PARALLEL_NODES="30"

# Assume we're upping, testing, and downing a cluster
export E2E_UP="true"
export E2E_TEST="true"
export E2E_DOWN="true"

# Skip gcloud update checking
export CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK=true

# Get golang into our PATH so we can run e2e.go
export PATH=${PATH}:/usr/local/go/bin

export KUBE_E2E_RUNNER="/workspace/kops-e2e-runner.sh"
timeout -k 15m 55m "${testinfra}/jenkins/dockerized-e2e-runner.sh" && rc=$? || rc=$?
if [[ ${rc} -ne 0 ]]; then
  if [[ -x cluster/log-dump.sh && -d _artifacts ]]; then
    echo "Dumping logs for any remaining nodes"
    ./cluster/log-dump.sh _artifacts
  fi
fi
if [[ ${rc} -eq 124 || ${rc} -eq 137 ]]; then
  echo "Build timed out" >&2
elif [[ ${rc} -ne 0 ]]; then
  echo "Build failed" >&2
fi
echo "Exiting with code: ${rc}"
exit ${rc}
