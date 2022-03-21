#! /bin/bash -e
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

# The presubmit jobs for the different Kubernetes-CSI repos are all
# the same except for the repo name. As Prow has no way of specifying
# the same job for multiple repos and manually copy-and-paste would be
# tedious, this script is used instead to generate them.

base="$(dirname $0)"

# All the Kubernetes versions we're testing. The patch version is
# irrelevant because the prow.sh script will pick a suitable KinD
# image or build from source.
k8s_versions="
1.21
1.22
1.23
"

# All the deployment versions we're testing.
deployment_versions="
1.21
1.22
1.23
"

# The experimental version for which jobs are optional.
experimental_k8s_version="1.23"

# The latest stable Kubernetes version for testing alpha jobs
latest_stable_k8s_version="1.22" # TODO: bump to 1.23 after testing a pull job

# Tag of the hostpath driver we should use for sidecar pull jobs
hostpath_driver_version="v1.7.2"

# We need this image because it has Docker in Docker and go.
dind_image="gcr.io/k8s-staging-test-infra/kubekins-e2e:v20220321-2d18391df1-master"

# All kubernetes-csi repos which are part of the hostpath driver example.
# For these repos we generate the full test matrix. For each entry here
# we need a "sig-storage-<repo>" dashboard in
# config/testgrids/kubernetes/sig-storage/config.yaml.
hostpath_example_repos="
csi-driver-host-path
external-attacher
external-provisioner
external-resizer
external-snapshotter
livenessprobe
node-driver-registrar
"

# All kubernetes-csi repos for which want to define pull tests for
# the csi-release-tools repo. Ideally, this list should represent
# different ways of using csi-release-tools (for example, single image
# vs. multiple images per repo). csi-sanity tests are used by csi-driver-host-path.
csi_release_tools_repos="
csi-test
external-provisioner
external-snapshotter
csi-driver-host-path
"

# kubernetes-csi repos which only need to be tested against at most a
# single Kubernetes version. We generate unit, stable and alpha jobs
# for these, without specifying a Kubernetes version. What the repo
# then tests in those jobs is entirely up to the repo.
#
# This list is currently empty, but such a job might be useful again
# in the future, so the code generator code below is kept.
single_kubernetes_repos="
"

# kubernetes-csi repos which only need unit testing.
unit_testing_repos="
external-health-monitor
csi-test
csi-release-tools
csi-lib-utils
csi-driver-iscsi
csi-driver-nvmf
csi-driver-nfs
csi-proxy
lib-volume-populator
volume-data-source-validator
"

# No Prow support in them yet.
# csi-driver-image-populator
# csi-lib-fc
# csi-lib-iscsi

# All branches that do *not* support Prow testing. All new branches
# are expected to have that support, therefore these list should be
# fixed. By excluding old branches we can avoid Prow config
# changes each time a new branch gets created.
skip_branches_cluster_driver_registrar='^(release-1.0)$'
skip_branches_csi_lib_utils='^(release-0.1|release-0.2)$'
skip_branches_csi_test='^(release-0.3|release-1.0|v0.1.0|v0.2.0)$'
skip_branches_external_attacher='^(release-0.2.0|release-0.3.0|release-0.4|release-1.0|v0.1.0)$'
skip_branches_external_provisioner='^(release-0.2.0|release-0.3.0|release-0.4|release-1.0|v0.1.0)$'
skip_branches_external_snapshotter='^(k8s_1.12.0-beta.1|release-0.4|release-1.0)$'
skip_branches_livenessprobe='^(release-0.4|release-1.0)$'
skip_branches_node_driver_registrar='^(release-1.0)$'

skip_branches () {
    eval echo \\\"\$skip_branches_$(echo $1 | tr - _)\\\" | grep -v '""'
}

find "$base" -name '*.yaml' -exec grep -q 'generated by gen-jobs.sh' '{}' \; -delete

