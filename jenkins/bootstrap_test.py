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

"""Tests for bootstrap."""

import collections
import json
import os
import re
import subprocess
import tempfile
import time
import unittest

import bootstrap

import yaml


BRANCH = 'random_branch'
BUILD = 'random_build'
FAIL = ['/bin/bash', '-c', 'exit 1']
JOB = 'random_job'
PASS = ['/bin/bash', '-c', 'exit 0']
PULL = 12345
REPO = 'github.com/random_org/random_repo'
ROOT = '/random/root'


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


class FakeCall(object):
    def __init__(self):
        self.calls = []

    def __call__(self, *a, **kw):
        self.calls.append((a, kw))


class FakeSubprocess(object):
    """Keep track of calls."""
    def __init__(self):
        self.calls = []

    def __call__(self, cmd, *a, **kw):
        self.calls.append((cmd, a, kw))


def Pass(*a, **kw):
    """Do nothing."""
    pass


def Truth(*a, **kw):
    """Always true."""
    return True


def Bomb(*a, **kw):
    """Always raise."""
    raise AssertionError('Should not happen', a, kw)


class SubprocessTest(unittest.TestCase):
    """Tests for call()."""

    def testStdin(self):
        """Will write to subprocess.stdin."""
        with self.assertRaises(subprocess.CalledProcessError) as cpe:
            bootstrap.call(['/bin/bash'], stdin='exit 92')
        self.assertEquals(92, cpe.exception.returncode)

    def testCheckTrue(self):
        """Raise on non-zero exit codes if check is set."""
        with self.assertRaises(subprocess.CalledProcessError) as cpe:
            bootstrap.call(FAIL, check=True)

        bootstrap.call(PASS, check=True)

    def testCheckDefault(self):
        """Default to check=True."""
        with self.assertRaises(subprocess.CalledProcessError) as cpe:
            bootstrap.call(FAIL)

        bootstrap.call(PASS)

    def testCheckFalse(self):
        """Never raise when check is not set."""
        bootstrap.call(FAIL, check=False)
        bootstrap.call(PASS, check=False)

    def testOutput(self):
        """Output is returned when requested."""
        cmd = ['/bin/bash', '-c', 'echo hello world']
        self.assertEquals(
            'hello world\n', bootstrap.call(cmd, output=True))

class CheckoutTest(unittest.TestCase):
    """Tests for checkout()."""

    def testFetchRetries(self):
        self.tries = 0
        expected_attempts = 3
        def ThirdTimeCharm(cmd, *a, **kw):
            if 'fetch' not in cmd:  # init/checkout are unlikely to fail
                return
            self.tries += 1
            if self.tries != expected_attempts:
                raise subprocess.CalledProcessError(128, cmd, None)
        with Stub(bootstrap, 'call', ThirdTimeCharm):
            with Stub(os, 'chdir', Pass):
                with Stub(time, 'sleep', Pass):
                    bootstrap.checkout(REPO, None, PULL)
        self.assertEquals(expected_attempts, self.tries)


    def testPull(self):
        """checkout fetches the right ref for a pull."""
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            with Stub(os, 'chdir', Pass):
                bootstrap.checkout(REPO, None, PULL)

        expected_ref = bootstrap.pull_ref(PULL)
        self.assertTrue(any(
            expected_ref in cmd for cmd, _, _ in fake.calls if 'fetch' in cmd))

    def testBranch(self):
        """checkout fetches the right ref for a branch."""
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            with Stub(os, 'chdir', Pass):
                bootstrap.checkout(REPO, BRANCH, None)

        expected_ref = BRANCH
        self.assertTrue(any(
            expected_ref in cmd for cmd, _, _ in fake.calls if 'fetch' in cmd))

    def testRepo(self):
        """checkout initializes and fetches the right repo."""
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            with Stub(os, 'chdir', Pass):
                bootstrap.checkout(REPO, BRANCH, None)

        expected_uri = 'https://%s' % REPO
        self.assertTrue(any(
            expected_uri in cmd for cmd, _, _ in fake.calls if 'fetch' in cmd))

    def testBranchXorPull(self):
        """Either branch or pull specified, not both."""
        with Stub(bootstrap, 'call', Bomb), Stub(os, 'chdir', Bomb):
            with self.assertRaises(ValueError):
              bootstrap.checkout(REPO, None, None)
            with self.assertRaises(ValueError):
              bootstrap.checkout(REPO, BRANCH, PULL)

    def testHappy(self):
        """checkout sanity check."""
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            with Stub(os, 'chdir', Pass):
                bootstrap.checkout(REPO, BRANCH, None)

        self.assertTrue(any(
            '--tags' in cmd for cmd, _, _ in fake.calls if 'fetch' in cmd))
        self.assertTrue(any(
            'FETCH_HEAD' in cmd for cmd, _, _ in fake.calls
            if 'checkout' in cmd))


