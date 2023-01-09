/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package approve

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/yaml"

	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/github/fakegithub"
	"k8s.io/test-infra/prow/labels"
	"k8s.io/test-infra/prow/pkg/layeredsets"
	"k8s.io/test-infra/prow/plugins"
	"k8s.io/test-infra/prow/plugins/approve/approvers"
	"k8s.io/test-infra/prow/plugins/ownersconfig"
	"k8s.io/test-infra/prow/repoowners"
)

const prNumber = 1

// TestPluginConfig validates that there are no duplicate repos in the approve plugin config.
func TestPluginConfig(t *testing.T) {
	pa := &plugins.ConfigAgent{}

	b, err := os.ReadFile("../../../config/prow/plugins.yaml")
	if err != nil {
		t.Fatalf("Failed to read plugin config: %v.", err)
	}
	np := &plugins.Configuration{}
	if err := yaml.Unmarshal(b, np); err != nil {
		t.Fatalf("Failed to unmarshal plugin config: %v.", err)
	}
	pa.Set(np)

	orgs := map[string]bool{}
	repos := map[string]bool{}
	for _, config := range pa.Config().Approve {
		for _, entry := range config.Repos {
			if strings.Contains(entry, "/") {
				if repos[entry] {
					t.Errorf("The repo %q is duplicated in the 'approve' plugin configuration.", entry)
				}
				repos[entry] = true
			} else {
				if orgs[entry] {
					t.Errorf("The org %q is duplicated in the 'approve' plugin configuration.", entry)
				}
				orgs[entry] = true
			}
		}
	}
}

func newTestComment(user, body string) github.IssueComment {
	return github.IssueComment{User: github.User{Login: user}, Body: body}
}

func newTestCommentTime(t time.Time, user, body string) github.IssueComment {
	c := newTestComment(user, body)
	c.CreatedAt = t
	return c
}

func newTestReview(user, body string, state github.ReviewState) github.Review {
	return github.Review{User: github.User{Login: user}, Body: body, State: state}
}

func newTestReviewTime(t time.Time, user, body string, state github.ReviewState) github.Review {
	r := newTestReview(user, body, state)
	r.SubmittedAt = t
	return r
}

func newFakeGitHubClient(hasLabel, humanApproved bool, files []string, comments []github.IssueComment, reviews []github.Review) *fakegithub.FakeClient {
	labels := []string{"org/repo#1:lgtm"}
	if hasLabel {
		labels = append(labels, fmt.Sprintf("org/repo#%v:approved", prNumber))
	}
	var changes []github.PullRequestChange
	for _, file := range files {
		changes = append(changes, github.PullRequestChange{Filename: file})
	}
	fgc := fakegithub.NewFakeClient()
	fgc.IssueLabelsAdded = labels
	fgc.PullRequestChanges = map[int][]github.PullRequestChange{prNumber: changes}
	fgc.IssueComments = map[int][]github.IssueComment{prNumber: comments}
	fgc.Reviews = map[int][]github.Review{prNumber: reviews}
	fgc.WasLabelAddedByHumanVal = humanApproved
	return fgc
}

type fakeRepo struct {
	approvers map[string]layeredsets.String
	// directory -> approver
	leafApprovers map[string]sets.String
	// toApprove -> directoryWithOwnersFile
	approverOwners map[string]string
	// dir -> allowed
	autoApproveUnownedSubfolders map[string]bool
	dirDenylist                  []*regexp.Regexp
}

func (fr fakeRepo) Filenames() ownersconfig.Filenames {
	return ownersconfig.FakeFilenames
}

func (fr fakeRepo) Approvers(path string) layeredsets.String {
	return fr.approvers[path]
}
func (fr fakeRepo) LeafApprovers(path string) sets.String {
	return fr.leafApprovers[path]
}
func (fr fakeRepo) FindApproverOwnersForFile(path string) string {
	return fr.approverOwners[path]
}
func (fr fakeRepo) IsNoParentOwners(path string) bool {
	return false
}
func (fr fakeRepo) IsAutoApproveUnownedSubfolders(ownerFilePath string) bool {
	return fr.autoApproveUnownedSubfolders[ownerFilePath]
}
func (fr fakeRepo) TopLevelApprovers() sets.String {
	return nil
}

