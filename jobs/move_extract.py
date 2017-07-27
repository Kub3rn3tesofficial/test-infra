#!/usr/bin/env python

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

"""Migrate --extract flag from an JENKINS_FOO env to a scenario flag."""

import json
import os
import re
import sys

ORIG_CWD = os.getcwd()  # Checkout changes cwd

def test_infra(*paths):
    """Return path relative to root of test-infra repo."""
    return os.path.join(ORIG_CWD, os.path.dirname(__file__), '..', *paths)

def sort():
    """Sort config.json alphabetically."""
    # pylint: disable=too-many-branches,too-many-statements,too-many-locals
    with open(test_infra('jobs/config.json'), 'r+') as fp:
        configs = json.loads(fp.read())
    regexp = re.compile('|'.join([
        r'^KUBE_GKE_NETWORK=(.*)$',
        r'^KUBE_GCE_NETWORK=(.*)$',
    ]))
    problems = []
    for job, values in configs.items():
        if values.get('scenario') != 'kubernetes_e2e':
            continue
        if 'args' not in values:
            continue
        args = values['args']
        if any('None' in a for a in args):
            problems.append('Bad flag with None: %s' % job)
            continue
        with open(test_infra('jobs/%s.env' % job)) as fp:
            env = fp.read()
        lines = []
        new_args = {}
        processed = []
        for arg in args:
            if re.search(r'^(--test_args)=', arg):
                key, val = arg.split('=', 1)
                new_args[key] = val
            else:
                processed.append(arg)
        args = processed
        okay = False
        mod = False
        for line in env.split('\n'):
            mat = regexp.search(line)
            if not mat:
                lines.append(line)
                continue
            knet, cnet = mat.groups()
            net = knet or cnet
            stop = False
            for key, val in {
                    '--gcp-network': net,
            }.items():
                if not val:
                    continue
                if key in new_args and val != new_args[key]:
                    problems.append('Duplicate %s in %s' % (key, job))
                    stop = True
                    break
                new_args[key] = val
                mod = True
            if stop:
                break
        else:
            okay = True
        if not okay or not mod:
            continue
        args = list(args)
        for key, val in new_args.items():
            args.append('%s=%s' % (key, val))
        flags = set()
        okay = False
        for arg in args:
            try:
                flag, _ = arg.split('=', 1)
            except ValueError:
                flag = ''
            if flag and flag not in ['--env-file', '--extract']:
                if flag in flags:
                    problems.append('Multiple %s in %s: %s' % (flag, job, args))
                    break
                flags.add(flag)
        else:
            okay = True
        if not okay:
            continue
        values['args'] = args
        with open(test_infra('jobs/%s.env' % job), 'w') as fp:
            fp.write('\n'.join(lines))
    with open(test_infra('jobs/config.json'), 'w') as fp:
        fp.write(json.dumps(configs, sort_keys=True, indent=2, separators=(',', ': ')))
        fp.write('\n')
    if not problems:
        sys.exit(0)
    print >>sys.stderr, '%d problems' % len(problems)
    print '\n'.join(problems)

if __name__ == '__main__':
    sort()