class GSUtilTest(unittest.TestCase):
    """Tests for GSUtil."""
    def testUploadJson(self):
        gsutil = bootstrap.GSUtil()
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            gsutil.upload_json('fake_path', {'wee': 'fun'})
        self.assertTrue(any(
            'application/json' in a for a in fake.calls[0][0]))
        self.assertIn('stdin', fake.calls[0][2])  # kwargs

    def testUploadText_Cached(self):
        gsutil = bootstrap.GSUtil()
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            gsutil.upload_text('fake_path', 'hello world', cached=True)
        self.assertFalse(any(
            'Cache-Control' in a and 'max-age' in a
            for a in fake.calls[0][0]))
        self.assertIn('stdin', fake.calls[0][2])  # kwargs

    def testUploadText_Default(self):
        gsutil = bootstrap.GSUtil()
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            gsutil.upload_text('fake_path', 'hello world')
        self.assertFalse(any(
            'Cache-Control' in a and 'max-age' in a
            for a in fake.calls[0][0]))
        self.assertIn('stdin', fake.calls[0][2])  # kwargs

    def testUploadText_Uncached(self):
        gsutil = bootstrap.GSUtil()
        with Stub(bootstrap, 'call', FakeSubprocess()) as fake:
            gsutil.upload_text('fake_path', 'hello world', cached=False)
        self.assertTrue(any(
            'Cache-Control' in a and 'max-age' in a
            for a in fake.calls[0][0]))
        self.assertIn('stdin', fake.calls[0][2])  # kwargs


class FakeGSUtil(object):
    generation = 123

    def __init__(self):
        self.cats = []
        self.jsons = []
        self.stats = []
        self.texts = []

    def cat(self, *a, **kw):
        self.cats.append((a, kw))
        return 'this is not a list'

    def stat(self, *a, **kw):
        self.stats.append((a, kw))
        return 'Generation: %s' % self.generation

    def upload_text(self, *args, **kwargs):
        self.texts.append((args, kwargs))

    def upload_json(self, *args, **kwargs):
        self.jsons.append((args, kwargs))


