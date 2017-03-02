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

# Need to figure out why this only fails on travis
# pylint: disable=too-few-public-methods

"""Test for kubernetes_e2e.py"""

import shutil
import string
import unittest

import kubernetes_e2e

def fake_pass(*_unused, **_unused2):
    """Do nothing."""
    pass

def fake_bomb(*a, **kw):
    """Always raise."""
    raise AssertionError('Should not happen', a, kw)


class Stub(object):
    """Replace thing.param with replacement until exiting with."""
    def __init__(self, thing, param, replacement):
        self.thing = thing
        self.param = param
        self.replacement = replacement
        self.old = getattr(thing, param)
        setattr(thing, param, self.replacement)

    def __enter__(self, *a, **kw):
        return self.replacement

    def __exit__(self, *a, **kw):
        setattr(self.thing, self.param, self.old)


class ScenarioTest(unittest.TestCase):
    """Test for e2e scenario."""
    callstack = []
    envs = {}

    def setUp(self):
        self.parser = kubernetes_e2e.create_parser()
        self.boiler = [
            Stub(kubernetes_e2e, 'check', self.fake_check),
            Stub(shutil, 'copy', fake_pass),
        ]

    def tearDown(self):
        for stub in self.boiler:
            with stub:  # Leaving with restores things
                pass
        self.callstack[:] = []
        self.envs.clear()

    def fake_check(self, *cmd):
        """Log the command."""
        self.callstack.append(string.join(cmd))

    def fake_check_env(self, env, *cmd):
        """Log the command with a specific env."""
        self.envs.update(env)
        self.callstack.append(string.join(cmd))


class LocalTest(ScenarioTest):
    """Class for testing e2e scenario in local mode."""
    def test_local(self):
        """Make sure local mode is fine overall."""
        args = self.parser.parse_args(['--mode=local'])
        self.assertEqual(args.mode, 'local')
        with Stub(kubernetes_e2e, 'check_env', self.fake_check_env):
            kubernetes_e2e.main(args)

        self.assertNotEqual(self.envs, {})
        for call in self.callstack:
            self.assertFalse(call.startswith('docker'))

class DockerTest(ScenarioTest):
    """Class for testing e2e scenario in docker mode."""
    def test_docker(self):
        """Make sure docker mode is fine overall."""
        args = self.parser.parse_args()
        self.assertEqual(args.mode, 'docker')
        with Stub(kubernetes_e2e, 'check_env', fake_bomb):
            kubernetes_e2e.main(args)

        self.assertEqual(self.envs, {})
        for call in self.callstack:
            self.assertTrue(call.startswith('docker'))


if __name__ == '__main__':
    unittest.main()
