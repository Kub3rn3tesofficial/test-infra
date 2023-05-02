/*
Copyright 2022 The Kubernetes Authors.

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

package approvers2

import (
	"sort"
	"testing"

	"github.com/sirupsen/logrus"

	"net/url"
	"reflect"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/test-infra/prow/plugins/ownersconfig"
)

type fakeapproval struct {
	approver string
	path     string
}

func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].String() < files[j].String()
	})
}

func TestUnapprovedFiles(t *testing.T) {
	rootApprovers := sets.NewString("Alice", "Bob")
	aApprovers := sets.NewString("Art", "Anne")
	bApprovers := sets.NewString("Bill", "Ben", "Barbara")
	cApprovers := sets.NewString("Chris", "Carol")
	dApprovers := sets.NewString("David", "Dan", "Debbie")
	eApprovers := sets.NewString("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.String{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
	}
	tests := []struct {
		testName           string
		filenames          []string
		currentlyApproved  sets.String
		expectedUnapproved sets.String
	}{
		{
			testName:           "Empty PR",
			filenames:          []string{},
			currentlyApproved:  sets.NewString(),
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:           "Single Root File PR Approved",
			filenames:          []string{"kubernetes.go"},
			currentlyApproved:  sets.NewString(rootApprovers.List()[0]),
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:           "Single Root File PR No One Approved",
			filenames:          []string{"kubernetes.go"},
			currentlyApproved:  sets.NewString(),
			expectedUnapproved: sets.NewString("kubernetes.go"),
		},
		{
			testName:           "B Only UnApproved",
			filenames:          []string{"b/test.go"},
			currentlyApproved:  bApprovers,
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:           "B Files Approved at Root",
			filenames:          []string{"b/test.go", "b/test_1.go"},
			currentlyApproved:  rootApprovers,
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:           "B Only UnApproved",
			filenames:          []string{"b/test_1.go", "b/test.go"},
			currentlyApproved:  sets.NewString(),
			expectedUnapproved: sets.NewString("b/test_1.go", "b/test.go"),
		},
		{
			testName:           "Combo and Other; Neither Approved",
			filenames:          []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved:  sets.NewString(),
			expectedUnapproved: sets.NewString("a/combo/test.go", "a/d/test.go"),
		},
		{
			testName:           "Combo and Other; Combo Approved",
			filenames:          []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved:  edcApprovers.Difference(dApprovers),
			expectedUnapproved: sets.NewString("a/d/test.go"),
		},
		{
			testName:           "Combo and Other; Both Approved",
			filenames:          []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved:  edcApprovers.Intersection(dApprovers),
			expectedUnapproved: sets.NewString(),
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: TestSeed, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = false
		for approver := range test.currentlyApproved {
			testApprovers.AddApprover(approver, "REFERENCE", false, "")
		}
		calculated := testApprovers.UnapprovedFiles()
		if !test.expectedUnapproved.Equal(calculated) {
			t.Errorf("Failed for test %v.  Expected unapproved files: %v. Found %v", test.testName, test.expectedUnapproved, calculated)
		}
	}
}

func TestUnapprovedFilesWithGranularApprovals(t *testing.T) {
	rootApprovers := sets.NewString("Alice", "Bob")
	aApprovers := sets.NewString("Art", "Anne")
	bApprovers := sets.NewString("Bill", "Ben", "Barbara")
	cApprovers := sets.NewString("Chris", "Carol")
	dApprovers := sets.NewString("David", "Dan", "Debbie")
	eApprovers := sets.NewString("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.String{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
	}
	tests := []struct {
		testName           string
		filenames          []string
		currentApproval    []fakeapproval
		expectedUnapproved sets.String
	}{
		{
			testName:           "Empty PR",
			filenames:          []string{},
			currentApproval:    []fakeapproval{},
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:  "Single Root File PR Approved",
			filenames: []string{"kubernetes.go"},
			currentApproval: []fakeapproval{
				{approver: rootApprovers.List()[0], path: ""},
			},
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:  "Single Root File PR With Single File Approved",
			filenames: []string{"kubernetes.go"},
			currentApproval: []fakeapproval{
				{approver: rootApprovers.List()[0], path: "kubernetes.go"},
			},
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:           "Single Root File PR No One Approved",
			filenames:          []string{"kubernetes.go"},
			currentApproval:    []fakeapproval{},
			expectedUnapproved: sets.NewString("kubernetes.go"),
		},
		{
			testName:  "B Only UnApproved",
			filenames: []string{"b/test.go"},
			currentApproval: []fakeapproval{
				{approver: bApprovers.List()[0], path: "b/test.go"},
			},
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:  "B Only Approved using wildcard",
			filenames: []string{"b/test.go", "b/test_1.go"},
			currentApproval: []fakeapproval{
				{approver: bApprovers.List()[0], path: "b/*"},
			},
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:  "B Only Approved One File",
			filenames: []string{"b/test.go", "b/test_1.go"},
			currentApproval: []fakeapproval{
				{approver: bApprovers.List()[0], path: "b/test.go"},
			},
			expectedUnapproved: sets.NewString("b/test_1.go"),
		},
		{
			testName:  "B Files Approved at Root",
			filenames: []string{"b/test.go", "b/test_1.go"},
			currentApproval: []fakeapproval{
				{approver: rootApprovers.List()[0], path: "b/test.go"},
				{approver: rootApprovers.List()[0], path: "b/test_1.go"},
			},
			expectedUnapproved: sets.NewString(),
		},
		{
			testName:  "Root Approver Partially Approved Using Wildcard",
			filenames: []string{"a/test.go", "b/test.go", "c/test.go"},
			currentApproval: []fakeapproval{
				{approver: rootApprovers.List()[0], path: "a/*"},
			},
			expectedUnapproved: sets.NewString("b/test.go", "c/test.go"),
		},
		{
			testName:  "Root Approver Approver Everything Using Wildcard",
			filenames: []string{"a/test.go", "b/test.go", "c/test.go"},
			currentApproval: []fakeapproval{
				{approver: rootApprovers.List()[0], path: "*"},
			},
			expectedUnapproved: sets.NewString(),
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: TestSeed, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = false
		for _, approval := range test.currentApproval {
			testApprovers.AddApprover(approval.approver, "REFERENCE", false, approval.path)
		}
		calculated := testApprovers.UnapprovedFiles()
		if !test.expectedUnapproved.Equal(calculated) {
			t.Errorf("Failed for test %v.  Expected unapproved files: %v. Found %v", test.testName, test.expectedUnapproved, calculated)
		}
	}
}

func TestGetFiles(t *testing.T) {
	rootApprovers := sets.NewString("Alice", "Bob")
	aApprovers := sets.NewString("Art", "Anne")
	bApprovers := sets.NewString("Bill", "Ben", "Barbara")
	cApprovers := sets.NewString("Chris", "Carol")
	dApprovers := sets.NewString("David", "Dan", "Debbie")
	eApprovers := sets.NewString("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.String{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
	}
	tests := []struct {
		testName          string
		filenames         []string
		currentlyApproved sets.String
		expectedFiles     []File
	}{
		{
			testName:          "Empty PR",
			filenames:         []string{},
			currentlyApproved: sets.NewString(),
			expectedFiles:     []File{},
		},
		{
			testName:          "Single Root File PR Approved",
			filenames:         []string{"kubernetes.go"},
			currentlyApproved: sets.NewString(rootApprovers.List()[0]),
			expectedFiles:     []File{},
		},
		{
			testName:          "Single File PR in B No One Approved",
			filenames:         []string{"b/test.go"},
			currentlyApproved: sets.NewString(),
			expectedFiles: []File{UnapprovedFile{
				baseURL:        &url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"},
				filepath:       "b",
				branch:         "master",
				ownersFilename: ownersconfig.FakeFilenames.Owners,
			}},
		},
		{
			testName:          "Single File PR in B Fully Approved",
			filenames:         []string{"b/test.go"},
			currentlyApproved: bApprovers,
			expectedFiles:     []File{},
		},
		{
			testName:          "Single Root File PR No One Approved",
			filenames:         []string{"kubernetes.go"},
			currentlyApproved: sets.NewString(),
			expectedFiles: []File{UnapprovedFile{
				baseURL:        &url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"},
				filepath:       "",
				branch:         "master",
				ownersFilename: ownersconfig.FakeFilenames.Owners,
			}},
		},
		{
			testName:          "Combo and Other; Neither Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved: sets.NewString(),
			expectedFiles: []File{
				UnapprovedFile{
					baseURL:        &url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"},
					filepath:       "a/combo",
					branch:         "master",
					ownersFilename: ownersconfig.FakeFilenames.Owners,
				},
				UnapprovedFile{
					baseURL:        &url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"},
					filepath:       "a/d",
					branch:         "master",
					ownersFilename: ownersconfig.FakeFilenames.Owners,
				},
			},
		},
		{
			testName:          "Combo and Other; Combo Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved: eApprovers,
			expectedFiles: []File{
				UnapprovedFile{
					baseURL:        &url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"},
					filepath:       "a/d",
					branch:         "master",
					ownersFilename: ownersconfig.FakeFilenames.Owners,
				},
			},
		},
		{
			testName:          "Combo and Other; Both Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved: edcApprovers.Intersection(dApprovers),
			expectedFiles:     []File{},
		},
		{
			testName:          "Combo, C, D; Combo and C Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go", "c/test"},
			currentlyApproved: cApprovers,
			expectedFiles: []File{
				UnapprovedFile{
					baseURL:        &url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"},
					filepath:       "a/d",
					branch:         "master",
					ownersFilename: ownersconfig.FakeFilenames.Owners,
				},
			},
		},
		{
			testName:          "Files Approved Multiple times",
			filenames:         []string{"a/test.go", "a/d/test.go", "b/test"},
			currentlyApproved: rootApprovers.Union(aApprovers).Union(bApprovers),
			expectedFiles:     []File{},
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: TestSeed, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = false
		for approver := range test.currentlyApproved {
			testApprovers.AddApprover(approver, "REFERENCE", false, "")
		}
		calculated := testApprovers.GetFiles(&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "master")
		sortFiles(test.expectedFiles)
		sortFiles(calculated)
		if !reflect.DeepEqual(test.expectedFiles, calculated) {
			t.Errorf("Failed for test %v.  Expected files: %v. Found %v", test.testName, test.expectedFiles, calculated)
		}
	}
}

type approval struct {
	name string
	path string
}

func TestGetCCs(t *testing.T) {
	rootApprovers := sets.NewString("Alice", "Bob")
	aApprovers := sets.NewString("Art", "Anne")
	bApprovers := sets.NewString("Bill", "Ben", "Barbara")
	cApprovers := sets.NewString("Chris", "Carol")
	dApprovers := sets.NewString("David", "Dan", "Debbie")
	eApprovers := sets.NewString("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	fApprovers := sets.NewString("Fred")
	FakeRepoMap := map[string]sets.String{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
		"f":       fApprovers,
	}
	tests := []struct {
		testName          string
		filenames         []string
		currentlyApproved []approval
		// testSeed affects who is chosen for CC
		testSeed  int64
		assignees []string
		// order matters for CCs
		expectedCCs []string
	}{
		{
			testName:          "Empty PR",
			filenames:         []string{},
			currentlyApproved: []approval{},
			testSeed:          0,
			expectedCCs:       []string{},
		},
		{
			testName:  "Single Root FFile PR Approved",
			filenames: []string{"kubernetes.go"},
			currentlyApproved: []approval{
				{rootApprovers.List()[0], ""},
			},
			testSeed:    13,
			expectedCCs: []string{},
		},
		{
			testName:          "Single Root File PR Unapproved Seed = 13",
			filenames:         []string{"kubernetes.go"},
			currentlyApproved: []approval{},
			testSeed:          13,
			expectedCCs:       []string{"alice"},
		},
		{
			testName:  "Single Root File PR Partially Approved Seed = 13",
			filenames: []string{"kubernetes.go", "root.go"},
			currentlyApproved: []approval{
				{"Alice", "kubernetes.go"},
			},
			testSeed:    13,
			expectedCCs: []string{"bob"},
		},
		{
			testName:          "Single Root File PR No One Seed = 10",
			filenames:         []string{"kubernetes.go"},
			testSeed:          10,
			currentlyApproved: []approval{},
			expectedCCs:       []string{"bob"},
		},
		{
			testName:  "A and B File PR. Root Partial Approval Seed = 10",
			filenames: []string{"a/test.go", "a/test2.go", "b/test.go", "b/test2.go"},
			testSeed:  10,
			currentlyApproved: []approval{
				{"Alice", "*/test2.go"},
			},
			expectedCCs: []string{"art", "barbara"},
		},
		{
			testName:          "Combo and Other; Neither Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:          0,
			currentlyApproved: []approval{},
			expectedCCs:       []string{"debbie"},
		},
		{
			testName:  "Combo and Other; Combo Approved",
			filenames: []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:  0,
			currentlyApproved: func() []approval {
				approvers := []approval{}
				for approver := range eApprovers {
					approvers = append(approvers, approval{approver, ""})
				}
				return approvers
			}(),
			expectedCCs: []string{"debbie"},
		},
		{
			testName:  "Combo and Other; Combo Partially Approved",
			filenames: []string{"a/combo/test.go", "a/combo/test2.go", "a/d/test.go", "a/d/test2.go"},
			testSeed:  0,
			currentlyApproved: func() []approval {
				approvers := []approval{}
				for approver := range eApprovers {
					approvers = append(approvers, approval{approver, "*/test2.go"})
				}
				return approvers
			}(),
			expectedCCs: []string{"debbie"},
		},
		{
			testName:  "Combo and Other; Both Partially Approved",
			filenames: []string{"a/combo/test.go", "a/combo/test2.go", "a/d/test.go", "a/d/test2.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"Dan", "a/d/test2.go"},
				{"Eve", "*/test2.go"},
			},
			expectedCCs: []string{"debbie"},
		},
		{
			testName:  "Combo and Other; Both Partially Approved, Seed = 10",
			filenames: []string{"a/combo/test.go", "a/combo/test2.go", "a/d/test.go", "a/d/test2.go"},
			testSeed:  5,
			currentlyApproved: []approval{
				{"Dan", "a/d/test2.go"},
				{"Eve", "*/test2.go"},
			},
			expectedCCs: []string{"david"},
		},
		{
			testName:  "Combo and Other; Both Approved",
			filenames: []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:  0,
			currentlyApproved: func() []approval {
				approvers := []approval{}
				for approver := range dApprovers {
					approvers = append(approvers, approval{approver, ""})
				}
				return approvers
			}(), // dApprovers can approve combo and d directory
			expectedCCs: []string{},
		},
		{
			testName:          "Combo, C, D; None Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: []approval{},
			// chris can approve c and combo, debbie can approve d
			expectedCCs: []string{"carol", "debbie"},
		},
		{
			testName:          "A, B, C; Nothing Approved",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: []approval{},
			// Need an approver from each of the three owners files
			expectedCCs: []string{"anne", "bill", "carol"},
		},
		{
			testName:  "A, B, C; One File Approved Per Folder By Root Approvers",
			filenames: []string{"a/test.go", "a/test2.go", "b/test.go", "b/test2.go", "c/test.go", "c/test2.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"Alice", "a/test.go"},
				{"Alice", "b/test.go"},
				{"Bob", "c/test.go"},
			},
			expectedCCs: []string{"anne", "bill", "carol"},
		},
		{
			testName:  "A, B, C; Partial Files Approved By Root Approver",
			filenames: []string{"a/test.go", "b/test.go", "b/test2.go", "c/test.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"Alice", "a/*"},
				{"Alice", "*/test2.go"},
			},
			// Need an approver from each of the three owners files
			expectedCCs: []string{"bill", "carol"},
		},
		{
			testName:  "A, B, C; Partially approved by non-suggested approvers",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			// Approvers are valid approvers, but not the one we would suggest
			currentlyApproved: []approval{
				{"Art", ""},
				{"Ben", ""},
			},
			// We don't suggest approvers for a and b, only for unapproved c.
			expectedCCs: []string{"carol"},
		},
		{
			testName:  "A, B, C; Nothing approved, but assignees can approve",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			// Approvers are valid approvers, but not the one we would suggest
			currentlyApproved: []approval{},
			assignees:         []string{"Art", "Ben"},
			// We suggest assigned people rather than "suggested" people
			// Suggested would be "Anne", "Bill", "Carol" if no one was assigned.
			expectedCCs: []string{"art", "ben", "carol"},
		},
		{
			testName:          "A, B, C; Nothing approved, but SOME assignees can approve",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: []approval{},
			// Assignees are a mix of potential approvers and random people
			assignees: []string{"Art", "Ben", "John", "Jack"},
			// We suggest assigned people rather than "suggested" people
			expectedCCs: []string{"art", "ben", "carol"},
		},
		{
			testName:          "Assignee is top OWNER, No one has approved",
			filenames:         []string{"a/test.go"},
			testSeed:          0,
			currentlyApproved: []approval{},
			// Assignee is a root approver
			assignees:   []string{"alice"},
			expectedCCs: []string{"alice"},
		},
		{
			testName:  "F folder partially approved, next set of CCs should reuse F folder approver",
			filenames: []string{"f/f.go", "f/f_test.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"Fred", "f/*_test.go"},
			},
			assignees:   []string{""},
			expectedCCs: []string{"fred"},
		},
		{
			testName:  "G folder partially approved by parent approver, next set of CCs should reuse parent approver",
			filenames: []string{"f/g/g.go", "f/g/g_test.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"Fred", "f/g/*_test.go"},
			},
			// Assignee is a root approver
			assignees:   []string{""},
			expectedCCs: []string{"fred"},
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: test.testSeed, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = false
		for _, aprvl := range test.currentlyApproved {
			testApprovers.AddApprover(aprvl.name, "REFERENCE", false, aprvl.path)
		}
		testApprovers.AddAssignees(test.assignees...)
		calculated := testApprovers.GetCCs()
		if !reflect.DeepEqual(test.expectedCCs, calculated) {
			t.Errorf("Failed for test %v.  Expected CCs: %v. Found %v", test.testName, test.expectedCCs, calculated)
		}
	}
}

func TestIsApproved(t *testing.T) {
	rootApprovers := sets.NewString("Alice", "Bob")
	aApprovers := sets.NewString("Art", "Anne")
	bApprovers := sets.NewString("Bill", "Ben", "Barbara")
	cApprovers := sets.NewString("Chris", "Carol")
	dApprovers := sets.NewString("David", "Dan", "Debbie")
	eApprovers := sets.NewString("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.String{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
		"d":       {},
	}
	tests := []struct {
		testName                        string
		filenames                       []string
		currentlyApproved               []approval
		testSeed                        int64
		isApproved                      bool
		autoApproveUnownedSubfoldersMap map[string]bool
	}{
		{
			testName:          "Empty PR",
			filenames:         []string{},
			currentlyApproved: []approval{},
			testSeed:          0,
			isApproved:        false,
		},
		{
			testName:  "Single Root File PR Approved",
			filenames: []string{"kubernetes.go"},
			currentlyApproved: []approval{
				{rootApprovers.List()[0], ""},
			},
			testSeed:   3,
			isApproved: true,
		},
		{
			testName:          "Single Root File PR No One Approved",
			filenames:         []string{"kubernetes.go"},
			testSeed:          0,
			currentlyApproved: []approval{},
			isApproved:        false,
		},
		{
			testName:  "Multiple Root Files. Wildcard Apporval By Root Approver",
			filenames: []string{"kubernetes.go", "root.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{rootApprovers.List()[0], "*"},
			},
			isApproved: true,
		},
		{
			testName:  "Multiple Root Files. Partial Apporval By Root Approver",
			filenames: []string{"kubernetes.go", "root.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{rootApprovers.List()[0], "root.go"},
			},
			isApproved: false,
		},
		{
			testName:          "Combo and Other; Neither Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:          0,
			currentlyApproved: []approval{},
			isApproved:        false,
		},
		{
			testName:  "Combo and Other; Both Approved",
			filenames: []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"David", ""},
				{"Eve", ""},
			},
			isApproved: true,
		},
		{
			testName:  "Combo and Other; Both Partially Approved",
			filenames: []string{"a/combo/test.go", "a/combo/test2.go", "a/d/test.go", "a/d/test2.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"David", "*/test2.go"},
				{"Eve", "*/test2.go"},
			},
			isApproved: false,
		},
		{
			testName:          "A, B, C; Nothing Approved",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: []approval{},
			isApproved:        false,
		},
		{
			testName:  "A, B, C; A and B Approved",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			currentlyApproved: []approval{
				{aApprovers.List()[0], ""},
				{bApprovers.List()[0], ""},
			},
			isApproved: false,
		},
		{
			testName:  "A, B, C; Approved At the Root",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			currentlyApproved: []approval{
				{rootApprovers.List()[0], ""},
			},
			isApproved: true,
		},
		{
			testName:  "A, B, C; Partially Approved At the Root",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			currentlyApproved: []approval{
				{rootApprovers.List()[0], "a/*"},
				{rootApprovers.List()[0], "b/*"},
			},
			isApproved: false,
		},
		{
			testName:  "A, B, C; Approved At the Leaves",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"Anne", ""},
				{"Ben", ""},
				{"Carol", ""},
			},
			isApproved: true,
		},
		{
			testName:  "A, B, C; Partially By Root Approver And Partially By Level",
			filenames: []string{"a/test.go", "a/test2.go", "b/test.go", "b/test2.go", "c/test.go", "c/test2.go"},
			testSeed:  0,
			currentlyApproved: []approval{
				{"Alice", "*/test2.go"},
				{"Anne", ""},
				{"Ben", ""},
				{"Carol", ""},
			},
			isApproved: true,
		},
		{
			testName:                        "File in folder with AutoApproveUnownedSubfolders does not get approved",
			filenames:                       []string{"a/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders gets approved",
			filenames:                       []string{"a/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      true,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders whose ownersfile has no approvers gets approved",
			filenames:                       []string{"d/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"d": true},
			isApproved:                      true,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders and other unapproved file does not get approved",
			filenames:                       []string{"b/unapproved.go", "a/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders and approved file, approved",
			filenames:                       []string{"b/approved.go", "a/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			currentlyApproved:               []approval{{bApprovers.List()[0], ""}},
			isApproved:                      true,
		},
		{
			testName:                        "Nested subfolder in folder with AutoApproveUnownedSubfolders gets approved",
			filenames:                       []string{"a/new-folder/child/grandchild/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      true,
		},
		{
			testName:                        "Change in folder with Owners whose parent has AutoApproveUnownedSubfolders does not get approved",
			filenames:                       []string{"a/d/new-file.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
		{
			testName:  "Partially approved file in parent folder and change in folder with Owners whose parent has AutoApproveUnownedSubfolders",
			filenames: []string{"a/file.go", "a/d/new-file.go"},
			currentlyApproved: []approval{
				{aApprovers.List()[0], "a/file.go"},
			},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
		{
			testName:  "Blanket approval for parent folder and change in folder with Owners whose parent has AutoApproveUnownedSubfolders, blanket approval supercedes",
			filenames: []string{"a/file.go", "a/d/new-file.go"},
			currentlyApproved: []approval{
				{aApprovers.List()[0], "a/*"},
			},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      true,
		},
		{
			testName:  "Partially approved file and change in folder with Owners whose parent (different folder) has AutoApproveUnownedSubfolders",
			filenames: []string{"a/file.go", "b/d/new-file.go"},
			currentlyApproved: []approval{
				{aApprovers.List()[0], ""},
			},
			autoApproveUnownedSubfoldersMap: map[string]bool{"b": true},
			isApproved:                      true,
		},
		{
			testName:  "Partially approved file and change in folder with Owners whose parent (different folder) has AutoApproveUnownedSubfolders",
			filenames: []string{"a/file.go", "b/d/new-file.go"},
			currentlyApproved: []approval{
				{aApprovers.List()[0], ""},
			},
			autoApproveUnownedSubfoldersMap: map[string]bool{"b": true},
			isApproved:                      true,
		},
		{
			testName:  "Partially approved file and change in folder with Owners whose parent (different folder) does not have AutoApproveUnownedSubfolders",
			filenames: []string{"b/file.go", "a/d/new-file.go"},
			currentlyApproved: []approval{
				{bApprovers.List()[0], ""},
			},
			isApproved: false,
		},
		{
			testName:                        "Unapproved file in parent folder and change in folder with Owners whose parent has AutoApproveUnownedSubfolders",
			filenames:                       []string{"a/file.go", "a/d/new-file.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
		{
			testName:                        "Unapproved file in parent folder and change in folder with Owners whose parent has AutoApproveUnownedSubfolders",
			filenames:                       []string{"a/file.go", "a/d/new-file.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap, func(fr *FakeRepo) {
			fr.autoApproveUnownedSubfolders = test.autoApproveUnownedSubfoldersMap
		}), seed: test.testSeed, log: logrus.WithField("plugin", "some_plugin")})
		for _, approver := range test.currentlyApproved {
			testApprovers.AddApprover(approver.name, "REFERENCE", false, approver.path)
		}
		calculated := testApprovers.IsApproved()
		if test.isApproved != calculated {
			t.Errorf("Failed for test %v.  Expected Approval Status: %v. Found %v", test.testName, test.isApproved, calculated)
		}
	}
}

func TestIsApprovedWithIssue(t *testing.T) {
	aApprovers := sets.NewString("Author", "Anne", "Carl")
	bApprovers := sets.NewString("Bill", "Carl")
	FakeRepoMap := map[string]sets.String{"a": aApprovers, "b": bApprovers}
	tests := []struct {
		testName          string
		filenames         []string
		currentlyApproved []approval
		noIssueApprovers  sets.String
		associatedIssue   int
		useselfApprove    bool
		isApproved        bool
	}{
		{
			testName:          "Empty PR",
			filenames:         []string{},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Single. No issue. No Approval",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Single file. No issue approval. File approved",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{{"Carl", ""}},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Single file. With issue. File approved. Issue not approved",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{{"Carl", ""}},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   100,
			isApproved:        true,
		},
		{
			testName:          "Single file. With issue. File approved. Issue Approved",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{{"Carl", ""}},
			noIssueApprovers:  sets.NewString("Carl"),
			associatedIssue:   100,
			isApproved:        true,
		},
		{
			testName:          "Single file. With issue. File not approved. Issue approved",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString("Carl"),
			associatedIssue:   100,
			isApproved:        false,
		},
		{
			testName:          "Single file. With issue. File not approved. Issue not approved",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   100,
			isApproved:        false,
		},
		{
			testName:          "Single file. With issue. File approved. Issue approval not valid",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{{"Anne", ""}},
			noIssueApprovers:  sets.NewString("Bill"),
			associatedIssue:   100,
			isApproved:        true,
		},
		{
			testName:          "Two files. No Issue. Files partially approved. Issue Approved",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{{"Carl", "a/*"}},
			noIssueApprovers:  sets.NewString("Bill"),
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Two files. With Issue. Files partially approved. Issue Approved",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{{"Carl", "a/*"}},
			noIssueApprovers:  sets.NewString("Bill"),
			associatedIssue:   100,
			isApproved:        false,
		},
		{
			testName:          "Two files. No Issue. Files Approved. Issue not approved",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{{"Carl", ""}},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Two files. With Issue. Files not approved. Issue approved",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString("Anne"),
			associatedIssue:   100,
			isApproved:        false,
		},
		{
			testName:  "Two files. With Issue. Files approved. Issue approved",
			filenames: []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{
				{"Anne", ""},
				{"Bill", ""},
			},
			noIssueApprovers: sets.NewString("Anne"),
			associatedIssue:  100,
			isApproved:       true,
		},
		{
			testName:          "Self approval missing issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   0,
			useselfApprove:    true,
			isApproved:        true,
		},
		{
			testName:          "Self approval with issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   10,
			useselfApprove:    true,
			isApproved:        true,
		},
		{
			testName:          "Self approval. No issue. Issue not approved",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   0,
			useselfApprove:    true,
			isApproved:        false,
		},
		{
			testName:          "Self approval. With issues. Files partially approved. Issue not approved",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{},
			noIssueApprovers:  sets.NewString(),
			associatedIssue:   0,
			useselfApprove:    true,
			isApproved:        false,
		},
		{
			testName:          "Self approval. With Issue. Files approved. Issue approved",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: []approval{{"Bill", ""}},
			noIssueApprovers:  sets.NewString("Carl"),
			associatedIssue:   100,
			useselfApprove:    true,
			isApproved:        true,
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: 0, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = true
		testApprovers.AssociatedIssue = test.associatedIssue
		for _, aprvl := range test.currentlyApproved {
			testApprovers.AddApprover(aprvl.name, "REFERENCE", false, aprvl.path)
		}
		if test.useselfApprove {
			testApprovers.AddAuthorSelfApprover("Author", "REFERENCE", false, "")
		}
		for nia := range test.noIssueApprovers {
			testApprovers.AddNoIssueApprover(nia, "REFERENCE")
		}
		if test.useselfApprove {
			testApprovers.AddNoIssueAuthorSelfApprover("Author", "REFERENCE")
		}
		calculated := testApprovers.IsApproved()
		if test.isApproved != calculated {
			t.Errorf("Failed for test %v.  Expected Approval Status: %v. Found %v", test.testName, test.isApproved, calculated)
		}
	}
}

func TestGetFilesApprovers(t *testing.T) {
	tests := []struct {
		testName       string
		filenames      []string
		approvals      []approval
		owners         map[string]sets.String
		expectedStatus map[string]sets.String
	}{
		{
			testName:       "Empty PR",
			filenames:      []string{},
			approvals:      []approval{},
			owners:         map[string]sets.String{},
			expectedStatus: map[string]sets.String{},
		},
		{
			testName:  "No approvals",
			filenames: []string{"a/a", "c"},
			approvals: []approval{},
			owners:    map[string]sets.String{"": sets.NewString("RootOwner")},
			expectedStatus: map[string]sets.String{
				"a/a": sets.NewString(),
				"c":   sets.NewString(),
			},
		},
		{
			testName:  "Partial approvals",
			filenames: []string{"a/a", "b/b", "c/c"},
			approvals: []approval{
				{"AApprover", "a/*"},
				{"BApprover", "a/*"},
			},
			owners: map[string]sets.String{
				"":  sets.NewString("RootOwner"),
				"a": sets.NewString("AApprover"),
				"b": sets.NewString("BApprover"),
				"c": sets.NewString("CApprover"),
			},
			expectedStatus: map[string]sets.String{
				"a/a": sets.NewString("aapprover"),
				"b/b": sets.NewString(),
				"c/c": sets.NewString(),
			},
		},
		{
			testName: "Approvers approves some",
			filenames: []string{
				"a/a",
				"c/c",
			},
			approvals: []approval{{"CApprover", ""}},
			owners: map[string]sets.String{
				"a": sets.NewString("AApprover"),
				"c": sets.NewString("CApprover"),
			},
			expectedStatus: map[string]sets.String{
				"a/a": sets.NewString(),
				"c/c": sets.NewString("capprover"),
			},
		},
		{
			testName: "Multiple approvers",
			filenames: []string{
				"a/a",
				"c/c",
			},
			approvals: []approval{
				{"RootApprover", ""},
				{"CApprover", ""},
			},
			owners: map[string]sets.String{
				"":  sets.NewString("RootApprover"),
				"a": sets.NewString("AApprover"),
				"c": sets.NewString("CApprover"),
			},
			expectedStatus: map[string]sets.String{
				"a/a": sets.NewString("rootapprover"),
				"c/c": sets.NewString("rootapprover", "capprover"),
			},
		},
		{
			testName: "Multiple approvers using path approvals",
			filenames: []string{
				"root",
				"a/a",
				"b/b",
				"c/c",
			},
			approvals: []approval{
				{"RootApprover", ""},
				{"BApprover", "b/*"},
				{"CApprover", "b/*"},
				{"CApprover", ""},
			},
			owners: map[string]sets.String{
				"":  sets.NewString("RootApprover"),
				"a": sets.NewString("AApprover"),
				"b": sets.NewString("BApprover"),
				"c": sets.NewString("CApprover"),
			},
			expectedStatus: map[string]sets.String{
				"root": sets.NewString("rootapprover"),
				"a/a":  sets.NewString("rootapprover"),
				"b/b":  sets.NewString("rootapprover", "bapprover"),
				"c/c":  sets.NewString("rootapprover", "capprover"),
			},
		},
		{
			testName:       "Case-insensitive approvers",
			filenames:      []string{"file"},
			approvals:      []approval{{"RootApprover", ""}},
			owners:         map[string]sets.String{"": sets.NewString("RootApprover")},
			expectedStatus: map[string]sets.String{"file": sets.NewString("rootapprover")},
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(test.owners), log: logrus.WithField("plugin", "some_plugin")})
		for _, aprvl := range test.approvals {
			testApprovers.AddApprover(aprvl.name, "REFERENCE", false, aprvl.path)
		}
		calculated := testApprovers.GetFilesApprovers()
		if !reflect.DeepEqual(test.expectedStatus, calculated) {
			t.Errorf("Failed for test %v.  Expected approval status: %v. Found %v", test.testName, test.expectedStatus, calculated)
		}
	}
}

func TestGetMessage(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddApprover("Bill", "REFERENCE", false, "")

	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Bill</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **alice**
You can assign the PR to them by writing ` + "`/assign @alice`" + ` in a comment when ready.

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **2** files: **1** are approved and **1** are unapproved.  

Needs approval from approvers in these files:
- **[a/OWNERS](https://github.com/org/repo/blob/dev/a/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/dev/b">b/</a></strike> (approved) [bill]</summary>

- <strike>b/b.go</strike> 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/dev/a">a/</a></strong> (unapproved) </summary>

- a/a.go 

</details>


<!-- META={"approvers":["alice"]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "dev"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessagePartiallyApproved(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b1.go", "b/b2.go"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddApprover("Bill", "REFERENCE", false, "b/b1.go")

	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Bill</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **alice**
You can assign the PR to them by writing ` + "`/assign @alice`" + ` in a comment when ready.

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **3** files: **1** are approved and **2** are unapproved.  

Needs approval from approvers in these files:
- **[a/OWNERS](https://github.com/org/repo/blob/dev/a/OWNERS)**
- **[b/OWNERS](https://github.com/org/repo/blob/dev/b/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/dev/a">a/</a></strong> (unapproved) </summary>

- a/a.go 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/dev/b">b/</a></strong> (partially approved, need additional approvals) [bill]</summary>

- b/b2.go 
- <strike>b/b1.go</strike> 

</details>


<!-- META={"approvers":["alice"]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "dev"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageAllApproved(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddApprover("Alice", "REFERENCE", false, "")
	ap.AddLGTMer("Bill", "REFERENCE", false, "")
	ap.AddApprover("Alice", "REFERENCE", true, "")

	want := `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="LGTM">Bill</a>*

Associated issue requirement bypassed by: *<a href="REFERENCE" title="Approved">Alice</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **2** files: **2** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/a">a/</a></strike> (approved) [alice]</summary>

- <strike>a/a.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/b">b/</a></strike> (approved) [bill]</summary>

- <strike>b/b.go</strike> 

</details>


<!-- META={"approvers":[]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageFilesApprovedIssueNotApproved(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddApprover("Alice", "REFERENCE", false, "")
	ap.AddLGTMer("Bill", "REFERENCE", false, "")

	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="LGTM">Bill</a>*

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **2** files: **2** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/a">a/</a></strike> (approved) [alice]</summary>

- <strike>a/a.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/b">b/</a></strike> (approved) [bill]</summary>

- <strike>b/b.go</strike> 

</details>


<!-- META={"approvers":[]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageNoneApproved(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.AddAuthorSelfApprover("John", "REFERENCE", false, "")
	ap.RequireIssue = true
	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Author self-approved">John</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **alice**, **bill**
You can assign the PR to them by writing ` + "`/assign @alice @bill`" + ` in a comment when ready.

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **2** files: **0** are approved and **2** are unapproved.  

Needs approval from approvers in these files:
- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**
- **[b/OWNERS](https://github.com/org/repo/blob/master/b/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/a">a/</a></strong> (unapproved) </summary>

- a/a.go 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/b">b/</a></strong> (unapproved) </summary>

- b/b.go 

</details>


<!-- META={"approvers":["alice","bill"]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageApprovedIssueAssociated(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AssociatedIssue = 12345
	ap.AddAuthorSelfApprover("John", "REFERENCE", false, "")
	ap.AddApprover("Bill", "REFERENCE", false, "")
	ap.AddApprover("Alice", "REFERENCE", false, "")

	want := `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="Approved">Bill</a>*, *<a href="REFERENCE" title="Author self-approved">John</a>*

Associated issue: *#12345*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **2** files: **2** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/a">a/</a></strike> (approved) [alice]</summary>

- <strike>a/a.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/b">b/</a></strike> (approved) [bill]</summary>

- <strike>b/b.go</strike> 

</details>


<!-- META={"approvers":[]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageApprovedNoIssueByPassed(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.md"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddAuthorSelfApprover("John", "REFERENCE", false, "")
	ap.AddApprover("Bill", "REFERENCE", false, "")
	ap.AddApprover("Bill", "REFERENCE", true, "")
	ap.AddApprover("Alice", "REFERENCE", false, "")
	ap.AddApprover("Alice", "REFERENCE", true, "")

	want := `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="Approved">Bill</a>*, *<a href="REFERENCE" title="Author self-approved">John</a>*

Associated issue requirement bypassed by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="Approved">Bill</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

Out of **2** files: **2** are approved and **0** are unapproved.  

The status of the PR is:  

<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/a">a/</a></strike> (approved) [alice]</summary>

- <strike>a/a.go</strike> 

</details>
<details>
<summary><strike><a href="https://github.com/org/repo/blob/master/b">b/</a></strike> (approved) [bill]</summary>

- <strike>b/b.md</strike> 

</details>


<!-- META={"approvers":[]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageMDOwners(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/README.md"},
			repo: createFakeRepo(map[string]sets.String{
				"a":           sets.NewString("Alice"),
				"b":           sets.NewString("Bill"),
				"b/README.md": sets.NewString("Doctor"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.AddAuthorSelfApprover("John", "REFERENCE", false, "")
	ap.RequireIssue = true
	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Author self-approved">John</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **alice**, **doctor**
You can assign the PR to them by writing ` + "`/assign @alice @doctor`" + ` in a comment when ready.

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **2** files: **0** are approved and **2** are unapproved.  

Needs approval from approvers in these files:
- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**
- **[b/README.md](https://github.com/org/repo/blob/master/b/README.md)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/a">a/</a></strong> (unapproved) </summary>

- a/a.go 

</details>
<details>
<summary><strong><a href="https://github.com/org/repo/blob/master/b/README.md">b/README.md/</a></strong> (unapproved) </summary>

- b/README.md 

</details>


<!-- META={"approvers":["alice","doctor"]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageDifferentGitHubLink(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/README.md"},
			repo: createFakeRepo(map[string]sets.String{
				"a": sets.NewString("Alice"),
				"b": sets.NewString("Bill", "Doctor"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.AddAuthorSelfApprover("John", "REFERENCE", false, "")
	ap.RequireIssue = true
	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Author self-approved">John</a>*
To complete the [pull request process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process), please assign **alice**, **bill**
You can assign the PR to them by writing ` + "`/assign @alice @bill`" + ` in a comment when ready.

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

Out of **2** files: **0** are approved and **2** are unapproved.  

Needs approval from approvers in these files:
- **[a/OWNERS](https://github.mycorp.com/org/repo/blob/master/a/OWNERS)**
- **[b/OWNERS](https://github.mycorp.com/org/repo/blob/master/b/OWNERS)**


Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can also choose to approve only specific files by writing ` + "`/approve files <path-to-file>`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
The status of the PR is:  

<details>
<summary><strong><a href="https://github.mycorp.com/org/repo/blob/master/a">a/</a></strong> (unapproved) </summary>

- a/a.go 

</details>
<details>
<summary><strong><a href="https://github.mycorp.com/org/repo/blob/master/b">b/</a></strong> (unapproved) </summary>

- b/README.md 

</details>


<!-- META={"approvers":["alice","bill"]} -->`
	if got := ap.GetMessage(&url.URL{Scheme: "https", Host: "github.mycorp.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}