class AppendResultTest(unittest.TestCase):
    """Tests for append_result()."""

    def testNewJob(self):
        """Stat fails when the job doesn't exist."""
        gsutil = FakeGSUtil()
        build = 123
        version = 'v.interesting'
        success = True
        def fake_stat(*a, **kw):
            raise subprocess.CalledProcessError(1, ['gsutil'], None)
        gsutil.stat = fake_stat
        bootstrap.append_result(gsutil, 'fake_path', build, version, success)
        cache = gsutil.jsons[0][0][1]
        self.assertEquals(1, len(cache))

    def testCollision_Cat(self):
        """cat fails if the cache has been updated."""
        gsutil = FakeGSUtil()
        build = 42
        version = 'v1'
        success = False
        generations = ['555', '444']
        orig_stat = gsutil.stat
        def fake_stat(*a, **kw):
            gsutil.generation = generations.pop()
            return orig_stat(*a, **kw)
        def fake_cat(_, gen):
            if gen == '555':  # Which version is requested?
                return '[{"hello": 111}]'
            raise subprocess.CalledProcessError(1, ['gsutil'], None)
        with Stub(bootstrap, 'random_sleep', Pass):
            with Stub(gsutil, 'stat', fake_stat):
                with Stub(gsutil, 'cat', fake_cat):
                    bootstrap.append_result(
                        gsutil, 'fake_path', build, version, success)
        self.assertIn('generation', gsutil.jsons[-1][1], gsutil.jsons)
        self.assertEquals('555', gsutil.jsons[-1][1]['generation'], gsutil.jsons)


    def testCollision_Upload(self):
        """Test when upload_json tries to update an old version."""
        gsutil = FakeGSUtil()
        build = 42
        version = 'v1'
        success = False
        generations = [555, 444]
        orig = gsutil.upload_json
        def fake_upload(path, cache, generation):
            if generation == '555':
                return orig(path, cache, generation=generation)
            raise subprocess.CalledProcessError(128, ['gsutil'], None)
        orig_stat = gsutil.stat
        def fake_stat(*a, **kw):
            gsutil.generation = generations.pop()
            return orig_stat(*a, **kw)
        def fake_call(*a, **kw):
            return '[{"hello": 111}]'
        with Stub(bootstrap, 'random_sleep', Pass):
            with Stub(gsutil, 'stat', fake_stat):
                with Stub(gsutil, 'upload_json', fake_upload):
                    with Stub(bootstrap, 'call', fake_call):
                        bootstrap.append_result(
                            gsutil, 'fake_path', build, version, success)
        self.assertIn('generation', gsutil.jsons[-1][1], gsutil.jsons)
        self.assertEquals('555', gsutil.jsons[-1][1]['generation'], gsutil.jsons)

    def testHandleJunk(self):
        gsutil = FakeGSUtil()
        build = 123
        version = 'v.interesting'
        success = True
        with Stub(bootstrap, 'call', lambda *a, **kw: '!@!$!@$@!$'):
            bootstrap.append_result(gsutil, 'fake_path', build, version, success)
        cache = gsutil.jsons[0][0][1]
        self.assertEquals(1, len(cache))
        self.assertIn(build, cache[0].values())
        self.assertIn(version, cache[0].values())

    def testPassedIsBool(self):
        build = 123
        version = 'v.interesting'
        def Try(success):
            gsutil = FakeGSUtil()
            with Stub(bootstrap, 'call', lambda *a, **kw: ''):
                bootstrap.append_result(gsutil, 'fake_path', build, version, success)
            cache = gsutil.jsons[0][0][1]
            self.assertTrue(isinstance(cache[0]['passed'], bool))

        Try(1)
        Try(0)
        Try(None)
        Try('')
        Try('hello')
        Try('true')

    def testTruncate(self):
        old = json.dumps({n: True for n in range(100000)})
        gsutil = FakeGSUtil()
        build = 123
        version = 'v.interesting'
        success = True
        with Stub(bootstrap, 'call', lambda *a, **kw: old):
            bootstrap.append_result(gsutil, 'fake_path', build, version, success)
        cache = gsutil.jsons[0][0][1]
        self.assertLess(len(cache), len(old))



class FinishTest(unittest.TestCase):
    """Tests for finish()."""
    def setUp(self):
      self.stubs = [
          Stub(bootstrap.GSUtil, 'upload_artifacts', Pass),
          Stub(bootstrap, 'append_result', Pass),
          Stub(os.path, 'isfile', Pass),
          Stub(os.path, 'isdir', Pass),
      ]

    def tearDown(self):
        for stub in self.stubs:
            with stub:
                pass

    def testIgnoreError_UploadArtifacts(self):
        paths = FakePath()
        gsutil = FakeGSUtil()
        local_artifacts = None
        build = 123
        version = 'v1.terrible'
        success = True
        calls = []
        with Stub(os.path, 'isdir', lambda _: True):
            with Stub(os, 'walk', lambda d: [(True, True, True)]):
                def fake_upload(*a, **kw):
                    calls.append((a, kw))
                    raise subprocess.CalledProcessError(1, ['fakecmd'], None)
                gsutil.upload_artifacts = fake_upload
                bootstrap.finish(
                    gsutil, paths, success, local_artifacts,
                    build, version, REPO)
                self.assertTrue(calls)


    def testIgnoreError_UploadText(self):
        paths = FakePath()
        gsutil = FakeGSUtil()
        local_artifacts = None
        build = 123
        version = 'v1.terrible'
        success = True
        calls = []
        with Stub(os.path, 'isdir', lambda _: True):
            with Stub(os, 'walk', lambda d: [(True, True, True)]):
                def fake_upload(*a, **kw):
                    calls.append((a, kw))
                    raise subprocess.CalledProcessError(1, ['fakecmd'], None)
                gsutil.upload_artifacts = Pass
                gsutil.upload_text = fake_upload
                bootstrap.finish(
                    gsutil, paths, success, local_artifacts,
                    build, version, REPO)
                self.assertTrue(calls)
                self.assertGreater(calls, 1)

    def testSkipUploadArtifacts(self):
        """Do not upload artifacts dir if it doesn't exist."""
        paths = FakePath()
        gsutil = FakeGSUtil()
        local_artifacts = None
        build = 123
        version = 'v1.terrible'
        success = True
        with Stub(os.path, 'isdir', lambda _: False):
            with Stub(bootstrap.GSUtil, 'upload_artifacts', Bomb):
                bootstrap.finish(
                    gsutil, paths, success, local_artifacts,
                    build, version, REPO)


