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

import unittest

import classifier


class MergedTest(unittest.TestCase):
    def test_merged(self):
        self.assertEqual(classifier.get_merged(zip('abcd', [
            {'issue': {'n': 1, 'a': 2}},
            {'pull_request': {'n': 2, 'b': 3}},
            {'c': 4},
            {'issue': {'n': 3, 'd': 4},
             'pull_request': {'n': 4, 'e': 5}}
        ])), {'n': 4, 'a': 2, 'b': 3, 'd': 4, 'e': 5})


def diffs_to_events(*diffs):
    events = []
    for diff in diffs:
        label = {'name': diff[1:], 'color': '#fff'}
        if diff[0] == '+':
            action = 'labeled'
        elif diff[0] == '-':
            action = 'unlabeled'
        events.append(('pull_request',
                       {'action': action,
                        'label': label}))
    return events


class LabelsTest(unittest.TestCase):
    def expect_labels(self, events, names):
        labels = classifier.get_labels(events)
        self.assertEqual(sorted(labels.keys()), sorted(names))

    def test_empty(self):
        self.expect_labels([('comment', {'body': 'no labels here'})], [])

    def test_colors(self):
        self.assertEqual(classifier.get_labels(
                [('c', {'issue':
                        {'labels': [{'name': 'foo', 'color': '#abc'}]}
            })]),
            {'foo': '#abc'})

    def test_labeled_action(self):
        self.expect_labels(diffs_to_events('+a'), ['a'])
        self.expect_labels(diffs_to_events('+a', '+a'), ['a'])
        self.expect_labels(diffs_to_events('+a', '-a'), [])
        self.expect_labels(diffs_to_events('+a', '+b', '-c', '-b'), ['a'])

    def test_issue_overrides_action(self):
        labels = [{'name': 'x', 'color': 'y'}]
        self.expect_labels(diffs_to_events('+a') +
            [('other_event', {'issue': {'labels': labels}})], ['x'])

    def test_labeled_action_missing_label(self):
        self.expect_labels([('pull_request', {'action': 'labeled'})], [])


def make_comment_event(num, name, msg='', event='issue_comment',
                       action='created', ts=None):
    return event, {
        'action': action,
        'sender': {'login': name},
        'comment': {
            'id': num,
            'user': {'login': name},
            'body': msg,
            'created_at': ts,
        }
    }