func (fr fakeRepo) ParseSimpleConfig(path string) (repoowners.SimpleConfig, error) {
	dir := filepath.Dir(path)
	for _, re := range fr.dirDenylist {
		if re.MatchString(dir) {
			return repoowners.SimpleConfig{}, filepath.SkipDir
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return repoowners.SimpleConfig{}, err
	}
	full := new(repoowners.SimpleConfig)
	err = yaml.Unmarshal(b, full)
	return *full, err
}

func (fr fakeRepo) ParseFullConfig(path string) (repoowners.FullConfig, error) {
	dir := filepath.Dir(path)
	for _, re := range fr.dirDenylist {
		if re.MatchString(dir) {
			return repoowners.FullConfig{}, filepath.SkipDir
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return repoowners.FullConfig{}, err
	}
	full := new(repoowners.FullConfig)
	err = yaml.Unmarshal(b, full)
	return *full, err
}

func TestHandle_SimpleApprover(t *testing.T) {
	// This function does not need to test IsApproved, that is tested in approvers/approvers_test.go.

	// includes tests with mixed case usernames
	// includes tests with stale notifications
	tests := []struct {
		name          string
		branch        string
		prBody        string
		hasLabel      bool
		humanApproved bool
		files         []string
		comments      []github.IssueComment
		reviews       []github.Review

		selfApprove         bool
		needsIssue          bool
		lgtmActsAsApprove   bool
		reviewActsAsApprove bool
		githubLinkURL       *url.URL

		expectDelete    bool
		expectComment   bool
		expectedComment string
		expectToggle    bool
	}{

		// breaking cases
		// case: /approve in PR body
		{
			name:                "initial notification (approved)",
			hasLabel:            false,
			files:               []string{"c/c.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">cjwagner</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)~~ [cjwagner]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:                "initial notification (unapproved)",
			hasLabel:            false,
			files:               []string{"c/c.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["cjwagner"]} -->`,
		},
		{
			name:                "no-issue comment",
			hasLabel:            false,
			files:               []string{"a/a.go"},
			comments:            []github.IssueComment{newTestComment("Alice", "stuff\n/approve no-issue \nmore stuff")},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Alice</a>*

Associated issue requirement bypassed by: *<a href="" title="Approved">Alice</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:                "issue provided in PR body",
			prBody:              "some changes that fix #42.\n/assign",
			hasLabel:            false,
			files:               []string{"a/a.go"},
			comments:            []github.IssueComment{newTestComment("Alice", "stuff\n/approve")},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Alice</a>*

Associated issue: *#42*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "non-implicit self approve no-issue",
			hasLabel: false,
			files:    []string{"a/a.go", "c/c.go"},
			comments: []github.IssueComment{
				newTestComment("ALIcE", "stuff\n/approve"),
				newTestComment("cjwagner", "stuff\n/approve no-issue"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:    false,
			expectToggle:    true,
			expectComment:   true,
			expectedComment: "",
		},
		{
			name:     "implicit self approve, missing issue",
			hasLabel: false,
			files:    []string{"a/a.go", "c/c.go"},
			comments: []github.IssueComment{
				newTestComment("ALIcE", "stuff\n/approve"),
				newTestCommentTime(time.Now(), "k8s-ci-robot", `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">ALIcE</a>*, *<a href="#" title="Author self-approved">cjwagner</a>*

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with `+"`/approve no-issue`"+`

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [ALIcE]
- ~~[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)~~ [cjwagner]

Approvers can indicate their approval by writing `+"`/approve`"+` in a comment
Approvers can cancel approval by writing `+"`/approve cancel`"+` in a comment
</details>
<!-- META={"approvers":[]} -->`),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: false,
		},
		{
			name:     "remove approval with /approve cancel",
			hasLabel: true,
			files:    []string{"a/a.go"},
			comments: []github.IssueComment{
				newTestComment("Alice", "/approve no-issue"),
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestComment("Alice", "stuff\n/approve cancel \nmore stuff"),
			},
			reviews:             []github.Review{},
			selfApprove:         true, // no-op test
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">cjwagner</a>*
**Once this PR has been reviewed and has the lgtm label**, please assign alice for approval. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice"]} -->`,
		},
		{
			name:     "remove approval with remove-approve",
			hasLabel: true,
			files:    []string{"a/a.go"},
			comments: []github.IssueComment{
				newTestComment("Alice", "/approve no-issue"),
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestComment("Alice", "stuff\n/remove-approve \nmore stuff"),
			},
			reviews:             []github.Review{},
			selfApprove:         true, // no-op test
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">cjwagner</a>*
**Once this PR has been reviewed and has the lgtm label**, please assign alice for approval. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice"]} -->`,
		},
		{
			name:     "remove approval after sync",
			prBody:   "Changes the thing.\n fixes #42",
			hasLabel: true,
			files:    []string{"a/a.go", "b/b.go"},
			comments: []github.IssueComment{
				newTestComment("bOb", "stuff\n/approve \nblah"),
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         true, // no-op test
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "cancel implicit self approve",
			prBody:   "Changes the thing.\n fixes #42",
			hasLabel: true,
			files:    []string{"c/c.go"},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestCommentTime(time.Now(), "CJWagner", "stuff\n/approve cancel \nmore stuff"),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "remove-approve for implicit self approveS",
			prBody:   "Changes the thing.\n fixes #42",
			hasLabel: true,
			files:    []string{"c/c.go"},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestCommentTime(time.Now(), "CJWagner", "stuff\n/remove-approve \nmore stuff"),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "cancel implicit self approve (with lgtm-after-commit message)",
			prBody:   "Changes the thing.\n fixes #42",
			hasLabel: true,
			files:    []string{"c/c.go"},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestCommentTime(time.Now(), "CJWagner", "/lgtm cancel //PR changed after LGTM, removing LGTM."),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   true,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "up to date, poked by pr sync",
			prBody:   "Finally fixes kubernetes/kubernetes#1\n",
			hasLabel: true,
			files:    []string{"a/a.go", "a/aa.go"},
			comments: []github.IssueComment{
				newTestComment("alice", "stuff\n/approve\nblah"),
				newTestCommentTime(time.Now(), "k8s-ci-robot", `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">alice</a>*

Associated issue: *#1*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [alice]

Approvers can indicate their approval by writing `+"`/approve`"+` in a comment
Approvers can cancel approval by writing `+"`/approve cancel`"+` in a comment
</details>
<!-- META={"approvers":[]} -->`),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: false,
		},
		{
			name:     "out of date, poked by pr sync",
			prBody:   "Finally fixes kubernetes/kubernetes#1\n",
			hasLabel: false,
			files:    []string{"a/a.go", "a/aa.go"}, // previous commits may have been ["b/b.go"]
			comments: []github.IssueComment{
				newTestComment("alice", "stuff\n/approve\nblah"),
				newTestCommentTime(time.Now(), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **NOT APPROVED**\n\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:          "human added approve",
			hasLabel:      true,
			humanApproved: true,
			files:         []string{"a/a.go"},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **NOT APPROVED**\n\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

Approval requirements bypassed by manually added approval.

This pull-request has been approved by:

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice"]} -->`,
		},
		{
			name:     "lgtm means approve",
			prBody:   "This is a great PR that will fix\nlots of things!",
			hasLabel: false,
			files:    []string{"a/a.go", "a/aa.go"},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **NOT APPROVED**\n\nblah"),
				newTestCommentTime(time.Now(), "alice", "stuff\n/lgtm\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   true,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "lgtm does not mean approve",
			prBody:   "This is a great PR that will fix\nlots of things!",
			hasLabel: false,
			files:    []string{"a/a.go", "a/aa.go"},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please assign alice for approval. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**

Approvers can indicate their approval by writing `+"`/approve`"+` in a comment
Approvers can cancel approval by writing `+"`/approve cancel`"+` in a comment
</details>
<!-- META={"approvers":["alice"]} -->`),
				newTestCommentTime(time.Now(), "alice", "stuff\n/lgtm\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: false,
		},
		{
			name:                "approve in review body with empty state",
			hasLabel:            false,
			files:               []string{"a/a.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("Alice", "stuff\n/approve", "")},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Alice</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:                "approved review but reviewActsAsApprove disabled",
			hasLabel:            false,
			files:               []string{"c/c.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("cjwagner", "stuff", github.ReviewStateApproved)},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["cjwagner"]} -->`,
		},
		{
			name:                "approved review with reviewActsAsApprove enabled",
			hasLabel:            false,
			files:               []string{"a/a.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("Alice", "stuff", github.ReviewStateApproved)},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Alice</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "reviews in non-approving state (should not approve)",
			hasLabel: false,
			files:    []string{"c/c.go"},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("cjwagner", "stuff", "COMMENTED"),
				newTestReview("cjwagner", "unsubmitted stuff", "PENDING"),
				newTestReview("cjwagner", "dismissed stuff", "DISMISSED"),
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["cjwagner"]} -->`,
		},
		{
			name:     "review in request changes state means cancel",
			hasLabel: true,
			files:    []string{"c/c.go"},
			comments: []github.IssueComment{
				newTestCommentTime(time.Now().Add(time.Hour), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"), // second
			},
			reviews: []github.Review{
				newTestReviewTime(time.Now(), "cjwagner", "yep", github.ReviewStateApproved),                           // first
				newTestReviewTime(time.Now().Add(time.Hour*2), "cjwagner", "nope", github.ReviewStateChangesRequested), // third
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["cjwagner"]} -->`,
		},
		{
			name:     "dismissed review doesn't cancel prior approval",
			hasLabel: true,
			files:    []string{"a/a.go"},
			comments: []github.IssueComment{
				newTestCommentTime(time.Now().Add(time.Hour), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"), // second
			},
			reviews: []github.Review{
				newTestReviewTime(time.Now(), "Alice", "yep", github.ReviewStateApproved),                         // first
				newTestReviewTime(time.Now().Add(time.Hour*2), "Alice", "dismissed", github.ReviewStateDismissed), // third
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Alice</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "approve cancel command supersedes earlier approved review",
			hasLabel: true,
			files:    []string{"c/c.go"},
			comments: []github.IssueComment{
				newTestCommentTime(time.Now().Add(time.Hour), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"), // second
				newTestCommentTime(time.Now().Add(time.Hour*2), "cjwagner", "stuff\n/approve cancel \nmore stuff"),                  // third
			},
			reviews: []github.Review{
				newTestReviewTime(time.Now(), "cjwagner", "yep", github.ReviewStateApproved), // first
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["cjwagner"]} -->`,
		},
		{
			name:     "remove-approve command supersedes earlier approved review",
			hasLabel: true,
			files:    []string{"c/c.go"},
			comments: []github.IssueComment{
				newTestCommentTime(time.Now().Add(time.Hour), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"), // second
				newTestCommentTime(time.Now().Add(time.Hour*2), "cjwagner", "stuff\n/remove-approve \nmore stuff"),                  // third
			},
			reviews: []github.Review{
				newTestReviewTime(time.Now(), "cjwagner", "yep", github.ReviewStateApproved), // first
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["cjwagner"]} -->`,
		},
		{
			name:     "approve cancel command supersedes simultaneous approved review",
			hasLabel: false,
			files:    []string{"a/a.go", "c/c.go"},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("cjwagner", "/approve cancel", github.ReviewStateApproved),
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner and additionally assign alice for approval. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**
- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice","cjwagner"]} -->`,
		},
		{
			name:     "remove-approve command supersedes simultaneous approved review",
			hasLabel: false,
			files:    []string{"a/a.go", "c/c.go"},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("cjwagner", "/remove-approve", github.ReviewStateApproved),
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner and additionally assign alice for approval. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**
- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice","cjwagner"]} -->`,
		},
		{
			name:                "approve command supersedes simultaneous changes requested review",
			hasLabel:            false,
			files:               []string{"a/a.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("Alice", "/approve", github.ReviewStateChangesRequested)},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Alice</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:                "different branch, initial notification (approved)",
			branch:              "dev",
			hasLabel:            false,
			files:               []string{"c/c.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">cjwagner</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[c/OWNERS](https://github.com/org/repo/blob/dev/c/OWNERS)~~ [cjwagner]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:                "different GitHub link URL",
			branch:              "dev",
			hasLabel:            false,
			files:               []string{"c/c.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.mycorp.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">cjwagner</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[c/OWNERS](https://github.mycorp.com/org/repo/blob/dev/c/OWNERS)~~ [cjwagner]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:                "Approved because of AutoApproveUnownedSubfolders:",
			hasLabel:            true,
			humanApproved:       true,
			files:               []string{"d/new-folder/new_file.go"},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by:

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`,
		},
		{
			name:                "don't suggest to /assign already assigned cjwagner",
			hasLabel:            false,
			files:               []string{"a/a.go", "c/c.go"},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("Alice", "/approve", github.ReviewStateChangesRequested)},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Alice</a>*
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from cjwagner. For more information see [the Kubernetes Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]
- **[c/OWNERS](https://github.com/org/repo/blob/master/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["cjwagner"]} -->`,
		},
	}

	fr := fakeRepo{
		approvers: map[string]layeredsets.String{
			"a":   layeredsets.NewString("alice"),
			"a/b": layeredsets.NewString("alice", "bob"),
			"c":   layeredsets.NewString("cblecker", "cjwagner"),
		},
		leafApprovers: map[string]sets.String{
			"a":   sets.NewString("alice"),
			"a/b": sets.NewString("bob"),
			"c":   sets.NewString("cblecker", "cjwagner"),
		},
		approverOwners: map[string]string{
			"a/a.go":                   "a",
			"a/aa.go":                  "a",
			"a/b/b.go":                 "a/b",
			"c/c.go":                   "c",
			"d/new-folder/new_file.go": "d",
		},
		autoApproveUnownedSubfolders: map[string]bool{
			"d": true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fghc := newFakeGitHubClient(test.hasLabel, test.humanApproved, test.files, test.comments, test.reviews)
			branch := "master"
			if test.branch != "" {
				branch = test.branch
			}

			rsa := !test.selfApprove
			irs := !test.reviewActsAsApprove
			if err := handle(
				logrus.WithField("plugin", "approve"),
				fghc,
				fr,
				config.GitHubOptions{
					LinkURL: test.githubLinkURL,
				},
				&plugins.Approve{
					Repos:               []string{"org/repo"},
					RequireSelfApproval: &rsa,
					IssueRequired:       test.needsIssue,
					LgtmActsAsApprove:   test.lgtmActsAsApprove,
					IgnoreReviewState:   &irs,
					CommandHelpLink:     "https://go.k8s.io/bot-commands",
					PrProcessLink:       "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process",
				},
				&state{
					org:       "org",
					repo:      "repo",
					branch:    branch,
					number:    prNumber,
					body:      test.prBody,
					author:    "cjwagner",
					assignees: []github.User{{Login: "spxtr"}},
				},
			); err != nil {
				t.Errorf("[%s] Unexpected error handling event: %v.", test.name, err)
			}

			if test.expectDelete {
				if len(fghc.IssueCommentsDeleted) != 1 {
					t.Errorf(
						"[%s] Expected 1 notification to be deleted but %d notifications were deleted.",
						test.name,
						len(fghc.IssueCommentsDeleted),
					)
				}
			} else {
				if len(fghc.IssueCommentsDeleted) != 0 {
					t.Errorf(
						"[%s] Expected 0 notifications to be deleted but %d notification was deleted.",
						test.name,
						len(fghc.IssueCommentsDeleted),
					)
				}
			}
			if test.expectComment {
				if len(fghc.IssueCommentsAdded) != 1 {
					t.Errorf(
						"[%s] Expected 1 notification to be added but %d notifications were added.",
						test.name,
						len(fghc.IssueCommentsAdded),
					)
				} else if expect, got := fmt.Sprintf("org/repo#%v:", prNumber)+test.expectedComment, fghc.IssueCommentsAdded[0]; test.expectedComment != "" && got != expect {
					t.Errorf("expected notification differs from actual: %s", cmp.Diff(expect, got))
				}
			} else {
				if len(fghc.IssueCommentsAdded) != 0 {
					t.Errorf(
						"[%s] Expected 0 notifications to be added but %d notification was added.",
						test.name,
						len(fghc.IssueCommentsAdded),
					)
				}
			}

			labelAdded := false
			for _, l := range fghc.IssueLabelsAdded {
				if l == fmt.Sprintf("org/repo#%v:approved", prNumber) {
					if labelAdded {
						t.Errorf("[%s] The approved label was applied to a PR that already had it!", test.name)
					}
					labelAdded = true
				}
			}
			if test.hasLabel {
				labelAdded = false
			}
			toggled := labelAdded
			for _, l := range fghc.IssueLabelsRemoved {
				if l == fmt.Sprintf("org/repo#%v:approved", prNumber) {
					if !test.hasLabel {
						t.Errorf("[%s] The approved label was removed from a PR that doesn't have it!", test.name)
					}
					toggled = true
				}
			}
			if test.expectToggle != toggled {
				t.Errorf(
					"[%s] Expected 'approved' label toggled: %t, but got %t.",
					test.name,
					test.expectToggle,
					toggled,
				)
			}
		})
	}
}

func TestHandle_GranularApprover(t *testing.T) {
	// This function does not need to test IsApproved, that is tested in approvers/approvers_test.go.

	// includes tests with mixed case usernames
	// includes tests with stale notifications
	tests := []struct {
		name            string
		branch          string
		prAuthor        string
		prBody          string
		hasLabel        bool
		humanApproved   bool
		files           []string
		approvers       map[string]layeredsets.String
		leafApprovers   map[string]sets.String
		approversOwners map[string]string
		comments        []github.IssueComment
		reviews         []github.Review

		selfApprove         bool
		needsIssue          bool
		lgtmActsAsApprove   bool
		reviewActsAsApprove bool
		githubLinkURL       *url.URL

		expectDelete    bool
		expectComment   bool
		expectedComment string
		expectToggle    bool
	}{

		// breaking cases
		// case: /approve in PR body
		{
			name:     "initial notification (approved)",
			prAuthor: "ykakarap",
			hasLabel: false,
			files:    []string{"y/y.go"},
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("xtrme", "ykakarap", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">ykakarap</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [ykakarap]</summary>

- <strike>y/y.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "initial notification (unapproved)",
			prAuthor: "ykakarap",
			hasLabel: false,
			files:    []string{"y/y.go"},
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("xtrme", "ykakarap", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **ykakarap**
You can assign the PR to them by writing ` + "`/assign @ykakarap`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **1** files: **0** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[y/OWNERS](https://github.com/org/repo/blob/master/y/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/y">y/</a></strong> (unapproved) </summary>

- y/y.go 

</details>


<!-- META={"approvers":["ykakarap"]} -->`,
		},
		{
			name:     "no-issue comment",
			hasLabel: false,
			prAuthor: "ykakarap",
			files:    []string{"z/z.go"},
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("xtrme", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Xtrme", "/approve"),                            // comment to approve changes
				newTestComment("Zac", "stuff\n/approve no-issue \nmore stuff"), // comment to approve no-issue
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Xtrme</a>*

Associated issue requirement bypassed by: *<a href="" title="Approved">Zac</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [xtrme]</summary>

- <strike>z/z.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},

		{
			name:     "partially approved. no issue",
			hasLabel: false,
			prAuthor: "nikhita",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("ykakarap", "zac"),
				"y/y_test.go": layeredsets.NewString("ykakarap", "zac"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("ykakarap"),
				"y/y_test.go": sets.NewString("ykakarap"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Xtrme", "tests look good\n/approve files */*_test.go"),
				newTestComment("Zac", "changes in y look good\n/approve files y/*"),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Xtrme</a>*, *<a href="" title="Approved">Zac</a>*, *<a href="#" title="Author self-approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **zoe**
You can assign the PR to them by writing ` + "`/assign @zoe`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **3** are approved and **3** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [zac]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (partially approved, need additional approvals) [xtrme]</summary>

- x/x.go 
- <strike>x/x_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/z_test.go 

</details>


<!-- META={"approvers":["zoe"]} -->`,
		},

		{
			name:     "partially approved with approve review state, partial approval should take precedence; reviewActsAsApprove = true.",
			hasLabel: false,
			prAuthor: "nikhita",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("ykakarap", "zac"),
				"y/y_test.go": layeredsets.NewString("ykakarap", "zac"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("ykakarap"),
				"y/y_test.go": sets.NewString("ykakarap"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("Zac", "Something \n/approve files y/*", github.ReviewStateApproved),
			},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*, *<a href="#" title="Author self-approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **xtrme**, **zoe**
You can assign the PR to them by writing ` + "`/assign @xtrme @zoe`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **2** are approved and **4** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [zac]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (unapproved) </summary>

- x/x.go 
- x/x_test.go 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/z_test.go 

</details>


<!-- META={"approvers":["xtrme","zoe"]} -->`,
		},

		{
			name:     "partially approved with approve review state, partial approval should take precedence; reviewActsAsApprove = false.",
			hasLabel: false,
			prAuthor: "nikhita",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("ykakarap", "zac"),
				"y/y_test.go": layeredsets.NewString("ykakarap", "zac"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("ykakarap"),
				"y/y_test.go": sets.NewString("ykakarap"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("Zac", "Something \n/approve files y/*", github.ReviewStateApproved),
			},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*, *<a href="#" title="Author self-approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **xtrme**, **zoe**
You can assign the PR to them by writing ` + "`/assign @xtrme @zoe`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **2** are approved and **4** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [zac]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (unapproved) </summary>

- x/x.go 
- x/x_test.go 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/z_test.go 

</details>


<!-- META={"approvers":["xtrme","zoe"]} -->`,
		},

		{
			name:     "Review acts as approve, an empty approve review should approve all files that the approver has rights over.",
			hasLabel: false,
			prAuthor: "nikhita",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("ykakarap", "zac"),
				"y/y_test.go": layeredsets.NewString("ykakarap", "zac"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("ykakarap"),
				"y/y_test.go": sets.NewString("ykakarap"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("Zac", "", github.ReviewStateApproved),
			},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*, *<a href="#" title="Author self-approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **xtrme**
You can assign the PR to them by writing ` + "`/assign @xtrme`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **4** are approved and **2** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [zac]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [zac]</summary>

- <strike>z/z.go</strike> 
- <strike>z/z_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (unapproved) </summary>

- x/x.go 
- x/x_test.go 

</details>


<!-- META={"approvers":["xtrme"]} -->`,
		},

		{
			name:     "issue provided in PR body",
			prBody:   "some changes that fix #42.\n/assign",
			prAuthor: "ykakarap",
			files:    []string{"z/z.go"},
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("xtrme", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Xtrme", "/approve"), // comment to approve changes
			},
			hasLabel:            false,
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Xtrme</a>*

Associated issue: *#42*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [xtrme]</summary>

- <strike>z/z.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "partially approved. with issue. no issue approval missing",
			hasLabel: false,
			prAuthor: "nikhita",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("ykakarap", "zac"),
				"y/y_test.go": layeredsets.NewString("ykakarap", "zac"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("ykakarap"),
				"y/y_test.go": sets.NewString("ykakarap"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Xtrme", "tests look good\n/approve files */*_test.go"),
				newTestComment("Zac", "changes in y look good\n/approve files y/*"),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Xtrme</a>*, *<a href="" title="Approved">Zac</a>*, *<a href="#" title="Author self-approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **zoe**
You can assign the PR to them by writing ` + "`/assign @zoe`" + ` in a comment when ready.

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **3** are approved and **3** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [zac]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (partially approved, need additional approvals) [xtrme]</summary>

- x/x.go 
- <strike>x/x_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/z_test.go 

</details>


<!-- META={"approvers":["zoe"]} -->`,
		},

		{
			name:     "partially approved. with issue. no issue approval given",
			hasLabel: false,
			prAuthor: "nikhita",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("ykakarap", "zac"),
				"y/y_test.go": layeredsets.NewString("ykakarap", "zac"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("ykakarap"),
				"y/y_test.go": sets.NewString("ykakarap"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Xtrme", "tests look good\n/approve files */*_test.go"),
				newTestComment("Zac", "changes in y look good\n/approve files y/*"),
				newTestComment("Zoe", "/approve no-issue"),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Xtrme</a>*, *<a href="" title="Approved">Zac</a>*, *<a href="#" title="Author self-approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **zoe**
You can assign the PR to them by writing ` + "`/assign @zoe`" + ` in a comment when ready.

Associated issue requirement bypassed by: *<a href="" title="Approved">Zoe</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **3** are approved and **3** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [zac]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (partially approved, need additional approvals) [xtrme]</summary>

- x/x.go 
- <strike>x/x_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/z_test.go 

</details>


<!-- META={"approvers":["zoe"]} -->`,
		},
		{
			name:     "approve single file",
			hasLabel: false,
			prAuthor: "yuvaraj",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("yuvaraj", "zac", "nikhita"),
				"y/y_test.go": layeredsets.NewString("yuvaraj", "zac", "nikhita"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("yuvaraj"),
				"y/y_test.go": sets.NewString("yuvaraj"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("nikhita", "changes in y/ look okay\n/approve files y/y.go"),
				newTestComment("Zoe", "/approve no-issue"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **xtrme**, **zac**
You can assign the PR to them by writing ` + "`/assign @xtrme @zac`" + ` in a comment when ready.

Associated issue requirement bypassed by: *<a href="" title="Approved">Zoe</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **1** are approved and **5** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**
- **[y/OWNERS](https://github.com/org/repo/blob/master/y/OWNERS)**
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (unapproved) </summary>

- x/x.go 
- x/x_test.go 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/y">y/</a></strong> (partially approved, need additional approvals) [nikhita]</summary>

- y/y_test.go 
- <strike>y/y.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/z_test.go 

</details>


<!-- META={"approvers":["xtrme","zac"]} -->`,
		},

		{
			name:     "approve only a directory",
			hasLabel: false,
			prAuthor: "yuvaraj",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("yuvaraj", "zac", "nikhita"),
				"y/y_test.go": layeredsets.NewString("yuvaraj", "zac", "nikhita"),
				"z/z.go":      layeredsets.NewString("zac", "zoe"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("yuvaraj"),
				"y/y_test.go": sets.NewString("yuvaraj"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("nikhita", "changes in y/ look okay\n/approve files y/*"),
				newTestComment("Zoe", "/approve no-issue"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">nikhita</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **xtrme**, **zac**
You can assign the PR to them by writing ` + "`/assign @xtrme @zac`" + ` in a comment when ready.

Associated issue requirement bypassed by: *<a href="" title="Approved">Zoe</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **6** files: **2** are approved and **4** are unapproved.  

Needs approval from approvers in these files:
- **[x/OWNERS](https://github.com/org/repo/blob/master/x/OWNERS)**
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [nikhita]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/x">x/</a></strong> (unapproved) </summary>

- x/x.go 
- x/x_test.go 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/z_test.go 

</details>


<!-- META={"approvers":["xtrme","zac"]} -->`,
		},

		{
			name:     "files approved individually. missing issue approval",
			hasLabel: false,
			prAuthor: "yuvaraj",
			files:    []string{"x/x.go", "x/x_test.go", "y/y.go", "y/y_test.go", "z/z.go", "z/z_test.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go":      layeredsets.NewString("xtrme"),
				"x/x_test.go": layeredsets.NewString("xtrme"),
				"y/y.go":      layeredsets.NewString("yuvaraj", "zac", "nikhita"),
				"y/y_test.go": layeredsets.NewString("yuvaraj", "zac", "nikhita"),
				"z/z.go":      layeredsets.NewString("zac", "zoe", "nikhita"),
				"z/z_test.go": layeredsets.NewString("zac", "zoe", "nikhita"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go":      sets.NewString("xtrme"),
				"x/x_test.go": sets.NewString("xtrme"),
				"y/y.go":      sets.NewString("yuvaraj"),
				"y/y_test.go": sets.NewString("yuvaraj"),
				"z/z.go":      sets.NewString("zac", "zoe"),
				"z/z_test.go": sets.NewString("zac", "zoe"),
			},
			approversOwners: map[string]string{
				"x/x.go":      "x",
				"x/x_test.go": "x",
				"y/y.go":      "y",
				"y/y_test.go": "y",
				"z/z.go":      "z",
				"z/z_test.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("xtrme", "change in x are good\n/approve files x/*"),
				newTestComment("yuvaraj", "changes in y/ look okay\n/approve files y/*"),
				newTestComment("nikhita", "/approve files z/z.go"),
				newTestComment("zoe", "/approve files z/z_test.go"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">nikhita</a>*, *<a href="" title="Approved">xtrme</a>*, *<a href="" title="Approved">zoe</a>*, *<a href="" title="Author self-approved">yuvaraj</a>*

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **6** files: **6** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/x">x/</a></strike> (approved) [xtrme]</summary>

- <strike>x/x.go</strike> 
- <strike>x/x_test.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [yuvaraj]</summary>

- <strike>y/y.go</strike> 
- <strike>y/y_test.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [nikhita,zoe]</summary>

- <strike>z/z.go</strike> 
- <strike>z/z_test.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},

		{
			name:     "non-implicit self approve no-issue",
			hasLabel: false,
			prAuthor: "zac",
			files:    []string{"x/x.go", "z/z.go"},
			approvers: map[string]layeredsets.String{
				"x/x.go": layeredsets.NewString("xtrme"),
				"z/z.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"x/x.go": sets.NewString("xtrme"),
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"x/x.go": "x",
				"z/z.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Xtrme", "/approve"),        // comment to approve changes
				newTestComment("Zac", "/approve"),          // comment to approve changes
				newTestComment("Zac", "/approve no-issue"), // comment to approve no-issue
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:    false,
			expectToggle:    true,
			expectComment:   true,
			expectedComment: "",
		},
		{
			name:     "implicit self approve, missing issue",
			hasLabel: false,
			files:    []string{"y/y.go", "z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("ykakarap"),
				"z/z.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
				"z/z.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Zac", "stuff\n/approve"),
				newTestCommentTime(time.Now(), "k8s-ci-robot", `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*, *<a href="#" title="Author self-approved">ykakarap</a>*

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with `+"`/approve no-issue`"+`

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **2** files: **2** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [ykakarap]</summary>

- <strike>y/y.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [zac]</summary>

- <strike>z/z.go</strike> 

</details>


<!-- META={"approvers":[]} -->`),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: false,
		},
		{
			name:     "remove approval with /approve cancel",
			hasLabel: true,
			files:    []string{"y/y.go", "z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("ykakarap"),
				"z/z.go": layeredsets.NewString("xtrme", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
				"z/z.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Xtrme", "/approve"),
				newTestComment("Xtrme", "/approve no-issue"),
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestComment("Xtrme", "stuff\n/approve cancel \nmore stuff"),
			},
			reviews:             []github.Review{},
			selfApprove:         true, // no-op test
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">ykakarap</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **zac**
You can assign the PR to them by writing ` + "`/assign @zac`" + ` in a comment when ready.

Associated issue requirement bypassed by: *<a href="" title="Approved">Xtrme</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **2** files: **1** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [ykakarap]</summary>

- <strike>y/y.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 

</details>


<!-- META={"approvers":["zac"]} -->`,
		},
		{
			name:     "remove approval after sync",
			prBody:   "Changes the thing.\n fixes #42",
			hasLabel: true,
			files:    []string{"y/y.go", "y/z/z.go"},
			prAuthor: "xtrme",
			approvers: map[string]layeredsets.String{
				"y/y.go":   layeredsets.NewString("ykakarap"),
				"y/z/z.go": layeredsets.NewString("ykakarap", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go":   sets.NewString("ykakarap"),
				"y/z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"y/y.go":   "y",
				"y/z/z.go": "y/z",
			},
			comments: []github.IssueComment{
				newTestComment("ZaC", "stuff\n/approve \nblah"),
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         true, // no-op test
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "cancel implicit self approve",
			prBody:   "Changes the thing.\n fixes #42",
			hasLabel: true,
			files:    []string{"y/y.go", "y/z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go":   layeredsets.NewString("ykakarap"),
				"y/z/z.go": layeredsets.NewString("ykakarap", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go":   sets.NewString("ykakarap"),
				"y/z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"y/y.go":   "y",
				"y/z/z.go": "y/z",
			},
			comments: []github.IssueComment{
				newTestComment("ykakarap", "/approve no-issue"),
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestCommentTime(time.Now(), "ykakarap", "stuff\n/approve cancel \nmore stuff"),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "cancel implicit self approve (with lgtm-after-commit message)",
			prBody:   "Changes the thing.\n fixes #42",
			hasLabel: true,
			files:    []string{"y/y.go", "y/z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go":   layeredsets.NewString("ykakarap"),
				"y/z/z.go": layeredsets.NewString("ykakarap", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go":   sets.NewString("ykakarap"),
				"y/z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"y/y.go":   "y",
				"y/z/z.go": "y/z",
			},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"),
				newTestCommentTime(time.Now(), "ykakarap", "/lgtm cancel //PR changed after LGTM, removing LGTM."),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          true,
			lgtmActsAsApprove:   true,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "up to date, poked by pr sync",
			prBody:   "Finally fixes kubernetes/kubernetes#1\n",
			hasLabel: true,
			files:    []string{"z/z.go", "z/zz.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go":  layeredsets.NewString("zac"),
				"z/zz.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go":  sets.NewString("zac"),
				"z/zz.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go":  "z",
				"z/zz.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("Zac", "stuff\n/approve\nblah"),
				newTestCommentTime(time.Now(), "k8s-ci-robot", `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*

Associated issue: *#1*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **2** files: **2** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [zac]</summary>

- <strike>z/z.go</strike> 
- <strike>z/zz.go</strike> 

</details>


<!-- META={"approvers":[]} -->`),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: false,
		},
		{
			name:     "out of date, poked by pr sync",
			prBody:   "Finally fixes kubernetes/kubernetes#1\n",
			hasLabel: false,
			files:    []string{"z/z.go", "z/zz.go"}, // previous commits may have been ["b/b.go"]
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go":  layeredsets.NewString("zac"),
				"z/zz.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go":  sets.NewString("zac"),
				"z/zz.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go":  "z",
				"z/zz.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("zac", "stuff\n/approve\nblah"),
				newTestCommentTime(time.Now(), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **NOT APPROVED**\n\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          true,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:          "human added approve",
			hasLabel:      true,
			humanApproved: true,
			files:         []string{"z/z.go", "z/zz.go"},
			prAuthor:      "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go":  layeredsets.NewString("zac"),
				"z/zz.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go":  sets.NewString("zac"),
				"z/zz.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go":  "z",
				"z/zz.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **NOT APPROVED**\n\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

**Approval requirements bypassed by manually added approval.**

This pull-request has been approved by:

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **2** files: **0** are approved and **2** are unapproved.  

Needs approval from approvers in these files:
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/zz.go 

</details>


<!-- META={"approvers":["zac"]} -->`,
		},
		{
			name:     "lgtm means approve",
			prBody:   "This is a great PR that will fix\nlots of things!",
			hasLabel: false,
			files:    []string{"z/z.go", "z/zz.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go":  layeredsets.NewString("zac"),
				"z/zz.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go":  sets.NewString("zac"),
				"z/zz.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go":  "z",
				"z/zz.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **NOT APPROVED**\n\nblah"),
				newTestCommentTime(time.Now(), "zac", "stuff\n/lgtm\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   true,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
		},
		{
			name:     "lgtm does not mean approve",
			prBody:   "This is a great PR that will fix\nlots of things!",
			hasLabel: false,
			files:    []string{"z/z.go", "z/zz.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go":  layeredsets.NewString("zac"),
				"z/zz.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go":  sets.NewString("zac"),
				"z/zz.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go":  "z",
				"z/zz.go": "z",
			},
			comments: []github.IssueComment{
				newTestComment("k8s-ci-robot", `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">ykakarap</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **zac**
You can assign the PR to them by writing `+"`/assign @zac`"+` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **2** files: **0** are approved and **2** are unapproved.  

Needs approval from approvers in these files:
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing `+"`/approve`"+` in a comment
Approvers can also choose to approve only specific files by writing `+"`/approve files <path-to-file>`"+` in a comment
Approvers can cancel approval by writing `+"`/approve cancel`"+` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 
- z/zz.go 

</details>


<!-- META={"approvers":["zac"]} -->`),
				newTestCommentTime(time.Now(), "zac", "stuff\n/lgtm\nblah"),
			},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: false,
		},
		{
			name:     "approve in review body with empty state",
			hasLabel: false,
			files:    []string{"z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("Zac", "stuff\n/approve", "")},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [zac]</summary>

- <strike>z/z.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "approved review but reviewActsAsApprove disabled",
			hasLabel: false,
			files:    []string{"z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("Zac", "stuff", github.ReviewStateApproved)},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **zac**
You can assign the PR to them by writing ` + "`/assign @zac`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **1** files: **0** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 

</details>


<!-- META={"approvers":["zac"]} -->`,
		},
		{
			name:     "approved review with reviewActsAsApprove enabled",
			hasLabel: false,
			files:    []string{"z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("Zac", "stuff", github.ReviewStateApproved)},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [zac]</summary>

- <strike>z/z.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "reviews in non-approving state (should not approve)",
			hasLabel: false,
			files:    []string{"z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("xtrme", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("xtrme", "stuff", "COMMENTED"),
				newTestReview("xtrme", "unsubmitted stuff", "PENDING"),
				newTestReview("xtrme", "dismissed stuff", "DISMISSED"),
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **zac**
You can assign the PR to them by writing ` + "`/assign @zac`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **1** files: **0** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 

</details>


<!-- META={"approvers":["zac"]} -->`,
		},
		{
			name:     "review in request changes state means cancel",
			hasLabel: true,
			files:    []string{"z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("ykakarap", "zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments: []github.IssueComment{
				newTestCommentTime(time.Now().Add(time.Hour), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"), // second
			},
			reviews: []github.Review{
				newTestReviewTime(time.Now(), "ykakarap", "yep", github.ReviewStateApproved),                           // first
				newTestReviewTime(time.Now().Add(time.Hour*2), "ykakarap", "nope", github.ReviewStateChangesRequested), // third
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **ykakarap**
You can assign the PR to them by writing ` + "`/assign @ykakarap`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **1** files: **0** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[z/OWNERS](https://github.com/org/repo/blob/master/z/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/z">z/</a></strong> (unapproved) </summary>

- z/z.go 

</details>


<!-- META={"approvers":["ykakarap"]} -->`,
		},
		{
			name:     "dismissed review doesn't cancel prior approval",
			hasLabel: true,
			files:    []string{"z/z.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"z/z.go": layeredsets.NewString("zac"),
			},
			leafApprovers: map[string]sets.String{
				"z/z.go": sets.NewString("zac"),
			},
			approversOwners: map[string]string{
				"z/z.go": "z",
			},
			comments: []github.IssueComment{
				newTestCommentTime(time.Now().Add(time.Hour), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"), // second
			},
			reviews: []github.Review{
				newTestReviewTime(time.Now(), "Zac", "yep", github.ReviewStateApproved),                         // first
				newTestReviewTime(time.Now().Add(time.Hour*2), "Zac", "dismissed", github.ReviewStateDismissed), // third
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Approved">Zac</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/z">z/</a></strike> (approved) [zac]</summary>

- <strike>z/z.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "approve cancel command supersedes earlier approved review",
			hasLabel: true,
			files:    []string{"y/y.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("ykakarap"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
			},
			comments: []github.IssueComment{
				newTestCommentTime(time.Now().Add(time.Hour), "k8s-ci-robot", "[APPROVALNOTIFIER] This PR is **APPROVED**\n\nblah"), // second
				newTestCommentTime(time.Now().Add(time.Hour*2), "ykakarap", "stuff\n/approve cancel \nmore stuff"),                  // third
			},
			reviews: []github.Review{
				newTestReviewTime(time.Now(), "ykakarap", "yep", github.ReviewStateApproved), // first
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  true,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **ykakarap**
You can assign the PR to them by writing ` + "`/assign @ykakarap`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **1** files: **0** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[y/OWNERS](https://github.com/org/repo/blob/master/y/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/y">y/</a></strong> (unapproved) </summary>

- y/y.go 

</details>


<!-- META={"approvers":["ykakarap"]} -->`,
		},
		{
			name:     "approve cancel command supersedes simultaneous approved review",
			hasLabel: false,
			files:    []string{"y/y.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("ykakarap"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
			},
			comments: []github.IssueComment{},
			reviews: []github.Review{
				newTestReview("ykakarap", "/approve cancel", github.ReviewStateApproved),
			},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  false,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by:
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **ykakarap**
You can assign the PR to them by writing ` + "`/assign @ykakarap`" + ` in a comment when ready.

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **1** files: **0** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[y/OWNERS](https://github.com/org/repo/blob/master/y/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/y">y/</a></strong> (unapproved) </summary>

- y/y.go 

</details>


<!-- META={"approvers":["ykakarap"]} -->`,
		},
		{
			name:     "approve command supersedes simultaneous changes requested review",
			hasLabel: false,
			files:    []string{"y/y.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("ykakarap"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{newTestReview("ykakarap", "/approve", github.ReviewStateChangesRequested)},
			selfApprove:         false,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: true,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="" title="Author self-approved">ykakarap</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/y">y/</a></strike> (approved) [ykakarap]</summary>

- <strike>y/y.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "different branch, initial notification (approved)",
			branch:   "dev",
			hasLabel: false,
			files:    []string{"y/y.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("ykakarap"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">ykakarap</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/dev/y">y/</a></strike> (approved) [ykakarap]</summary>

- <strike>y/y.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
		{
			name:     "different GitHub link URL",
			branch:   "dev",
			hasLabel: false,
			files:    []string{"y/y.go"},
			prAuthor: "ykakarap",
			approvers: map[string]layeredsets.String{
				"y/y.go": layeredsets.NewString("ykakarap"),
			},
			leafApprovers: map[string]sets.String{
				"y/y.go": sets.NewString("ykakarap"),
			},
			approversOwners: map[string]string{
				"y/y.go": "y",
			},
			comments:            []github.IssueComment{},
			reviews:             []github.Review{},
			selfApprove:         true,
			needsIssue:          false,
			lgtmActsAsApprove:   false,
			reviewActsAsApprove: false,
			githubLinkURL:       &url.URL{Scheme: "https", Host: "github.mycorp.com"},

			expectDelete:  false,
			expectToggle:  true,
			expectComment: true,
			expectedComment: `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="#" title="Author self-approved">ykakarap</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **1** files: **1** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.mycorp.com/org/repo/blob/dev/y">y/</a></strike> (approved) [ykakarap]</summary>

- <strike>y/y.go</strike> 

</details>


<!-- META={"approvers":[]} -->`,
		},
	}

	for _, test := range tests {
		fr := fakeRepo{
			approvers:      test.approvers,
			leafApprovers:  test.leafApprovers,
			approverOwners: test.approversOwners,
		}
		fghc := newFakeGitHubClient(test.hasLabel, test.humanApproved, test.files, test.comments, test.reviews)
		branch := "master"
		if test.branch != "" {
			branch = test.branch
		}

		rsa := !test.selfApprove
		irs := !test.reviewActsAsApprove
		if err := handle(
			logrus.WithField("plugin", "approve"),
			fghc,
			fr,
			config.GitHubOptions{
				LinkURL: test.githubLinkURL,
			},
			&plugins.Approve{
				Repos:               []string{"org/repo"},
				RequireSelfApproval: &rsa,
				IssueRequired:       test.needsIssue,
				LgtmActsAsApprove:   test.lgtmActsAsApprove,
				IgnoreReviewState:   &irs,
				GranularApproval:    true,
				CommandHelpLink:     "https://go.k8s.io/bot-commands",
				PrProcessLink:       "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process",
			},
			&state{
				org:       "org",
				repo:      "repo",
				branch:    branch,
				number:    prNumber,
				body:      test.prBody,
				author:    test.prAuthor,
				assignees: []github.User{{Login: "spxtr"}},
			},
		); err != nil {
			t.Errorf("[%s] Unexpected error handling event: %v.", test.name, err)
		}

		if test.expectDelete {
			if len(fghc.IssueCommentsDeleted) != 1 {
				t.Errorf(
					"[%s] Expected 1 notification to be deleted but %d notifications were deleted.",
					test.name,
					len(fghc.IssueCommentsDeleted),
				)
			}
		} else {
			if len(fghc.IssueCommentsDeleted) != 0 {
				t.Errorf(
					"[%s] Expected 0 notifications to be deleted but %d notification was deleted.",
					test.name,
					len(fghc.IssueCommentsDeleted),
				)
			}
		}
		//TODO: Remove this debug statement
		//fmt.Println(fghc.IssueCommentsAdded[0])
		if test.expectComment {
			if len(fghc.IssueCommentsAdded) != 1 {
				t.Errorf(
					"[%s] Expected 1 notification to be added but %d notifications were added.",
					test.name,
					len(fghc.IssueCommentsAdded),
				)
			} else if expect, got := fmt.Sprintf("org/repo#%v:", prNumber)+test.expectedComment, fghc.IssueCommentsAdded[0]; test.expectedComment != "" && got != expect {
				t.Errorf(
					"[%s] Expected the created notification to be:\n%s\n\nbut got:\n%s\n\n",
					test.name,
					expect,
					got,
				)
			}
		} else {
			if len(fghc.IssueCommentsAdded) != 0 {
				t.Errorf(
					"[%s] Expected 0 notifications to be added but %d notification was added.",
					test.name,
					len(fghc.IssueCommentsAdded),
				)
			}
		}

		labelAdded := false
		for _, l := range fghc.IssueLabelsAdded {
			if l == fmt.Sprintf("org/repo#%v:approved", prNumber) {
				if labelAdded {
					t.Errorf("[%s] The approved label was applied to a PR that already had it!", test.name)
				}
				labelAdded = true
			}
		}
		if test.hasLabel {
			labelAdded = false
		}
		toggled := labelAdded
		for _, l := range fghc.IssueLabelsRemoved {
			if l == fmt.Sprintf("org/repo#%v:approved", prNumber) {
				if !test.hasLabel {
					t.Errorf("[%s] The approved label was removed from a PR that doesn't have it!", test.name)
				}
				toggled = true
			}
		}
		if test.expectToggle != toggled {
			t.Errorf(
				"[%s] Expected 'approved' label toggled: %t, but got %t.",
				test.name,
				test.expectToggle,
				toggled,
			)
		}
	}
}

// TODO: cache approvers 'GetFilesApprovers' and 'GetCCs' since these are called repeatedly and are
// expensive.

type fakeOwnersClient struct{}

func (foc fakeOwnersClient) LoadRepoOwners(org, repo, base string) (repoowners.RepoOwner, error) {
	return fakeRepoOwners{}, nil
}

type fakeRepoOwners struct {
	fakeRepo
}

func (fro fakeRepoOwners) AllOwners() sets.String {
	return sets.String{}
}

func (fro fakeRepoOwners) FindLabelsForFile(path string) sets.String {
	return sets.NewString()
}

func (fro fakeRepoOwners) FindReviewersOwnersForFile(path string) string {
	return ""
}

func (fro fakeRepoOwners) LeafReviewers(path string) sets.String {
	return sets.NewString()
}

func (fro fakeRepoOwners) Reviewers(path string) layeredsets.String {
	return layeredsets.NewString()
}

func (fro fakeRepoOwners) RequiredReviewers(path string) sets.String {
	return sets.NewString()
}

func TestHandleGenericComment(t *testing.T) {
	tests := []struct {
		name              string
		commentEvent      github.GenericCommentEvent
		lgtmActsAsApprove bool
		expectHandle      bool
		expectState       *state
	}{
		{
			name: "valid approve command",
			commentEvent: github.GenericCommentEvent{
				Action: github.GenericCommentActionCreated,
				IsPR:   true,
				Body:   "/approve",
				Number: 1,
				User: github.User{
					Login: "author",
				},
				IssueBody: "Fix everything",
				IssueAuthor: github.User{
					Login: "P.R. Author",
				},
			},
			expectHandle: true,
			expectState: &state{
				org:       "org",
				repo:      "repo",
				branch:    "branch",
				number:    1,
				body:      "Fix everything",
				author:    "P.R. Author",
				assignees: nil,
				htmlURL:   "",
			},
		},
		{
			name: "not comment created",
			commentEvent: github.GenericCommentEvent{
				Action: github.GenericCommentActionEdited,
				IsPR:   true,
				Body:   "/approve",
				Number: 1,
				User: github.User{
					Login: "author",
				},
			},
			expectHandle: false,
		},
		{
			name: "not PR",
			commentEvent: github.GenericCommentEvent{
				Action: github.GenericCommentActionEdited,
				IsPR:   false,
				Body:   "/approve",
				Number: 1,
				User: github.User{
					Login: "author",
				},
			},
			expectHandle: false,
		},
		{
			name: "closed PR",
			commentEvent: github.GenericCommentEvent{
				Action: github.GenericCommentActionCreated,
				IsPR:   true,
				Body:   "/approve",
				Number: 1,
				User: github.User{
					Login: "author",
				},
				IssueState: "closed",
			},
			expectHandle: false,
		},
		{
			name: "no approve command",
			commentEvent: github.GenericCommentEvent{
				Action: github.GenericCommentActionCreated,
				IsPR:   true,
				Body:   "stuff",
				Number: 1,
				User: github.User{
					Login: "author",
				},
			},
			expectHandle: false,
		},
		{
			name: "lgtm without lgtmActsAsApprove",
			commentEvent: github.GenericCommentEvent{
				Action: github.GenericCommentActionCreated,
				IsPR:   true,
				Body:   "/lgtm",
				Number: 1,
				User: github.User{
					Login: "author",
				},
			},
			expectHandle: false,
		},
		{
			name: "lgtm with lgtmActsAsApprove",
			commentEvent: github.GenericCommentEvent{
				Action: github.GenericCommentActionCreated,
				IsPR:   true,
				Body:   "/lgtm",
				Number: 1,
				User: github.User{
					Login: "author",
				},
			},
			lgtmActsAsApprove: true,
			expectHandle:      true,
		},
	}

	var handled bool
	var gotState *state
	handleFunc = func(log *logrus.Entry, ghc githubClient, repo approvers.Repo, githubConfig config.GitHubOptions, opts *plugins.Approve, pr *state) error {
		gotState = pr
		handled = true
		return nil
	}
	defer func() {
		handleFunc = handle
	}()

	repo := github.Repo{
		Owner: github.User{
			Login: "org",
		},
		Name: "repo",
	}
	pr := github.PullRequest{
		Base: github.PullRequestBranch{
			Ref: "branch",
		},
		Number: 1,
	}
	fghc := fakegithub.NewFakeClient()
	fghc.PullRequests = map[int]*github.PullRequest{1: &pr}

	for _, test := range tests {
		test.commentEvent.Repo = repo
		githubConfig := config.GitHubOptions{
			LinkURL: &url.URL{
				Scheme: "https",
				Host:   "github.com",
			},
		}
		config := &plugins.Configuration{}
		config.Approve = append(config.Approve, plugins.Approve{
			Repos:             []string{test.commentEvent.Repo.Owner.Login},
			LgtmActsAsApprove: test.lgtmActsAsApprove,
		})
		err := handleGenericComment(
			logrus.WithField("plugin", "approve"),
			fghc,
			fakeOwnersClient{},
			githubConfig,
			config,
			&test.commentEvent,
		)

		if test.expectHandle && !handled {
			t.Errorf("%s: expected call to handleFunc, but it wasn't called", test.name)
		}

		if !test.expectHandle && handled {
			t.Errorf("%s: expected no call to handleFunc, but it was called", test.name)
		}

		if test.expectState != nil && !reflect.DeepEqual(test.expectState, gotState) {
			t.Errorf("%s: expected PR state to equal: %#v, but got: %#v", test.name, test.expectState, gotState)
		}

		if err != nil {
			t.Errorf("%s: error calling handleGenericComment: %v", test.name, err)
		}
		handled = false
	}
}

// GitHub webhooks send state as lowercase, so force it to lowercase here.
func stateToLower(s github.ReviewState) github.ReviewState {
	return github.ReviewState(strings.ToLower(string(s)))
}

func TestHandleReview(t *testing.T) {
	tests := []struct {
		name                string
		reviewEvent         github.ReviewEvent
		lgtmActsAsApprove   bool
		reviewActsAsApprove bool
		expectHandle        bool
		expectState         *state
	}{
		{
			name: "approved state",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionSubmitted,
				Review: github.Review{
					Body: "looks good",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStateApproved),
				},
			},
			reviewActsAsApprove: true,
			expectHandle:        true,
			expectState: &state{
				org:       "org",
				repo:      "repo",
				branch:    "branch",
				number:    1,
				body:      "Fix everything",
				author:    "P.R. Author",
				assignees: nil,
				htmlURL:   "",
			},
		},
		{
			name: "changes requested state",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionSubmitted,
				Review: github.Review{
					Body: "looks bad",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStateChangesRequested),
				},
			},
			reviewActsAsApprove: true,
			expectHandle:        true,
		},
		{
			name: "pending review state",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionSubmitted,
				Review: github.Review{
					Body: "looks good",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStatePending),
				},
			},
			reviewActsAsApprove: true,
			expectHandle:        false,
		},
		{
			name: "edited review",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionEdited,
				Review: github.Review{
					Body: "looks good",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStateApproved),
				},
			},
			reviewActsAsApprove: true,
			expectHandle:        false,
		},
		{
			name: "dismissed review",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionDismissed,
				Review: github.Review{
					Body: "looks good",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStateDismissed),
				},
			},
			reviewActsAsApprove: true,
			expectHandle:        true,
		},
		{
			name: "approve command",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionSubmitted,
				Review: github.Review{
					Body: "/approve",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStateApproved),
				},
			},
			reviewActsAsApprove: true,
			expectHandle:        false,
		},
		{
			name: "lgtm command",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionSubmitted,
				Review: github.Review{
					Body: "/lgtm",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStateApproved),
				},
			},
			lgtmActsAsApprove:   true,
			reviewActsAsApprove: true,
			expectHandle:        false,
		},
		{
			name: "feature disabled",
			reviewEvent: github.ReviewEvent{
				Action: github.ReviewActionSubmitted,
				Review: github.Review{
					Body: "looks good",
					User: github.User{
						Login: "author",
					},
					State: stateToLower(github.ReviewStateApproved),
				},
			},
			reviewActsAsApprove: false,
			expectHandle:        false,
		},
	}

	var handled bool
	var gotState *state
	handleFunc = func(log *logrus.Entry, ghc githubClient, repo approvers.Repo, config config.GitHubOptions, opts *plugins.Approve, pr *state) error {
		gotState = pr
		handled = true
		return nil
	}
	defer func() {
		handleFunc = handle
	}()

	repo := github.Repo{
		Owner: github.User{
			Login: "org",
		},
		Name: "repo",
	}
	pr := github.PullRequest{
		User: github.User{
			Login: "P.R. Author",
		},
		Base: github.PullRequestBranch{
			Ref: "branch",
		},
		Number: 1,
		Body:   "Fix everything",
	}
	fghc := fakegithub.NewFakeClient()
	fghc.PullRequests = map[int]*github.PullRequest{1: &pr}

	for _, test := range tests {
		test.reviewEvent.Repo = repo
		test.reviewEvent.PullRequest = pr
		githubConfig := config.GitHubOptions{
			LinkURL: &url.URL{
				Scheme: "https",
				Host:   "github.com",
			},
		}
		config := &plugins.Configuration{}
		irs := !test.reviewActsAsApprove
		config.Approve = append(config.Approve, plugins.Approve{
			Repos:             []string{test.reviewEvent.Repo.Owner.Login},
			LgtmActsAsApprove: test.lgtmActsAsApprove,
			IgnoreReviewState: &irs,
		})
		err := handleReview(
			logrus.WithField("plugin", "approve"),
			fghc,
			fakeOwnersClient{},
			githubConfig,
			config,
			&test.reviewEvent,
		)

		if test.expectHandle && !handled {
			t.Errorf("%s: expected call to handleFunc, but it wasn't called", test.name)
		}

		if !test.expectHandle && handled {
			t.Errorf("%s: expected no call to handleFunc, but it was called", test.name)
		}

		if test.expectState != nil && !reflect.DeepEqual(test.expectState, gotState) {
			t.Errorf("%s: expected PR state to equal: %#v, but got: %#v", test.name, test.expectState, gotState)
		}

		if err != nil {
			t.Errorf("%s: error calling handleReview: %v", test.name, err)
		}
		handled = false
	}
}

func TestHandlePullRequest(t *testing.T) {
	tests := []struct {
		name         string
		prEvent      github.PullRequestEvent
		expectHandle bool
		expectState  *state
	}{
		{
			name: "pr opened",
			prEvent: github.PullRequestEvent{
				Action: github.PullRequestActionOpened,
				PullRequest: github.PullRequest{
					User: github.User{
						Login: "P.R. Author",
					},
					Base: github.PullRequestBranch{
						Ref: "branch",
					},
					Body: "Fix everything",
				},
				Number: 1,
			},
			expectHandle: true,
			expectState: &state{
				org:       "org",
				repo:      "repo",
				branch:    "branch",
				number:    1,
				body:      "Fix everything",
				author:    "P.R. Author",
				assignees: nil,
				htmlURL:   "",
			},
		},
		{
			name: "pr reopened",
			prEvent: github.PullRequestEvent{
				Action: github.PullRequestActionReopened,
			},
			expectHandle: true,
		},
		{
			name: "pr sync",
			prEvent: github.PullRequestEvent{
				Action: github.PullRequestActionSynchronize,
			},
			expectHandle: true,
		},
		{
			name: "pr labeled",
			prEvent: github.PullRequestEvent{
				Action: github.PullRequestActionLabeled,
				Label: github.Label{
					Name: labels.Approved,
				},
			},
			expectHandle: true,
		},
		{
			name: "pr another label",
			prEvent: github.PullRequestEvent{
				Action: github.PullRequestActionLabeled,
				Label: github.Label{
					Name: "some-label",
				},
			},
			expectHandle: false,
		},
		{
			name: "pr closed",
			prEvent: github.PullRequestEvent{
				Action: github.PullRequestActionLabeled,
				Label: github.Label{
					Name: labels.Approved,
				},
				PullRequest: github.PullRequest{
					State: "closed",
				},
			},
			expectHandle: false,
		},
		{
			name: "pr review requested",
			prEvent: github.PullRequestEvent{
				Action: github.PullRequestActionReviewRequested,
			},
			expectHandle: false,
		},
	}

	var handled bool
	var gotState *state
	handleFunc = func(log *logrus.Entry, ghc githubClient, repo approvers.Repo, githubConfig config.GitHubOptions, opts *plugins.Approve, pr *state) error {
		gotState = pr
		handled = true
		return nil
	}
	defer func() {
		handleFunc = handle
	}()

	repo := github.Repo{
		Owner: github.User{
			Login: "org",
		},
		Name: "repo",
	}
	fghc := fakegithub.NewFakeClient()

	for _, test := range tests {
		test.prEvent.Repo = repo
		err := handlePullRequest(
			logrus.WithField("plugin", "approve"),
			fghc,
			fakeOwnersClient{},
			config.GitHubOptions{
				LinkURL: &url.URL{
					Scheme: "https",
					Host:   "github.com",
				},
			},
			&plugins.Configuration{},
			&test.prEvent,
		)

		if test.expectHandle && !handled {
			t.Errorf("%s: expected call to handleFunc, but it wasn't called", test.name)
		}

		if !test.expectHandle && handled {
			t.Errorf("%s: expected no call to handleFunc, but it was called", test.name)
		}

		if test.expectState != nil && !reflect.DeepEqual(test.expectState, gotState) {
			t.Errorf("%s: expected PR state to equal: %#v, but got: %#v", test.name, test.expectState, gotState)
		}

		if err != nil {
			t.Errorf("%s: error calling handlePullRequest: %v", test.name, err)
		}
		handled = false
	}
}

func TestHelpProvider(t *testing.T) {
	enabledRepos := []config.OrgRepo{
		{Org: "org1", Repo: "repo"},
		{Org: "org2", Repo: "repo"},
	}
	cases := []struct {
		name         string
		config       *plugins.Configuration
		enabledRepos []config.OrgRepo
		err          bool
	}{
		{
			name:         "Empty config",
			config:       &plugins.Configuration{},
			enabledRepos: enabledRepos,
		},
		{
			name: "All configs enabled",
			config: &plugins.Configuration{
				Approve: []plugins.Approve{
					{
						Repos:               []string{"org2/repo"},
						IssueRequired:       true,
						RequireSelfApproval: &[]bool{true}[0],
						LgtmActsAsApprove:   true,
						IgnoreReviewState:   &[]bool{true}[0],
					},
				},
			},
			enabledRepos: enabledRepos,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := helpProvider(c.config, c.enabledRepos)
			if err != nil && !c.err {
				t.Fatalf("helpProvider error: %v", err)
			}
		})
	}
}