class MetadataTest(unittest.TestCase):
    def testAlwaysSetMetadata(self):
        meta = bootstrap.metadata(REPO, 'missing-artifacts-dir')
        self.assertIn('repo', meta)
        self.assertEquals(REPO, meta['repo'])


SECONDS = 10


def FakeEnviron(
    set_home=True, set_node=True, set_job=True,
    **kwargs
):
    if set_home:
        kwargs.setdefault(bootstrap.HOME_ENV, '/fake/home-dir')
    if set_node:
        kwargs.setdefault(bootstrap.NODE_ENV, 'fake-node')
    if set_job:
        kwargs.setdefault(bootstrap.JOB_ENV, JOB)
    return kwargs


class BuildNameTest(unittest.TestCase):
    """Tests for build_name()."""

    def testAuto(self):
        """Automatically select a build if not done by user."""
        with Stub(os, 'environ', FakeEnviron()) as fake:
            bootstrap.build_name(SECONDS)
            self.assertTrue(fake[bootstrap.BUILD_ENV])

    def testManual(self):
        """Respect user-selected build."""
        with Stub(os, 'environ', FakeEnviron()) as fake:
            truth = 'erick is awesome'
            fake[bootstrap.BUILD_ENV] = truth
            self.assertEquals(truth, fake[bootstrap.BUILD_ENV])

    def testUnique(self):
        """New build every minute."""
        with Stub(os, 'environ', FakeEnviron()) as fake:
            bootstrap.build_name(SECONDS)
            first = fake[bootstrap.BUILD_ENV]
            del fake[bootstrap.BUILD_ENV]
            bootstrap.build_name(SECONDS + 60)
            self.assertNotEqual(first, fake[bootstrap.BUILD_ENV])



class SetupCredentialsTest(unittest.TestCase):
    """Tests for setup_credentials()."""

    def setUp(self):
        keys = {
            bootstrap.GCE_KEY_ENV: 'fake-key',
            bootstrap.SERVICE_ACCOUNT_ENV: 'fake-service-account.json',
        }
        self.env = FakeEnviron(**keys)


    def testRequireGoogleApplicationCredentials(self):
        """Raise if GOOGLE_APPLICATION_CREDENTIALS does not exist."""
        del self.env[bootstrap.SERVICE_ACCOUNT_ENV]
        with Stub(os, 'environ', self.env) as fake:
            gac = 'FAKE_CREDS.json'
            fake['HOME'] = 'kansas'
            fake[bootstrap.SERVICE_ACCOUNT_ENV] = gac
            with Stub(os.path, 'isfile', lambda p: p != gac):
                with self.assertRaises(IOError):
                    bootstrap.setup_credentials()

            with Stub(os.path, 'isfile', Truth):
                with Stub(bootstrap, 'call', Pass):
                    bootstrap.setup_credentials()

    def testRequireGCEKey(self):
        """Raise if the private gce does not exist."""
        del self.env[bootstrap.GCE_KEY_ENV]
        with Stub(os, 'environ', self.env) as fake:
            pkf = 'FAKE_PRIVATE_KEY'
            fake['HOME'] = 'kansas'
            fake[bootstrap.GCE_KEY_ENV] = pkf
            with Stub(os.path, 'isfile', lambda p: p != pkf):
                with self.assertRaises(IOError):
                    bootstrap.setup_credentials()

            with Stub(os.path, 'isfile', Truth):
                with Stub(bootstrap, 'call', Pass):
                    bootstrap.setup_credentials()