# Resource usage of a job depends on whether it needs to build Kubernetes or not.
resources_for_kubernetes () {
    local kubernetes="$1"

    # "master" is always built from source. The experimental version might be built from
    # source, depending on what is in the prow.sh script. Here we play it safe and
    # request more resource when testing the experimental version, just in case.
    case $kubernetes in master|$experimental_k8s_version|release-*) cat <<EOF
      resources:
        requests:
          # these are both a bit below peak usage during build
          # this is mostly for building kubernetes
          memory: "9000Mi"
          # during the tests more like 3-20m is used
          cpu: 2000m
EOF
                            ;;
                            *) cat <<EOF
        resources:
          requests:
            cpu: 2000m
EOF
                            ;;
    esac
}

# Combines deployment and Kubernetes version in a job suffix like "1-14-on-kubernetes-1-13".
kubernetes_job_name () {
    local deployment="$1"
    local kubernetes="$2"
    echo "$(echo "$deployment-on-kubernetes-$kubernetes" | tr . -)"
}

# Combines type ("ci" or "pull"), repo, test type ("unit", "alpha", "non-alpha") and deployment+kubernetes into a
# Prow job name of the format <type>-kubernetes-csi[-<repo>][-<test type>][-<kubernetes job name].
# The <test type> part is only added for "unit" and "non-alpha" because there is no good name for it ("stable"?!)
# and to keep the job name a bit shorter.
job_name () {
    local type="$1"
    local repo="$2"
    local tests="$3"
    local deployment="$4"
    local kubernetes="$5"
    local name

    name="$type-kubernetes-csi"
    if [ "$repo" ]; then
        name+="-$repo"
    fi
    name+=$(test_name "$tests" "$deployment" "$kubernetes")
    echo "$name"
}

# Generates the testgrid annotations. "ci" jobs all land in the same
# "sig-storage-csi-ci" and send alert emails, "pull" jobs land in "sig-storage-csi-<repo>"
# and don't alert. Some repos only have a single pull job. Those
# land in "sig-storage-csi-other".
annotations () {
    local indent="$1"
    shift
    local type="$1"
    local repo="$2"
    local tests="$3"
    local deployment="$4"
    local kubernetes="$5"
    local description

    echo "annotations:"
    case "$type" in
        ci)
            echo "${indent}testgrid-dashboards: sig-storage-csi-ci"
            local alpha_testgrid_prefix="$(if [ "$tests" = "alpha" ]; then echo alpha-; fi)"
            echo "${indent}testgrid-tab-name: ${alpha_testgrid_prefix}${deployment}-on-${kubernetes}"
            echo "${indent}testgrid-alert-email: kubernetes-sig-storage-test-failures@googlegroups.com"
            description="periodic Kubernetes-CSI job"
            ;;
        pull)
            local testgrid
            local name=$(test_name "$tests" "$deployment" "$kubernetes" | sed -e 's/^-//')
            if [ "$name" ]; then
                testgrid="sig-storage-csi-$repo"
            else
                testgrid="sig-storage-csi-other"
                name=$(job_name "$@")
            fi
            echo "${indent}testgrid-dashboards: $testgrid"
            echo "${indent}testgrid-tab-name: $name"
            description="Kubernetes-CSI pull job"
            ;;
    esac

    if [ "$repo" ]; then
        description+=" in repo $repo"
    fi
    if [ "$tests" ]; then
        description+=" for $tests tests"
    fi
    if [ "$deployment" ] || [ "$kubernetes" ]; then
        description+=", using deployment $deployment on Kubernetes $kubernetes"
    fi
    echo "${indent}description: $description"
}

# Common suffix for job names which contains informatiopn about the test and cluster.
# Empty or starts with a hyphen.
test_name() {
    local tests="$1"
    local deployment="$2"
    local kubernetes="$3"
    local name

    if [ "$tests" ] && [ "$tests" != "non-alpha" ]; then
        name+="-$tests"
    fi
    if [ "$deployment" ] || [ "$kubernetes" ]; then
        name+="-$(kubernetes_job_name "$deployment" "$kubernetes")"
    fi
    echo "$name"
}

