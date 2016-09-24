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

import logging
import json
import os
import re

import defusedxml.ElementTree as ET

import gcs_async
from github import models
import log_parser
import testgrid
import view_base


def parse_junit(xml, filename):
    """Generate failed tests as a series of (name, duration, text, filename) tuples."""
    tree = ET.fromstring(xml)
    if tree.tag == 'testsuite':
        for child in tree:
            name = child.attrib['name']
            time = float(child.attrib['time'])
            for param in child.findall('failure'):
                yield name, time, param.text, filename
    elif tree.tag == 'testsuites':
        for testsuite in tree:
            suite_name = testsuite.attrib['name']
            for child in testsuite.findall('testcase'):
                name = '%s %s' % (suite_name, child.attrib['name'])
                time = float(child.attrib['time'])
                for param in child.findall('failure'):
                    yield name, time, param.text, filename
    else:
        logging.error('unable to find failures, unexpected tag %s', tree.tag)


@view_base.memcache_memoize('build-details://', expires=60 * 60 * 4)
def build_details(build_dir):
    """
    Collect information from a build directory.

    Args:
        build_dir: GCS path containing a build's results.
    Returns:
        started: value from started.json {'version': ..., 'timestamp': ...}
        finished: value from finished.json {'timestamp': ..., 'result': ...}
        failures: list of (name, duration, text) tuples
        build_log: a hilighted portion of errors in the build log. May be None.
    """
    started_fut = gcs_async.read(build_dir + '/started.json')
    finished = gcs_async.read(build_dir + '/finished.json').get_result()
    started = started_fut.get_result()
    if finished and not started:
        started = 'null'
    if started and not finished:
        finished = 'null'
    elif not (started and finished):
        return
    started = json.loads(started)
    finished = json.loads(finished)

    failures = []
    junit_paths = [f.filename for f in view_base.gcs_ls('%s/artifacts' % build_dir)
                   if re.match(r'junit_.*\.xml', os.path.basename(f.filename))]

    junit_futures = {}
    for f in junit_paths:
        junit_futures[gcs_async.read(f)] = f

    for future in junit_futures:
        junit = future.get_result()
        if not junit:
            continue
        failures.extend(parse_junit(junit, junit_futures[future]))
    failures.sort()

    build_log = None
    if finished and finished.get('result') != 'SUCCESS' and len(failures) == 0:
        build_log = gcs_async.read(build_dir + '/build-log.txt').get_result()
        if build_log:
            build_log = log_parser.digest(build_log)
            logging.info('fallback log parser emitted %d lines',
                         build_log.count('\n'))
    return started, finished, failures, build_log


class BuildHandler(view_base.BaseHandler):
    """Show information about a Build and its failing tests."""
    def get(self, prefix, job, build):
        job_dir = '/%s/%s/' % (prefix, job)
        testgrid_query = testgrid.path_to_query(job_dir)
        build_dir = job_dir + build
        details = build_details(build_dir)
        if not details:
            logging.warning('unable to load %s', build_dir)
            self.render(
                'build_404.html',
                dict(build_dir=build_dir, job_dir=job_dir, job=job, build=build))
            self.response.set_status(404)
            return
        started, finished, failures, build_log = details

        if started:
            commit = started['version'].split('+')[-1]
        else:
            commit = None

        pr = None
        pr_digest = None
        if prefix.startswith(view_base.PR_PREFIX):
            pr = os.path.basename(prefix)
            pr_digest = models.GHIssueDigest.get('kubernetes/kubernetes', pr)
        self.render('build.html', dict(
            job_dir=job_dir, build_dir=build_dir, job=job, build=build,
            commit=commit, started=started, finished=finished,
            failures=failures, build_log=build_log, pr=pr, pr_digest=pr_digest,
            testgrid_query=testgrid_query))

@view_base.memcache_memoize('build-list://', expires=60)
def build_list((job_dir, before)):
    '''
    Given a job dir, give a (partial) list of recent build
    finished.jsons.

    Args:
        job_dir: the GCS path holding the jobs
    Returns:
        a list of [(build, finished)]. build is a string like "123",
        finished is either None or a dict of the finished.json.
    '''
    latest_fut = gcs_async.read(job_dir + 'latest-build.txt')
    try:
        if 'pr-logs' in job_dir:
            raise ValueError('bad code path for PR build list')
        # If we have latest-build.txt, we can skip an expensive GCS ls call!
        latest_build = int(latest_fut.get_result())
        if before:
            latest_build = int(before) - 1
        builds = range(latest_build, max(0, latest_build - 40), -1)
    except (ValueError, TypeError):
        fstats = view_base.gcs_ls(job_dir)
        fstats.sort(key=lambda f: view_base.pad_numbers(f.filename),
                    reverse=True)
        builds = [os.path.basename(os.path.dirname(f.filename))
                  for f in fstats if f.is_dir]
        if before and before in builds:
            builds = builds[builds.index(before) + 1:]
        builds = builds[:40]
    finished_futs = {build: gcs_async.read(
        '%s%s/finished.json' % (job_dir, build))
        for build in builds}

    def from_json(fut):
        res = fut.get_result()
        if res:
            return json.loads(res)
    return [(str(build), from_json(finished_futs[build]))
            for build in builds]

class BuildListHandler(view_base.BaseHandler):
    """Show a list of Builds for a Job."""
    def get(self, prefix, job):
        job_dir = '/%s/%s/' % (prefix, job)
        testgrid_query = testgrid.path_to_query(job_dir)
        builds = build_list((job_dir, self.request.get('before')))
        self.render('build_list.html',
                    dict(job=job, job_dir=job_dir,
                         testgrid_query=testgrid_query,
                         builds=builds))


class JobListHandler(view_base.BaseHandler):
    """Show a list of Jobs in a directory."""
    def get(self, prefix):
        jobs_dir = '/%s' % prefix
        fstats = view_base.gcs_ls(jobs_dir)
        fstats.sort()
        self.render('job_list.html', dict(jobs_dir=jobs_dir, fstats=fstats))