class SetupMagicEnvironmentTest(unittest.TestCase):
    def testWorkspace(self):
        """WORKSPACE exists, equals HOME and is set to cwd."""
        env = FakeEnviron()
        cwd = '/fake/random-location'
        with Stub(os, 'environ', env):
            with Stub(os, 'getcwd', lambda: cwd):
                bootstrap.setup_magic_environment(JOB)

        self.assertIn(bootstrap.WORKSPACE_ENV, env)
        self.assertEquals(env[bootstrap.HOME_ENV], env[bootstrap.WORKSPACE_ENV])
        self.assertEquals(cwd, env[bootstrap.WORKSPACE_ENV])

    def testJobEnvMismatch(self):
        env = FakeEnviron()
        with Stub(os, 'environ', env):
            with self.assertRaises(ValueError):
                bootstrap.setup_magic_environment('this-is-a-job')

    def testExpected(self):
        env = FakeEnviron()
        del env[bootstrap.JOB_ENV]
        del env[bootstrap.NODE_ENV]
        with Stub(os, 'environ', env):
            bootstrap.setup_magic_environment(JOB)

        def Check(name):
            self.assertIn(name, env)

        # Some of these are probably silly to check...
        # TODO(fejta): remove as many of these from our infra as possible.
        Check(bootstrap.JOB_ENV)
        Check(bootstrap.CLOUDSDK_ENV)
        Check(bootstrap.BOOTSTRAP_ENV)
        Check(bootstrap.WORKSPACE_ENV)
        Check(bootstrap.SERVICE_ACCOUNT_ENV)

    def testNode_Present(self):
        expected = 'whatever'
        env = {bootstrap.NODE_ENV: expected}
        with Stub(os, 'environ', env):
            self.assertEquals(expected, bootstrap.node())
        self.assertEquals(expected, env[bootstrap.NODE_ENV])

    def testNode_Missing(self):
        env = {}
        with Stub(os, 'environ', env):
            expected = bootstrap.node()
            self.assertTrue(expected)
        self.assertEquals(expected, env[bootstrap.NODE_ENV])



    def testCloudSdkConfig(self):
        cwd = 'now-here'
        env = FakeEnviron()
        with Stub(os, 'environ', env):
            with Stub(os, 'getcwd', lambda: cwd):
                bootstrap.setup_magic_environment(JOB)


        self.assertTrue(env[bootstrap.CLOUDSDK_ENV].startswith(cwd))


class FakePath(object):
    artifacts = 'fake_artifacts'
    pr_latest = 'fake_pr_latest'
    pr_build_link = 'fake_pr_link'
    build_log = 'fake_log_path'
    pr_path = 'fake_pr_path'
    pr_result_cache = 'fake_pr_result_cache'
    latest = 'fake_latest'
    result_cache = 'fake_result_cache'
    started = 'fake_started.json'
    finished = 'fake_finished.json'
    def __call__(self, *a, **kw):
        self.a = a
        self.kw = kw
        return self


class FakeLogging(object):
    close = Pass
    def __call__(self, *a, **kw):
        return self


class FakeFinish(object):
    called = False
    result = None
    def __call__(self, unused_a, unused_b, success, *a, **kw):
        self.called = True
        self.result = success

class PRPathsTest(unittest.TestCase):
    def testKubernetesKubernetes(self):
        """Test the kubernetes/kubernetes prefix."""
        path = bootstrap.pr_paths('kubernetes/kubernetes', JOB, BUILD, PULL)
        self.assertTrue(any(
            str(PULL) == p for p in path.build_log.split('/')))

    def testKubernetes(self):
        """Test the kubernetes/something prefix."""
        path = bootstrap.pr_paths('kubernetes/prefix', JOB, BUILD, PULL)
        self.assertTrue(any(
            'prefix' in p for p in path.build_log.split('/')), path.build_log)
        self.assertTrue(any(
            str(PULL) in p for p in path.build_log.split('/')), path.build_log)

    def testOther(self):
        """Test the none kubernetes prefixes."""
        path = bootstrap.pr_paths('github.com/random/repo', JOB, BUILD, PULL)
        self.assertTrue(any(
            'random_repo' in p for p in path.build_log.split('/')), path.build_log)
        self.assertTrue(any(
            str(PULL) in p for p in path.build_log.split('/')), path.build_log)


