#!/bin/bash
# Copyright 2017 The Kubernetes Authors.
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

# Usage: ./flakes.sh | tee flakes-$(date +%Y-%m-%d).json
# This script uses flakes.sql to find job flake data for the past week
# The script then filters jobs down to those which flake more than 4x/day
# And also notes any test in those jobs which flake more than 1x/day

out="/tmp/flakes-$(date +%Y-%m-%d).json"
if [[ ! -f "${out}" ]]; then
  which bq >/dev/null || (echo 'Cannot find bq on path. Install gcloud' 1>&2 && exit 1)
  echo "Flakes results will be available at: ${out}" 1>&2
  cat "$(dirname "${0}")/flakes.sql" | bq query --format=prettyjson > "${out}"
fi
which jq >/dev/null || (echo 'Cannot find jq on path. Install jq' 1>&2 && exit 1)
echo 'Jobs flaking more than 4x/day:' 1>&2
cat "${out}" | jq '
  [(.[] | select(.flakes|tonumber > 28) | {(.job): {
      consistency: (.commit_consistency|tonumber),
      flakes: (.flakes|tonumber),
      flakiest: ([(.flakiest[] | select(.flakes|tonumber >= 7) | {
        (.name): (.flakes|tonumber)}) ])| add
  }})] | add'
echo "Full flake data saved to: ${out}" 1>&2
