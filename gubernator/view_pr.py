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

import datetime
import json
import logging
import os
import time

import webapp2

import gcs_async
import github.models as ghm
import pull_request
import view_base

PR_PREFIX = view_base.PR_PREFIX


@view_base.memcache_memoize('pr-details://', expires=60 * 3)
def pr_builds(path, pr):
    """
    Get information for all builds run by a PR.

    Args:
        pr: the PR number
    Returns:
        A dictionary of {job: [(build_number, started_json, finished.json)]}
    """
    jobs_dirs_fut = gcs_async.listdirs('%s/%s%s' % (PR_PREFIX, path, pr))
    print '%s/%s%s' % (PR_PREFIX, path, pr)

    def base(path):
        return os.path.basename(os.path.dirname(path))

    jobs_futures = [(job, gcs_async.listdirs(job)) for job in jobs_dirs_fut.get_result()]
    futures = []

    for job, builds_fut in jobs_futures:
        for build in builds_fut.get_result():
            futures.append([
                base(job),
                base(build),
                gcs_async.read('/%sstarted.json' % build),
                gcs_async.read('/%sfinished.json' % build)])

    futures.sort(key=lambda (job, build, s, f): (job, pull_request.pad_numbers(build)),
                 reverse=True)

    jobs = {}
    for job, build, started_fut, finished_fut in futures:
        started = started_fut.get_result()
        finished = finished_fut.get_result()
        if started is not None:
            started = json.loads(started)
        if finished is not None:
            finished = json.loads(finished)
        jobs.setdefault(job, []).append((build, started, finished))

    return jobs


class PRHandler(view_base.BaseHandler):
    """Show a list of test runs for a PR."""
    def get(self, path, pr):
        if path:
            repo = 'kubernetes/%s' % path.strip('/')
        else:
            path = ''
            repo = 'kubernetes/kubernetes'
        builds = pr_builds(path, pr)
        if pr == 'batch':  # truncate batch results to last day
            cutoff = time.time() - 60 * 60 * 24
            builds = {j: [(b, s, f) for b, s, f in bs if not s or s['timestamp'] > cutoff]
                      for j, bs in builds.iteritems()}
        max_builds, headings, rows = pull_request.builds_to_table(builds)
        digest = ghm.GHIssueDigest.get(repo, pr)
        self.render('pr.html', dict(pr=pr, prefix=PR_PREFIX, digest=digest,
            max_builds=max_builds, header=headings, repo=repo, rows=rows, path=path))


class PRDashboard(view_base.BaseHandler):
    def get(self, user=None):
        # pylint: disable=singleton-comparison
        login = self.session.get('user')
        if not user:
            user = login
            if not user:
                self.redirect('/github_auth/pr')
                return
            logging.debug('user=%s', user)
        elif user == 'all':
            user = None
        qs = [ghm.GHIssueDigest.is_pr == True]
        if not self.request.get('all', False):
            qs.append(ghm.GHIssueDigest.is_open == True)
        if user:
            qs.append(ghm.GHIssueDigest.involved == user)
        prs = list(ghm.GHIssueDigest.query(*qs))
        prs.sort(key=lambda x: x.updated_at, reverse=True)

        fmt = self.request.get('format', 'html')
        if fmt == 'json':
            self.response.headers['Content-Type'] = 'application/json'
            def serial(obj):
                if isinstance(obj, datetime.datetime):
                    return obj.isoformat()
                elif isinstance(obj, ghm.GHIssueDigest):
                    # pylint: disable=protected-access
                    keys = ['repo', 'number'] + list(obj._values)
                    return {k: getattr(obj, k) for k in keys}
                raise TypeError
            self.response.write(json.dumps(prs, sort_keys=True, default=serial))
        elif fmt == 'html':
            if user:
                cats = [
                    ('Needs Attention', lambda p: user in p.payload['attn'], ''),
                    ('Incoming', lambda p: user in p.payload['assignees'],
                     'is:open is:pr user:kubernetes assignee:%s' % user),
                    ('Outgoing', lambda p: user == p.payload['author'],
                     'is:open is:pr user:kubernetes author:%s' % user),
                ]
            else:
                cats = [('Open Kubernetes PRs', lambda x: True,
                    'is:open is:pr user:kubernetes')]

            self.render('pr_dashboard.html', dict(prs=prs,
                cats=cats, user=user, login=login))
        else:
            self.abort(406)


class PRBuildLogHandler(webapp2.RequestHandler):
    def get(self, path):
        self.redirect('https://storage.googleapis.com/%s/%s' % (PR_PREFIX, path))
