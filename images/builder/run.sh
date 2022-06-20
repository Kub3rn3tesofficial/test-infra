#!/bin/sh
# Copyright 2019 The Kubernetes Authors.
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

# Used by gcr.io/k8s-staging-test-infra/image-builder.
# See ci-runner.sh for the version prow uses to build and run on the fly.

set -e

if [[ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]]; then
  echo "Activating service account..."
  gcloud auth activate-service-account --key-file="${GOOGLE_APPLICATION_CREDENTIALS}"
fi

# If IS_PRESUBMIT & IMAGE env is set, use yq to strip images key
# from cloudbuild.yaml file to avoid pushing images to GCR/AR.

if [[ -n "${IS_PRESUBMIT:-}" ]]; then
  yq 'del(.images)' -i images/${IMAGE}/cloudbuild.yaml --exit-status
fi

echo "Running..."
if [ -n "${ARTIFACTS}" ] && [ -z "${LOG_TO_STDOUT}" ]; then
  echo "\$ARTIFACTS is set, sending logs to ${ARTIFACTS}"
  /builder --log-dir="${ARTIFACTS}" "$@"
else
  /builder "$@"
fi