class CalculateTest(unittest.TestCase):
    def test_classify(self):
        # A quick integration test to ensure that all the sub-parts are included.
        # If this test fails, a smaller unit test SHOULD fail as well.
        self.assertEqual(classifier.classify([
                ('pull_request', {
                    'pull_request': {
                        'state': 'open',
                        'user': {'login': 'a'},
                        'assignees': [{'login': 'b'}],
                        'title': 'some fix',
                        'head': {'sha': 'abcdef'},
                        'additions': 1,
                        'deletions': 1,
                    }
                }),
                make_comment_event(1, 'k8s-bot',
                    'failure in https://k8s-gubernator.appspot.com/build/bucket/job/123/'),
                ('pull_request', {
                    'action': 'labeled',
                    'label': {'name': 'release-note-none', 'color': 'orange'},
                })
            ], {'e2e': ['failure', None, 'stuff is broken']}
        ),
        (True, True, ['a', 'b'],
         {
            'author': 'a',
            'assignees': ['b'],
            'additions': 1,
            'deletions': 1,
            'attn': {'a': 'fix tests', 'b': 'needs review'},
            'title': 'some fix',
            'labels': {'release-note-none': 'orange'},
            'head': 'abcdef',
            'needs_rebase': False,
            'status': {'e2e': ['failure', None, 'stuff is broken']},
            'xrefs': ['/bucket/job/123'],
        }))

    def test_distill(self):
        self.assertEqual(classifier.distill_events([
            make_comment_event(1, 'a'),
            make_comment_event(2, 'b'),
            make_comment_event(1, 'a', action='deleted'),
            make_comment_event(3, 'c', event='pull_request_review_comment'),
            make_comment_event(4, 'k8s-bot'),
            ('pull_request', {'action': 'synchronize', 'sender': {'login': 'auth'}}),
            ('pull_request', {'action': 'labeled', 'sender': {'login': 'rev'},
                'label': {'name': 'lgtm'}}),
        ]),
        [
            ('comment', 'b'),
            ('comment', 'c'),
            ('push', 'auth'),
            ('label lgtm', 'rev'),
        ])

    def test_calculate_attention(self):
        def expect(payload, events, expected_attn):
            self.assertEqual(classifier.calculate_attention(events, payload),
                             expected_attn)

        def make_payload(author, assignees=None, labels=None, **kwargs):
            ret = {'author': author, 'assignees': assignees or [], 'labels': labels or []}
            ret.update(kwargs)
            return ret

        expect(make_payload('alpha', needs_rebase=True), [],
            {'alpha': 'needs rebase'})
        expect(make_payload('beta', labels={'release-note-label-needed'}), [],
            {'beta': 'needs release-note label'})
        expect(make_payload('gamma', status={'ci': ['failure', '', '']}), [],
            {'gamma': 'fix tests'})
        expect(make_payload('gamma', status={'ci': ['failure', '', '']}),
            [('comment', 'other')],
            {'gamma': 'address comments'})
        expect(make_payload('delta', ['epsilon']), [],
            {'epsilon': 'needs review'})

        expect(make_payload('alpha', ['alpha']), [('comment', 'other')],
            {'alpha': 'address comments'})

    def test_author_state(self):
        def expect(events, result):
            self.assertEqual(classifier.get_author_state('author', events),
                             result)
        expect([], 'waiting')
        expect([('comment', 'author')], 'waiting')
        expect([('comment', 'other')], 'address comments')
        expect([('comment', 'other'), ('push', 'author')], 'waiting')
        expect([('comment', 'other'), ('comment', 'author')], 'waiting')

    def test_assignee_state(self):
        def expect(events, result):
            self.assertEqual(classifier.get_assignee_state('me', 'author', events),
                             result)
        expect([], 'needs review')
        expect([('comment', 'other')], 'needs review')
        expect([('comment', 'me')], 'waiting')
        expect([('label lgtm', 'other')], 'needs review')
        expect([('label lgtm', 'me')], 'waiting')
        expect([('comment', 'me'), ('push', 'author')], 'needs review')
        expect([('comment', 'me'), ('comment', 'author')], 'needs review')

    def test_xrefs(self):
        def expect(body, comments, result):
            self.assertEqual(result, classifier.get_xrefs(
                [{'comment': c} for c in comments], {'body': body}))
        def fail(path):
            return 'foobar https://k8s-gubernator.appspot.com/build%s asdf' % path
        expect(None, [], [])
        expect('something', [], [])
        expect(fail('/a/b/34/'), [], ['/a/b/34'])
        expect(None, [fail('/a/b/34/')], ['/a/b/34'])
        expect(fail('/a/b/34/'), [fail('/a/b/34]')], ['/a/b/34'])
        expect(fail('/a/b/34/)'), [fail('/a/b/35]')], ['/a/b/34', '/a/b/35'])


class CommentsTest(unittest.TestCase):
    def test_basic(self):
        self.assertEqual(classifier.get_comments([make_comment_event(1, 'aaa', 'msg', ts=2016)]),
            [{'author': 'aaa', 'comment': 'msg', 'timestamp': 2016}])

    def test_deleted(self):
        self.assertEqual(classifier.get_comments([
            make_comment_event(1, 'aaa', 'msg', 2016),
            make_comment_event(1, None, None, None, action='deleted'),
            make_comment_event(2, '', '', '', action='deleted')]),
            [])

    def test_edited(self):
        self.assertEqual(classifier.get_comments([
            make_comment_event(1, 'aaa', 'msg', ts=2016),
            make_comment_event(1, 'aaa', 'redacted', ts=2016.1, action='edited')]),
            [{'author': 'aaa', 'comment': 'redacted', 'timestamp': 2016.1}])


if __name__ == '__main__':
    unittest.main()
