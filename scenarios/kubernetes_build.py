#!/usr/bin/env python

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

# Need to figure out why this only fails on travis
# pylint: disable=bad-continuation

"""Builds kubernetes with specified config"""

import argparse
import os
import subprocess
import sys
import tarfile


def check(*cmd):
    """Log and run the command, raising on errors."""
    print >>sys.stderr, 'Run:', cmd
    subprocess.check_call(cmd)

def upload_string(gcs_path, text):
    """Uploads s to gcs_path"""
    cmd = ['gsutil', '-q', '-h', 'Content-Type:text/plain', 'cp', '-', gcs_path]
    print >>sys.stderr, 'Run:', cmd, 'stdin=%s'%text
    proc = subprocess.Popen(cmd, stdin=subprocess.PIPE)
    proc.communicate(input=text)

def main(args):
    """Build and push kubernetes.

    This is a python port of the kubernetes/hack/jenkins/build.sh script.
    """
    if os.path.split(os.getcwd())[-1] != 'kubernetes':
        print >>sys.stderr, (
            'Scenario should only run from a kubernetes directory!')
        sys.exit(1)
    env = {
        # Skip gcloud update checking; do we still need this?
        'CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK': 'true',
        # Don't run any unit/integration tests when building
        'KUBE_RELEASE_RUN_TESTS': 'n',
    }
    push_build_args = ['--nomock', '--verbose', '--ci']
    if args.suffix:
        push_build_args.append('--gcs-suffix=%s' % args.suffix)
    if args.federation:
        # TODO: do we need to set these?
        env['PROJECT'] = args.federation
        env['FEDERATION_PUSH_REPO_BASE'] = 'gcr.io/%s' % args.federation
        push_build_args.append('--federation')
    if args.release:
        push_build_args.append('--bucket=%s' % args.release)
    if args.registry:
        push_build_args.append('--docker-registry=%s' % args.registry)
    if args.hyperkube:
        env['KUBE_BUILD_HYPERKUBE'] = 'y'
    if args.extra_publish_file:
        push_build_args.append('--extra-publish-file=%s' % args.extra_publish_file)

    for key, value in env.items():
        os.environ[key] = value
    check('make', 'clean')
    if args.fast:
        check('make', 'quick-release')
    else:
        check('make', 'release')
    check('../release/push-build.sh', *push_build_args)
    # log push-build location to path child jobs can find
    # (gs://<shared-bucket>/$PULL_REFS/bazel-build-location.txt)
    # first get build location
    bucket = args.release or 'kubernetes-release-dev'
    dest = 'ci'
    if args.suffix:
        dest += args.suffix
    build_tar = tarfile.open("_output/release-tars/kubernetes.tar.gz")
    latest = build_tar.getmember("kubernetes/version").read().strip()
    gcs_build = 'gs://'+bucket+dest+latest
    # and then write it to GCS
    gcs_shared = args.gcs_shared + os.getenv('PULL_REFS', '') + '/build-location.txt'
    upload_string(gcs_shared, gcs_build)


if __name__ == '__main__':
    PARSER = argparse.ArgumentParser(
        'Build and push.')
    PARSER.add_argument('--fast', action='store_true', help='Build quickly')
    PARSER.add_argument(
        '--release', help='Upload binaries to the specified gs:// path')
    PARSER.add_argument(
        '--gcs-shared',
        default="gs://kubernetes-jenkins/shared-results/",
        help='If release is set push build location to this bucket')
    PARSER.add_argument(
        '--suffix', help='Append suffix to the upload path if set')
    PARSER.add_argument(
        '--federation',
        help='Push federation images to the specified project')
    PARSER.add_argument(
        '--registry', help='Push images to the specified docker registry')
    PARSER.add_argument(
        '--hyperkube', action='store_true', help='Build hyperkube image')
    PARSER.add_argument(
        '--extra-publish-file', help='Additional version file uploads to')
    ARGS = PARSER.parse_args()
    main(ARGS)