class BootstrapTest(unittest.TestCase):

    def setUp(self):
        self.boiler = [
            Stub(bootstrap, 'checkout', Pass),
            Stub(bootstrap, 'finish', Pass),
            Stub(bootstrap.GSUtil, 'copy_file', Pass),
            Stub(bootstrap, 'node', lambda: 'fake-node'),
            Stub(bootstrap, 'setup_credentials', Pass),
            Stub(bootstrap, 'setup_logging', FakeLogging()),
            Stub(bootstrap, 'start', Pass),
            Stub(bootstrap, 'call', Pass),
            Stub(os, 'environ', FakeEnviron()),
            Stub(os, 'chdir', Pass),
            Stub(os, 'makedirs', Pass),
        ]

    def tearDown(self):
        for stub in self.boiler:
            with stub:  # Leaving with restores things
                pass

    def testRoot_NotExists(self):
        with Stub(os, 'chdir', FakeCall()) as fake_chdir:
            with Stub(os.path, 'exists', lambda p: False):
                with Stub(os, 'makedirs', FakeCall()) as fake_makedirs:
                    bootstrap.bootstrap(JOB, REPO, None, PULL, ROOT)
        self.assertTrue(any(ROOT in c[0] for c in fake_chdir.calls), fake_chdir.calls)
        self.assertTrue(any(ROOT in c[0] for c in fake_makedirs.calls), fake_makedirs.calls)

    def testRoot_Exists(self):
        with Stub(os, 'chdir', FakeCall()) as fake_chdir:
            bootstrap.bootstrap(JOB, REPO, None, PULL, ROOT)
        self.assertTrue(any(ROOT in c[0] for c in fake_chdir.calls))

    def testPRPaths(self):
        """Use a pr_paths when pull is set."""

        with Stub(bootstrap, 'ci_paths', Bomb):
            with Stub(bootstrap, 'pr_paths', FakePath()) as path:
                bootstrap.bootstrap(JOB, REPO, None, PULL, ROOT)
            self.assertTrue(PULL in path.a or PULL in path.kw)

    def testCIPaths(self):
        """Use a ci_paths when branch is set."""

        with Stub(bootstrap, 'pr_paths', Bomb):
            with Stub(bootstrap, 'ci_paths', FakePath()) as path:
                bootstrap.bootstrap(JOB, REPO, BRANCH, None, ROOT)
            self.assertFalse(any(
                PULL in o for o in (path.a, path.kw)))

    def testNoFinishWhenStartFails(self):
        with Stub(bootstrap, 'finish', FakeFinish()) as fake:
            with Stub(bootstrap, 'start', Bomb):
                with self.assertRaises(AssertionError):
                    bootstrap.bootstrap(JOB, REPO, BRANCH, None, ROOT)
        self.assertFalse(fake.called)


    def testFinishWhenBuildFails(self):
        def CallError(*a, **kw):
            raise subprocess.CalledProcessError(1, [], '')
        with Stub(bootstrap, 'finish', FakeFinish()) as fake:
            with Stub(bootstrap, 'call', CallError):
                with self.assertRaises(SystemExit):
                    bootstrap.bootstrap(JOB, REPO, BRANCH, None, ROOT)
        self.assertTrue(fake.called)
        self.assertTrue(fake.result is False)  # Distinguish from None

    def testHappy(self):
        with Stub(bootstrap, 'finish', FakeFinish()) as fake:
            bootstrap.bootstrap(JOB, REPO, BRANCH, None, ROOT)
        self.assertTrue(fake.called)
        self.assertTrue(fake.result)  # Distinguish from None

    def testJobEnv(self):
        """bootstrap sets JOB_NAME."""
        with Stub(os, 'environ', FakeEnviron()) as env:
            bootstrap.bootstrap(JOB, REPO, BRANCH, None, ROOT)
        self.assertIn(bootstrap.JOB_ENV, env)


class RepositoryTest(unittest.TestCase):
    def testKubernetesKubernetes(self):
        expected = 'https://github.com/kubernetes/kubernetes'
        actual = bootstrap.repository('k8s.io/kubernetes')
        self.assertEquals(expected, actual)

    def testKubernetesTestInfra(self):
        expected = 'https://github.com/kubernetes/test-infra'
        actual = bootstrap.repository('k8s.io/test-infra')
        self.assertEquals(expected, actual)

    def testWhatever(self):
        expected = 'https://foo.com/bar'
        actual = bootstrap.repository('foo.com/bar')
        self.assertEquals(expected, actual)