# "alpha" and "non-alpha" need to be expanded to different CSI_PROW_TESTS names.
expand_tests () {
    case "$1" in
        non-alpha)
            echo "sanity serial parallel";;
        alpha)
            echo "serial-alpha parallel-alpha";;
        *)
            echo "$1";;
    esac
}

# "alpha" features can be breaking across releases and
# therefore cannot be a required job
pull_optional() {
    local tests="$1"
    local kubernetes="$2"
    local deployment_suffix="$3"

    # https://github.com/kubernetes-csi/csi-driver-host-path/pull/282 has not been merged yet,
    # therefore pull jobs which depend on the new deployment flavors have to be optional.
    # TODO: remove this check once merged.
    if [ "$tests" == "alpha" ] || [ "$deployment_suffix" ] ; then
        echo "true"
    elif [ "$kubernetes" == "$experimental_k8s_version" ]; then
        # New k8s versions may require updates to kind or release-tools.
        # Make tests optional until everything is updated.
        echo "true"
    else
        echo "false"
    fi
}

pull_alwaysrun() {
    if [ "$1" != "alpha" ]; then
        echo "true"
    else
        echo "false"
    fi
}

# version_gt returns true if arg1 is greater than arg2.
#
# This function expects versions to be one of the following formats:
#   X.Y.Z, release-X.Y.Z, vX.Y.Z
#
#   where X,Y, and Z are any number.
#
# Partial versions (1.2, release-1.2) work as well as long as both
# arguments use the same format.
#
# The follow substrings are stripped before version comparison:
#   - "v"
#   - "release-"
#
# Usage:
# version_gt release-1.3 v1.2.0  (returns true)
# version_gt v1.1.1 v1.2.0  (returns false)
# version_gt 1.1.1 v1.2.0  (returns false)
# version_gt 1.3.1 v1.2.0  (returns true)
# version_gt 1.1.1 release-1.2.0  (returns false)
# version_gt 1.2.0 1.2.2  (returns false)
function version_gt() {
    versions=$(for ver in "$@"; do ver=${ver#release-}; ver=${ver#kubernetes-}; echo ${ver#v}; done)
    greaterVersion=${1#"release-"};
    greaterVersion=${greaterVersion#"kubernetes-"};
    greaterVersion=${greaterVersion#"v"};
    test "$(printf '%s' "$versions" | sort -V | head -n 1)" != "$greaterVersion"
}




snapshotter_version() {
    local kubernetes="$1"
    local canary="$2"

    if [ "$kubernetes" = "latest" ] || [ "$canary" = "canary" ]; then
        # Kubernetes master and canary images may need a more recent
        # snapshot controller and/or CRD than the ones from the latest
        # stable release.
        echo '"master"'
    else
        # All other jobs test against the latest supported, stable snapshotter
        # release for that Kubernetes release.
        #
        # Additional jobs could be created to cover version
        # skew, if desired.
        if [ "$kubernetes" == "latest" ] || [ "$kubernetes" == "master" ] || version_gt "$kubernetes" 1.19; then
            echo '"v4.0.0"'
        else
            echo '"v3.0.3"'
        fi
    fi
}

use_bazel() (
    local kubernetes="$1"

    # Strip z from x.y.z, version_gt does not handle it when comparing against 1.20.
    kubernetes="$(echo "$kubernetes" | sed -e 's/^\([0-9]*\)\.\([0-9]*\)\.[0-9]*$/\1.\2/')"

    # Kubernetes 1.21 removed support for building with Bazel.
    if version_gt "$kubernetes" "1.20"; then
        echo "false"
    else
        echo "true"
    fi
)

additional_deployment_suffices () (
    local repo="$1"

    case "$repo" in
        csi-driver-host-path) echo "-test";;
    esac
)

for repo in $hostpath_example_repos; do
    mkdir -p "$base/$repo"
    cat >"$base/$repo/$repo-config.yaml" <<EOF
# generated by gen-jobs.sh, do not edit manually

presubmits:
  kubernetes-csi/$repo:
EOF

    for deployment_suffix in "" $(additional_deployment_suffices "$repo"); do
        for tests in non-alpha alpha; do
            for deployment in $deployment_versions; do
                for kubernetes in $k8s_versions; do
                    # We could generate these pre-submit jobs for all combinations, but to save resources in the Prow
                    # cluster we only do it for those cases where the deployment matches the Kubernetes version.
                    # Once we have more than two supported Kubernetes releases we should limit this to the most
                    # recent two.
                    #
                    # Periodic jobs need to test the full matrix.
                    if [ "$kubernetes" = "$deployment" ]; then
                        # Alpha jobs only run on the latest version
                        if [ "$tests" != "alpha" ] || [ "$kubernetes" = "$latest_stable_k8s_version" ]; then
                            # These required jobs test the binary built from the PR against
                            # older, stable hostpath driver deployments and Kubernetes versions
                            cat >>"$base/$repo/$repo-config.yaml" <<EOF
  - name: $(job_name "pull" "$repo" "$tests" "$deployment$deployment_suffix" "$kubernetes")
    always_run: $(pull_alwaysrun "$tests")
    optional: $(pull_optional "$tests" "$kubernetes" "$deployment_suffix")
    decorate: true
    skip_report: false
    skip_branches: [$(skip_branches $repo)]
    labels:
      preset-service-account: "true"
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
    $(annotations "      " "pull" "$repo" "$tests" "$deployment$deployment_suffix" "$kubernetes")
    spec:
      containers:
      # We need this image because it has Docker in Docker and go.
      - image: ${dind_image}
        command:
        - runner.sh
        args:
        - ./.prow.sh
        env:
        # We pick some version for which there are pre-built images for kind.
        # Update only when the newer version is known to not cause issues,
        # otherwise presubmit jobs may start to fail for reasons that are
        # unrelated to the PR. Testing against the latest Kubernetes is covered
        # by periodic jobs (see https://k8s-testgrid.appspot.com/sig-storage-csi-ci#Summary).
        - name: CSI_PROW_KUBERNETES_VERSION
          value: "$kubernetes.0"
        - name: CSI_PROW_USE_BAZEL
          value: "$(use_bazel "$kubernetes")"
        - name: CSI_PROW_KUBERNETES_DEPLOYMENT
          value: "$deployment"
        - name: CSI_PROW_DEPLOYMENT_SUFFIX
          value: "$deployment_suffix"
        - name: CSI_PROW_DRIVER_VERSION
          value: "$hostpath_driver_version"
        - name: CSI_SNAPSHOTTER_VERSION
          value: $(snapshotter_version "$kubernetes" "")
        - name: CSI_PROW_TESTS
          value: "$(expand_tests "$tests")"
        # docker-in-docker needs privileged mode
        securityContext:
          privileged: true
$(resources_for_kubernetes "$kubernetes")
EOF
                        fi
                    fi
                done # end kubernetes


                # These optional jobs test the binary built from the PR against
                # older, stable hostpath driver deployments and Kubernetes master
                if [ "$tests" != "alpha" ] || [ "$deployment" = "$latest_stable_k8s" ]; then
                    cat >>"$base/$repo/$repo-config.yaml" <<EOF
  - name: $(job_name "pull" "$repo" "$tests" "$deployment$deployment_suffix" master)
    # Explicitly needs to be started with /test.
    # This cannot be enabled by default because there's always the risk
    # that something changes in master which breaks the pre-merge check.
    always_run: false
    optional: true
    decorate: true
    skip_report: false
    labels:
      preset-service-account: "true"
      preset-dind-enabled: "true"
      preset-bazel-remote-cache-enabled: "true"
      preset-kind-volume-mounts: "true"
    $(annotations "      " "pull" "$repo" "$tests" "$deployment$deployment_suffix" master)
    spec:
      containers:
      # We need this image because it has Docker in Docker and go.
      - image: ${dind_image}
        command:
        - runner.sh
        args:
        - ./.prow.sh
        env:
        - name: CSI_PROW_KUBERNETES_VERSION
          value: "latest"
        - name: CSI_PROW_USE_BAZEL
          value: "$(use_bazel "latest")"
        - name: CSI_PROW_DRIVER_VERSION
          value: "$hostpath_driver_version"
        - name: CSI_PROW_DEPLOYMENT_SUFFIX
          value: "$deployment_suffix"
        - name: CSI_SNAPSHOTTER_VERSION
          value: $(snapshotter_version "latest" "")
        - name: CSI_PROW_TESTS
          value: "$(expand_tests "$tests")"
        # docker-in-docker needs privileged mode
        securityContext:
          privileged: true
$(resources_for_kubernetes master)
EOF
                fi
            done # end deployment
        done # end tests
    done # end deployment_suffix

    cat >>"$base/$repo/$repo-config.yaml" <<EOF
  - name: $(job_name "pull" "$repo" "unit")
    always_run: true
    decorate: true
    skip_report: false
    skip_branches: [$(skip_branches $repo)]
    labels:
      preset-service-account: "true"
      preset-dind-enabled: "true"
      preset-bazel-remote-cache-enabled: "true"
      preset-kind-volume-mounts: "true"
    $(annotations "      " "pull" "$repo" "unit")
    spec:
      containers:
      # We need this image because it has Docker in Docker and go.
      - image: ${dind_image}
        command:
        - runner.sh
        args:
        - ./.prow.sh
        env:
        - name: CSI_PROW_TESTS
          value: "unit"
        # docker-in-docker needs privileged mode
        securityContext:
          privileged: true
$(resources_for_kubernetes master)
EOF
done

for repo in $single_kubernetes_repos; do
    mkdir -p "$base/$repo"
    cat >"$base/$repo/$repo-config.yaml" <<EOF
# generated by gen-jobs.sh, do not edit manually

presubmits:
  kubernetes-csi/$repo:
EOF
    for tests in non-alpha unit alpha; do
        cat >>"$base/$repo/$repo-config.yaml" <<EOF
  - name: $(job_name "pull" "$repo" "$tests")
    always_run: true
    optional: $(pull_optional "$tests")
    decorate: true
    skip_report: false
    skip_branches: [$(skip_branches $repo)]
    labels:
      preset-service-account: "true"
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
    $(annotations "      " "pull" "$repo" "$tests")
    spec:
      containers:
      # We need this image because it has Docker in Docker and go.
      - image: ${dind_image}
        command:
        - runner.sh
        args:
        - ./.prow.sh
        env:
        - name: CSI_PROW_DRIVER_VERSION
          value: "$hostpath_driver_version"
        - name: CSI_SNAPSHOTTER_VERSION
          value: $(snapshotter_version "" "")
        - name: CSI_PROW_TESTS
          value: "$(expand_tests "$tests")"
        # docker-in-docker needs privileged mode
        securityContext:
          privileged: true
$(resources_for_kubernetes default)
EOF
    done
done

# Single job for everything.
for repo in $unit_testing_repos; do
    mkdir -p "$base/$repo"
    cat >"$base/$repo/$repo-config.yaml" <<EOF
# generated by gen-jobs.sh, do not edit manually

presubmits:
  kubernetes-csi/$repo:
EOF

    cat >>"$base/$repo/$repo-config.yaml" <<EOF
  - name: pull-kubernetes-csi-$repo
    always_run: true
    decorate: true
    skip_report: false
    skip_branches: [$(skip_branches $repo)]
    labels:
      preset-service-account: "true"
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
    $(annotations "      " "pull" "$repo")
    spec:
      containers:
      # We need this image because it has Docker in Docker and go.
      - image: ${dind_image}
        command:
        - runner.sh
        args:
        - ./.prow.sh
        env:
        - name: CSI_SNAPSHOTTER_VERSION
          value: $(snapshotter_version "" "")
        # docker-in-docker needs privileged mode
        securityContext:
          privileged: true
$(resources_for_kubernetes default)
EOF
done

# The csi-driver-host-path repo contains different deployments. We
# test those against different Kubernetes releases at regular
# intervals. We do this for several reasons:
# - Detect regressions in Kubernetes. This can happen because
#   Kubernetes does not test against all of our deployments when
#   preparing an update.
# - Not all test configurations are covered by pre-submit jobs.
# - The actual deployment content is not used verbatim in pre-submit
#   jobs. The csi-driver-host-path image itself always gets replaced.
#
# This does E2E testing, with alpha tests only enabled in cases where
# it makes sense. Unit tests are not enabled because we aren't building
# the components.
cat >>"$base/csi-driver-host-path/csi-driver-host-path-config.yaml" <<EOF

periodics:
EOF

for deployment_suffix in "" "-test"; do
    for tests in non-alpha alpha; do
        for deployment in $deployment_versions; do
            for kubernetes in $deployment_versions master; do # these tests run against top of release-1.X instead of a specific release version
                if [ "$tests" = "alpha" ]; then
                    # No version skew testing of alpha features, deployment has to match Kubernetes.
                    if ! echo "$kubernetes" | grep -q "^$deployment"; then
                        continue
                    fi
                    # Alpha testing is only done on the latest stable version or
                    # master
                    if [ "$kubernetes" != "$latest_stable_k8s_minor_version" ] && [ "$kubernetes" != "master" ]; then
                        continue
                    fi
                fi

                # Skip generating tests where the k8s version is lower than the deployment version
                # because we do not support running newer deployments and sidecars on older kubernetes releases.
                # The recommended Kubernetes version can be found in each kubernetes-csi sidecar release.
                if [[ $kubernetes < $deployment ]]; then
                    continue
                fi
                actual="$(if [ "$kubernetes" = "master" ]; then echo latest; else echo "release-$kubernetes"; fi)"
                cat >>"$base/csi-driver-host-path/csi-driver-host-path-config.yaml" <<EOF
- interval: 6h
  name: $(job_name "ci" "" "$tests" "$deployment$deployment_suffix" "$kubernetes")
  decorate: true
  extra_refs:
  - org: kubernetes-csi
    repo: csi-driver-host-path
    base_ref: master
  labels:
    preset-service-account: "true"
    preset-dind-enabled: "true"
    preset-bazel-remote-cache-enabled: "$(if [ "$kubernetes" = "master" ]; then echo true; else echo false; fi)"
    preset-kind-volume-mounts: "true"
  $(annotations "    " "ci" "" "$tests" "$deployment$deployment_suffix" "$kubernetes")
  spec:
    containers:
    # We need this image because it has Docker in Docker and go.
    - image: ${dind_image}
      command:
      - runner.sh
      args:
      - ./.prow.sh
      env:
      - name: CSI_PROW_KUBERNETES_VERSION
        value: "$actual"
      - name: CSI_PROW_USE_BAZEL
        value: "$(use_bazel "$actual")"
      - name: CSI_SNAPSHOTTER_VERSION
        value: $(snapshotter_version "$actual" "")
      - name: CSI_PROW_BUILD_JOB
        value: "false"
      - name: CSI_PROW_DEPLOYMENT
        value: "kubernetes-$deployment"
      - name: CSI_PROW_DEPLOYMENT_SUFFIX
        value: "$deployment_suffix"
      - name: CSI_PROW_TESTS
        value: "$(expand_tests "$tests")"
      # docker-in-docker needs privileged mode
      securityContext:
        privileged: true
$(resources_for_kubernetes "$actual")
EOF
            done
        done
    done
done

# The canary builds use the latest sidecars from master and run them on
# specific Kubernetes versions, using the default deployment for that Kubernetes
# release.
for deployment_suffix in "" "-test"; do
    for kubernetes in $k8s_versions master; do
        # master -> latest
        actual="${kubernetes/master/latest}"
        # 1.20 -> 1.20.0
        actual="$(echo "$actual" | sed -e 's/^\([0-9]*\)\.\([0-9]*\)$/\1.\2.0/')"

        for tests in non-alpha alpha; do
            # Alpha with latest sidecars only on master.
            if [ "$tests" = "alpha" ] && [ "$kubernetes" != "master" ]; then
                continue
            fi
            alpha_testgrid_prefix="$(if [ "$tests" = "alpha" ]; then echo alpha-; fi)"
            cat >>"$base/csi-driver-host-path/csi-driver-host-path-config.yaml" <<EOF
- interval: 6h
  name: $(job_name "ci" "" "$tests" "canary$deployment_suffix" "$kubernetes")
  decorate: true
  extra_refs:
  - org: kubernetes-csi
    repo: csi-driver-host-path
    base_ref: master
  labels:
    preset-service-account: "true"
    preset-dind-enabled: "true"
    preset-bazel-remote-cache-enabled: "true"
    preset-kind-volume-mounts: "true"
  $(annotations "    " "ci" "" "$tests" "canary$deployment_suffix" "$kubernetes")
  spec:
    containers:
    # We need this image because it has Docker in Docker and go.
    - image: ${dind_image}
      command:
      - runner.sh
      args:
      - ./.prow.sh
      env:
      - name: CSI_PROW_KUBERNETES_VERSION
        value: "$actual"
      - name: CSI_PROW_USE_BAZEL
        value: "$(use_bazel "$actual")"
      - name: CSI_PROW_BUILD_JOB
        value: "false"
      # Replace images....
      - name: CSI_PROW_HOSTPATH_CANARY
        value: "canary"
      - name: CSI_PROW_DEPLOYMENT_SUFFIX
        value: "$deployment_suffix"
      - name: CSI_SNAPSHOTTER_VERSION
        value: $(snapshotter_version "$actual" "canary")
      # ... but the RBAC rules only when testing on master.
      # The other jobs test against the unmodified deployment for
      # that Kubernetes version, i.e. with the original RBAC rules.
      - name: UPDATE_RBAC_RULES
        value: "$([ "$kubernetes" = "master" ] && echo "true" || echo "false")"
      - name: CSI_PROW_TESTS
        value: "$(expand_tests "$tests")"
      # docker-in-docker needs privileged mode
      securityContext:
        privileged: true
$(resources_for_kubernetes "$actual")
EOF
        done
    done
done

for repo in $csi_release_tools_repos; do
    cat >>"$base/csi-release-tools/csi-release-tools-config.yaml" <<EOF
  - name: $(job_name "pull" "release-tools" "$repo" "" "")
    always_run: true
    optional: true # cannot be required because updates in csi-release-tools may include breaking changes
    decorate: true
    skip_report: false
    extra_refs:
    - org: kubernetes-csi
      repo: $repo
      base_ref: master
      workdir: false
      # Checked out in /home/prow/go/src/github.com/kubernetes-csi/$repo
    labels:
      preset-service-account: "true"
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
    annotations:
      testgrid-dashboards: sig-storage-csi-other
      testgrid-tab-name: pull-csi-release-tools-in-$repo
      description: Kubernetes-CSI pull job in repo csi-release-tools for $repo, using deployment $latest_stable_k8s_version on Kubernetes $latest_stable_k8s_version
    spec:
      containers:
      # We need this image because it has Docker in Docker and go.
      - image: ${dind_image}
        command:
        - runner.sh
        args:
        - ./pull-test.sh # provided by csi-release-tools
        env:
        - name: CSI_PROW_KUBERNETES_VERSION
          value: "$latest_stable_k8s_version.0"
        - name: CSI_PROW_USE_BAZEL
          value: "$(use_bazel "$latest_stable_k8s_version")"
        - name: CSI_PROW_KUBERNETES_DEPLOYMENT
          value: "$latest_stable_k8s_version"
        - name: CSI_PROW_DRIVER_VERSION
          value: "$hostpath_driver_version"
        - name: CSI_SNAPSHOTTER_VERSION
          value: $(snapshotter_version "$latest_stable_k8s_version" "")
        - name: CSI_PROW_TESTS
          value: "unit sanity parallel"
        - name: PULL_TEST_REPO_DIR
          value: /home/prow/go/src/github.com/kubernetes-csi/$repo
        # docker-in-docker needs privileged mode
        securityContext:
          privileged: true
$(resources_for_kubernetes "$latest_stable_k8s_version")
EOF
done
