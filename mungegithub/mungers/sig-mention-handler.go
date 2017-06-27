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

package mungers

import (
	"fmt"
	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"k8s.io/test-infra/mungegithub/features"
	"k8s.io/test-infra/mungegithub/github"
	"strings"
)

type SigMentionHandler struct{}

func init() {
	h := &SigMentionHandler{}
	RegisterMungerOrDie(h)
}

// Name is the name usable in --pr-mungers
func (*SigMentionHandler) Name() string { return "sig-mention-handler" }

// RequiredFeatures is a slice of 'features' that must be provided
func (*SigMentionHandler) RequiredFeatures() []string {
	return []string{}
}

// Initialize will initialize the munger
func (s *SigMentionHandler) Initialize(config *github.Config, features *features.Features) error {
	return nil
}

// EachLoop is called at the start of every munge loop
func (*SigMentionHandler) EachLoop() error { return nil }

// AddFlags will add any request flags to the cobra `cmd`
func (*SigMentionHandler) AddFlags(cmd *cobra.Command, config *github.Config) {}

func (*SigMentionHandler) HasSigLabel(obj *github.MungeObject) bool {
	labels := obj.Issue.Labels

	for i := range labels {
		if labels[i].Name != nil && strings.HasPrefix(*labels[i].Name, "sig/") {
			return true
		}
	}

	return false
}

func (*SigMentionHandler) HasNeedsSigLabel(obj *github.MungeObject) bool {
	labels := obj.Issue.Labels

	for i := range labels {
		if labels[i].Name != nil && strings.Compare(*labels[i].Name, "needs-sig") == 0 {
			return true
		}
	}

	return false
}

// Munge is the workhorse notifying issue owner to add a @kubernetes/sig mention if there is none
// The algorithm:
// (1) return if it is a PR and/or the issue is closed
// (2) find if the issue has a sig label
// (3) find if the issue has a needs-sig label
// (4) if the issue has both the sig and needs-sig labels, remove the needs-sig label
// (5) if the issue has none of the labels, add the needs-sig label and comment
// (6) if the issue has only the sig label, do nothing
// (7) if the issue has only the needs-sig label, do nothing
func (s *SigMentionHandler) Munge(obj *github.MungeObject) {
	if obj.Issue == nil || obj.IsPR() || obj.Issue.State == nil || *obj.Issue.State == "closed" {
		return
	}

	hasSigLabel := s.HasSigLabel(obj)
	hasNeedsSigLabel := s.HasNeedsSigLabel(obj)

	if hasSigLabel && hasNeedsSigLabel {
		if err := obj.RemoveLabel("needs-sig"); err != nil {
			glog.Errorf("failed to remove needs-sig label for issue #%v", *obj.Issue.Number)
		}
	} else if !hasSigLabel && !hasNeedsSigLabel {
		if err := obj.AddLabel("needs-sig"); err != nil {
			glog.Errorf("failed to add needs-sig label for issue #%v", *obj.Issue.Number)
			return
		}

		msg := fmt.Sprintf("@%s There are no sig labels on this issue. Please [add a sig label](https://github.com/kubernetes/test-infra/blob/master/commands.md) by:<br>(1) mentioning a sig: `@kubernetes/sig-<team-name>-misc`<br>e.g., `@kubernetes/sig-api-machinery-*` for API Machinery<br>(2) specifying the label manually: `/sig <label>`<br>e.g., `/sig scalability` for sig/scalability<br><br>_Note: method (1) will trigger a notification to the team. You can find the team list [here](https://github.com/kubernetes/community/blob/master/sig-list.md) and label list [here](https://github.com/kubernetes/kubernetes/labels)_", *obj.Issue.User.Login)

		if err := obj.WriteComment(msg); err != nil {
			glog.Errorf("failed to leave comment for %s that issue #%v needs sig label", *obj.Issue.User.Login, *obj.Issue.Number)
		}
	}
}