class IntegrationTest(unittest.TestCase):
    REPO = 'hello/world'
    MASTER = 'fake-master-file'
    BRANCH_FILE = 'fake-branch-file'
    PR_FILE = 'fake-pr-file'
    BRANCH = 'another-branch'
    PR = 42
    PR_TAG = bootstrap.pull_ref(PR).strip('+')

    def FakeRepo(self, repo):
        return os.path.join(self.root_github, repo)

    def setUp(self):
        self.boiler = [
            Stub(bootstrap, 'finish', Pass),
            Stub(bootstrap.GSUtil, 'copy_file', Pass),
            Stub(bootstrap, 'repository', self.FakeRepo),
            Stub(bootstrap, 'setup_credentials', Pass),
            Stub(bootstrap, 'setup_logging', FakeLogging()),
            Stub(bootstrap, 'start', Pass),
            Stub(os, 'environ', FakeEnviron(set_job=False)),
        ]
        self.root_github = tempfile.mkdtemp()
        self.root_workspace = tempfile.mkdtemp()
        self.ocwd = os.getcwd()
        repo = self.FakeRepo(self.REPO)
        subprocess.check_call(['git', 'init', repo])
        os.chdir(repo)
        subprocess.check_call(['touch', self.MASTER])
        subprocess.check_call(['git', 'add', self.MASTER])
        subprocess.check_call(['git', 'commit', '-m', 'Initial commit'])
        subprocess.check_call(['git', 'checkout', 'master'])

    def tearDown(self):
        for stub in self.boiler:
            with stub:  # Leaving with restores things
                pass
        os.chdir(self.ocwd)
        subprocess.check_call(['rm', '-rf', self.root_github])
        subprocess.check_call(['rm', '-rf', self.root_workspace])

    def testPr(self):
        subprocess.check_call(['git', 'checkout', 'master'])
        subprocess.check_call(['git', 'checkout', '-b', 'unknown-pr-branch'])
        subprocess.check_call(['git', 'rm', self.MASTER])
        subprocess.check_call(['touch', self.PR_FILE])
        subprocess.check_call(['git', 'add', self.PR_FILE])
        subprocess.check_call(['git', 'commit', '-m', 'Create branch for PR %d' % self.PR])
        subprocess.check_call(['git', 'tag', self.PR_TAG])
        os.chdir('/tmp')
        bootstrap.bootstrap('fake-pr', self.REPO, None, self.PR, self.root_workspace)

    def testBranch(self):
        subprocess.check_call(['git', 'checkout', '-b', self.BRANCH])
        subprocess.check_call(['git', 'rm', self.MASTER])
        subprocess.check_call(['touch', self.BRANCH_FILE])
        subprocess.check_call(['git', 'add', self.BRANCH_FILE])
        subprocess.check_call(['git', 'commit', '-m', 'Create %s' % self.BRANCH])

        os.chdir('/tmp')
        bootstrap.bootstrap('fake-branch', self.REPO, self.BRANCH, None, self.root_workspace)

    def testPr_Bad(self):
        random_pr = 111
        with Stub(bootstrap, 'start', Bomb):
            with Stub(time, 'sleep', Pass):
                with self.assertRaises(subprocess.CalledProcessError):
                    bootstrap.bootstrap('fake-pr', self.REPO, None, random_pr, self.root_workspace)

    def testBranch_Bad(self):
        random_branch = 'something'
        with Stub(bootstrap, 'start', Bomb):
            with Stub(time, 'sleep', Pass):
                with self.assertRaises(subprocess.CalledProcessError):
                    bootstrap.bootstrap('fake-branch', self.REPO, random_branch, None, self.root_workspace)

    def testJobMissing(self):
        with self.assertRaises(OSError):
            bootstrap.bootstrap('this-job-no-exists', self.REPO, 'master', None, self.root_workspace)

    def testJobFails(self):
        with self.assertRaises(SystemExit):
            bootstrap.bootstrap('fake-failure', self.REPO, 'master', None, self.root_workspace)


class JobTest(unittest.TestCase):

    @property
    def jobs(self):
        """[(job, job_path)] sequence"""
        for path, _, filenames in os.walk(
            os.path.dirname(bootstrap.job_script(JOB))):
            for job in filenames:
                job_path = os.path.join(path, job)
                yield job, job_path

    def testBootstrapMaintenanceYaml(self):
        def Check(job, name):
            job_name = 'maintenance-%s' % name
            self.assertEquals(job_name, job.get('job-name'))
            self.assertIn('frequency', job)
            self.assertIn('repo-name', job)
            self.assertIn('.', job['repo-name'])  # Has domain

        self.CheckBootstrapYaml('job-configs/bootstrap-maintenance.yaml', Check)

    def testBootstrapPullYaml(self):
        def Check(job, name):
            job_name = 'pull-%s' % name
            self.assertEquals(job_name, job.get('job-name'))
            self.assertIn('max-total', job)
            self.assertIn('repo-name', job)
            self.assertIn('.', job['repo-name'])  # Has domain

        self.CheckBootstrapYaml('job-configs/kubernetes-jenkins-pull/bootstrap-pull.yaml', Check)

    def testBootstrapCIYaml(self):
        def Check(job, name):
            job_name = 'ci-%s' % name
            self.assertEquals(job_name, job.get('job-name'))
            self.assertIn('branch', job)
            self.assertIn('repo-name', job)
            self.assertIn('.', job['repo-name'])  # Has domain

        self.CheckBootstrapYaml('job-configs/kubernetes-jenkins/bootstrap-ci.yaml', Check)

    def CheckBootstrapYaml(self, path, check):
        with open(os.path.join(
            os.path.dirname(__file__), path)) as fp:
            doc = yaml.safe_load(fp)

        project = None
        for item in doc:
            if not isinstance(item, dict):
                continue
            if not isinstance(item.get('project'), dict):
                continue
            project = item['project']
            self.assertIn('bootstrap-', project.get('name'))
            break
        else:
            self.fail('Could not find bootstrap-pull-jobs project')

        jobs = project.get('suffix')
        if not jobs or not isinstance(jobs, list):
            self.fail('Could not find suffix list in %s' % project)

        for job in jobs:
            # Things to check on all bootstrap jobs
            if not isinstance(job, dict):
                self.fail('suffix items should be dicts', jobs)
            self.assertEquals(1, len(job), job)
            name = job.keys()[0]
            real_job = job[name]

            path = bootstrap.job_script(real_job.get('job-name'))
            self.assertTrue(os.path.isfile(path), path)
            for key, value in real_job.items():
                if not isinstance(value, (basestring, int)):
                    self.fail('Jobs may not contain child objects %s: %s' % (
                        key, value))
                if '{' in str(value):
                    self.fail('Jobs may not contain {expansions}' % (
                        key, value))  # Use simple strings
            # Things to check on specific flavors.
            check(real_job, name)

    def testOnlyJobs(self):
        """Ensure that everything in jobs/ is a valid job name and script."""
        for job, job_path in self.jobs:
            # Jobs should have simple names
            self.assertTrue(re.match(r'[0-9a-z-]+.sh', job), job)
            # Jobs should point to a real, executable file
            # Note: it is easy to forget to chmod +x
            self.assertTrue(os.path.isfile(job_path), job_path)
            self.assertFalse(os.path.islink(job_path), job_path)
            self.assertTrue(os.access(job_path, os.X_OK|os.R_OK), job_path)

    def testAllProjectAreUnique(self):
        projects = collections.defaultdict(list)
        for job, job_path in self.jobs:
            with open(job_path) as fp:
                lines = list(fp)
            for line in lines:
                if 'PROJECT' not in line:
                    continue
                self.assertIn('export', line, line)
                project = re.search(r'PROJECT="([^"]+)"', line).group(1)
                projects[project].append(job)
        duplicates = [(p, j) for p, j in projects.items() if len(j) > 1]
        if duplicates:
            self.fail('Jobs duplicate projects:\n  %s' % (
                '\n  '.join('%s: %s' % t for t in duplicates)))

    def testAllJobsHaveErrExit(self):
        options = {
            'errexit',
            'nounset',
            'pipefail',
            'xtrace',
        }
        for job, job_path in self.jobs:
            with open(job_path) as fp:
                lines = list(fp)
            for option in options:
                expected = 'set -o %s\n' % option
                self.assertIn(
                     expected, lines,
                     '%s not found in %s' % (expected, job_path))



if __name__ == '__main__':
    unittest.main()
