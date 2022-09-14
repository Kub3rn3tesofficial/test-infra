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

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	coreapi "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	prowjobv1 "k8s.io/test-infra/prow/apis/prowjobs/v1"
	"k8s.io/test-infra/prow/kube"
	"k8s.io/test-infra/prow/pubsub/subscriber"
	"k8s.io/test-infra/prow/test/integration/internal/fakegitserver"
	"k8s.io/test-infra/prow/test/integration/internal/fakepubsub"
)

func createGerritRepo(id, job string) string {
	// We create PRs *before* uniquifying the repo with the given "id" string,
	// because then we don't have to change the PR sha constants in
	// TestPubSubSubscriptions.
	template := `
echo hello > README.txt
git add README.txt
git commit -m "commit 1"

# Create fake PRs. These are "Gerrit" style refs. Technically we don't actually
# use these ref names in these tests, but we have them here as an illustrative
# example.
for num in 1 2 3; do
	git checkout -d master
	echo "${num}" > "${num}"
	git add "${num}"
	git commit -m "PR${num}"
	git update-ref "refs/changes/00/123/${num}" HEAD
done

git checkout master

echo this-is-from-repo%s > README.txt
git add README.txt
git commit -m "uniquify"

mkdir .prow
cat <<EOF >.prow/presubmits.yaml
%s
EOF

git add .prow/presubmits.yaml
git commit -m "add inrepoconfig"
`
	return fmt.Sprintf(template, id, job)
}

func TestPubSubSubscriptions(t *testing.T) {
	t.Parallel()

	const (
		PubsubEmulatorHost = "localhost"
		UidLabel           = "integration-test/uid"
		Repo1HEADsha       = "8c5dc6fe1b5a63200f23a2364011e8270f0f7cd0"
		Repo2HEADsha       = "0c035e2664a380bf17cbef8ba78c6381cc78e1ce"
		Repo2PR1sha        = "458b96a96a74689447530035f5a71c426bacb505"
		Repo3PR1sha        = Repo2PR1sha
		Repo4PR1sha        = Repo2PR1sha
		Repo5PR1sha        = Repo2PR1sha
		Repo2PR2sha        = "eb02ec2e228b2102b531ec049ffaab9b8c1db346"
		Repo3PR2sha        = Repo2PR2sha
		Repo4PR2sha        = Repo2PR2sha
		Repo2PR3sha        = "b9004c6430af9ffb4cb337dabeba4b6819597fa9"
		Repo3PR3sha        = Repo2PR3sha
		Repo4PR3sha        = Repo2PR3sha
		Repo3HEADsha       = "97b866610ecdee8b90a7808b176c1fb3a859fa00"
		Repo4HEADsha       = "4c028549d727a9deebf69b68b640837844222632"
		Repo5HEADsha       = "c0ea4b30f3d9dc0bf1d3391d8e3a6bee39ad4de6"
		CreateRepoRepo1    = `
echo this-is-from-repo1 > README.txt
git add README.txt
git commit -m "commit 1"
`
		ProwJobDecorated = `
presubmits:
  - name: trigger-inrepoconfig-presubmit-via-pubsub-repo%s
    always_run: false
    decorate: true
    spec:
      containers:
      - image: localhost:5001/alpine
        command:
        - sh
        args:
        - -c
        - |
          set -eu
          echo "hello from trigger-inrepoconfig-presubmit-via-pubsub-repo%s"
          cat README.txt
`
		ProwJobDecoratedCloneURI = `
presubmits:
  - name: trigger-inrepoconfig-presubmit-via-pubsub-repo%s
    always_run: false
    decorate: true
    # Force this job to use a particular CloneURI.
    clone_uri: "%s"
    spec:
      containers:
      - image: localhost:5001/alpine
        command:
        - sh
        args:
        - -c
        - |
          set -eu
          echo "hello from trigger-inrepoconfig-presubmit-via-pubsub-repo%s"
          cat README.txt
`
	)

	CreateRepo2 := createGerritRepo("2", fmt.Sprintf(ProwJobDecorated, "2", "2"))
	CreateRepo3 := createGerritRepo("3", fmt.Sprintf(ProwJobDecorated, "3", "3"))
	CreateRepo4 := createGerritRepo("4", fmt.Sprintf(ProwJobDecorated, "4", "4"))
	CreateRepo5 := createGerritRepo("5", fmt.Sprintf(ProwJobDecoratedCloneURI, "5", "https://fakegitserver.default/repo/org1/repo5", "5"))

	tests := []struct {
		name       string
		repoSetups []fakegitserver.RepoSetup
		msg        fakepubsub.PubSubMessageForSub
		expected   []string
	}{
		{
			name: "staticconfig-postsubmit",
			repoSetups: []fakegitserver.RepoSetup{
				{
					Name:      "repo1",
					Script:    CreateRepoRepo1,
					Overwrite: true,
				},
			},
			msg: fakepubsub.PubSubMessageForSub{
				Attributes: map[string]string{
					subscriber.ProwEventType: subscriber.PostsubmitProwJobEvent,
				},
				Data: subscriber.ProwJobEvent{
					Name: "trigger-postsubmit-via-pubsub1", // This job is defined in the static config.
					Refs: &prowjobv1.Refs{
						Org:      "org1",
						Repo:     "repo1",
						BaseSHA:  Repo1HEADsha,
						BaseRef:  "master",
						CloneURI: "https://fakegitserver.default/repo/repo1",
					},
				},
			},
			expected: []string{Repo1HEADsha},
		},
		{
			name: "inrepoconfig-presubmit2-explicit-cloneuri",
			repoSetups: []fakegitserver.RepoSetup{
				{
					Name:      "repo2",
					Script:    CreateRepo2,
					Overwrite: true,
				},
			},
			msg: fakepubsub.PubSubMessageForSub{
				Attributes: map[string]string{
					subscriber.ProwEventType: subscriber.PresubmitProwJobEvent,
				},
				Data: subscriber.ProwJobEvent{
					Name: "trigger-inrepoconfig-presubmit-via-pubsub-repo2",
					Refs: &prowjobv1.Refs{
						CloneURI: "https://fakegitserver.default/repo/repo2",
						Org:      "https://fakegitserver.default/repo",
						Repo:     "repo2",
						BaseSHA:  Repo2HEADsha,
						BaseRef:  "master",
						Pulls: []prowjobv1.Pull{
							{
								Number: 1,
								SHA:    Repo2PR1sha,
							},
							{
								Number: 2,
								SHA:    Repo2PR2sha,
							},
							{
								Number: 3,
								SHA:    Repo2PR3sha,
							},
						},
					},
					Labels: map[string]string{
						kube.GerritRevision: "123",
					},
				},
			},
			expected: []string{Repo2HEADsha, Repo2PR1sha, Repo2PR2sha, Repo2PR3sha},
		},
		{
			name: "inrepoconfig-presubmit3",
			repoSetups: []fakegitserver.RepoSetup{
				{
					Name:      "repo3",
					Script:    CreateRepo3,
					Overwrite: true,
				},
			},
			msg: fakepubsub.PubSubMessageForSub{
				Attributes: map[string]string{
					subscriber.ProwEventType: subscriber.PresubmitProwJobEvent,
				},
				Data: subscriber.ProwJobEvent{
					Name: "trigger-inrepoconfig-presubmit-via-pubsub-repo3",
					Refs: &prowjobv1.Refs{
						Org:  "https://fakegitserver.default/repo",
						Repo: "repo3",
						// RepoLink is used by clonerefs to determine whether
						// the repo is from Git or GitHub. It is overridden by
						// CloneURI (if set) for determining the clone URL to
						// give to the "git" binary. RepoLink is appended with
						// at ".git" suffix, whereas CloneURI is used as-is.
						RepoLink: "https://fakegitserver.default/repo/repo3",
						BaseSHA:  Repo3HEADsha,
						BaseRef:  "master",
						Pulls: []prowjobv1.Pull{
							{
								Number: 1,
								SHA:    Repo3PR1sha,
							},
							{
								Number: 2,
								SHA:    Repo3PR2sha,
							},
							{
								Number: 3,
								SHA:    Repo3PR3sha,
							},
						},
					},
					Labels: map[string]string{
						kube.GerritRevision: "123",
					},
				},
			},
			expected: []string{Repo3HEADsha, Repo3PR1sha, Repo3PR2sha, Repo3PR3sha},
		},
		{
			name: "inrepoconfig-presubmit4-with-nested-directory",
			repoSetups: []fakegitserver.RepoSetup{
				{
					Name:      "org1/repo4",
					Script:    CreateRepo4,
					Overwrite: true,
				},
			},
			msg: fakepubsub.PubSubMessageForSub{
				Attributes: map[string]string{
					subscriber.ProwEventType: subscriber.PresubmitProwJobEvent,
				},
				Data: subscriber.ProwJobEvent{
					Name: "trigger-inrepoconfig-presubmit-via-pubsub-repo4",
					Refs: &prowjobv1.Refs{
						Org:      "https://fakegitserver.default/repo/org1",
						Repo:     "repo4",
						RepoLink: "https://fakegitserver.default/repo/org1/repo4",
						BaseSHA:  Repo4HEADsha,
						BaseRef:  "master",
						Pulls: []prowjobv1.Pull{
							{
								Number: 1,
								SHA:    Repo4PR1sha,
							},
							{
								Number: 2,
								SHA:    Repo4PR2sha,
							},
							{
								Number: 3,
								SHA:    Repo4PR3sha,
							},
						},
					},
					Labels: map[string]string{
						kube.GerritRevision: "123",
					},
				},
			},
			expected: []string{Repo4HEADsha, Repo4PR1sha, Repo4PR2sha, Repo4PR3sha},
		},
		{
			name: "inrepoconfig-presubmit5-with-clone-uri-in-job-definition",
			repoSetups: []fakegitserver.RepoSetup{
				{
					Name:      "org1/repo5",
					Script:    CreateRepo5,
					Overwrite: true,
				},
			},
			msg: fakepubsub.PubSubMessageForSub{
				Attributes: map[string]string{
					subscriber.ProwEventType: subscriber.PresubmitProwJobEvent,
				},
				Data: subscriber.ProwJobEvent{
					Name: "trigger-inrepoconfig-presubmit-via-pubsub-repo5",
					Refs: &prowjobv1.Refs{
						// Technically Org and Repo are not used by clonerefs as
						// clone_uri is set on the job definition itself (see
						// ProwJobDecoratedCloneURI). However sub needs Org and
						// Repo to retrieve (clone) this inrepo job config.
						Org:     "https://fakegitserver.default/repo/org1",
						Repo:    "repo5",
						BaseSHA: Repo5HEADsha,
						BaseRef: "master",
						Pulls: []prowjobv1.Pull{
							{
								Number: 1,
								SHA:    Repo5PR1sha,
							},
						},
					},
					Labels: map[string]string{
						kube.GerritRevision: "123",
					},
				},
			},
			expected: []string{Repo5HEADsha, Repo5PR1sha},
		},
	}

	// Ensure that all repos are named uniquely, because otherwise they clobber
	// each other when we create them against fakegitserver. This prevents
	// programmer error when writing new tests.
	allRepoDirs := []string{}
	for _, tt := range tests {
		for _, repoSetup := range tt.repoSetups {
			allRepoDirs = append(allRepoDirs, repoSetup.Name)
		}
	}
	if err := enforceUniqueRepoDirs(allRepoDirs); err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			var pod *v1.Pod

			clusterContext := getClusterContext()
			t.Logf("Creating client for cluster: %s", clusterContext)

			restConfig, err := NewRestConfig("", clusterContext)
			if err != nil {
				t.Fatalf("could not create restConfig: %v", err)
			}

			clientset, err := kubernetes.NewForConfig(restConfig)
			if err != nil {
				t.Fatalf("could not create Clientset: %v", err)
			}

			kubeClient, err := ctrlruntimeclient.New(restConfig, ctrlruntimeclient.Options{})
			if err != nil {
				t.Fatalf("Failed creating clients for cluster %q: %v", clusterContext, err)
			}

			fpsClient, err := fakepubsub.NewClient("project1", fmt.Sprintf("%s:%d", PubsubEmulatorHost, *fakepubsubNodePort))
			if err != nil {
				t.Fatalf("Failed creating fakepubsub client")
			}

			// Set up repos on FGS for just this test case.
			fgsClient := fakegitserver.NewClient("http://localhost/fakegitserver", 5*time.Second)
			for _, repoSetup := range tt.repoSetups {
				err := fgsClient.SetupRepo(repoSetup)
				if err != nil {
					t.Fatalf("FGS repo setup failed: %v", err)
				}
			}

			// Create a unique test case ID (UID) for this particular test
			// invocation. This makes it easier to check from this code whether
			// sub actually received the exact same message we just published.
			uid := RandomString(t)
			tt.msg.Data.Labels = make(map[string]string)
			tt.msg.Data.Labels[UidLabel] = uid

			// Publish the message to the topic being watched by sub. This topic
			// is defined in the integration tests's config/prow/config.yaml.
			err = fpsClient.PublishMessage(ctx, tt.msg, "topic1")
			if err != nil {
				t.Fatalf("Failed to publish message to topic1: %v", err)
			}

			// We expect the job to have succeeded. This is mostly copy/pasted
			// from the pod-utils_test.go file next to this file.
			//
			// Testing that the job has succeeded is useful because if there are
			// any refs defined, those refs need to be cloned as well. So it
			// tests more components (clonerefs, initupload, etc). In this
			// sense, the tests here can be thought of as a superset of the
			// TestClonerefs test in pod-utils_test.go.
			//
			// Kind is not super efficient in terms of pod scheduling, and it
			// takes some time for a very basic prowjob to finish(It could take
			// up to 60 seconds). So waiting for clonerefs to success instead of
			// the entire job to save some time on integration test.
			expectCloneSuccess := func() (bool, error) {
				var podsList v1.PodList
				err := kubeClient.List(ctx,
					&podsList,
					&ctrlruntimeclient.ListOptions{Namespace: "test-pods"},
					ctrlruntimeclient.MatchingLabels{"integration-test/uid": uid},
				)
				if err != nil {
					t.Logf("failed listing pods with label: %s", uid)
					return false, nil
				}
				if len(podsList.Items) == 0 {
					return false, nil
				}
				if len(podsList.Items) != 1 {
					return false, fmt.Errorf("unexpected number of matching pods: %d", len(podsList.Items))
				}
				pod = &podsList.Items[0]
				var finishedCloning bool
				for _, container := range pod.Status.InitContainerStatuses {
					if container.Name != "clonerefs" {
						continue
					}
					if container.State.Terminated == nil {
						continue
					}
					finishedCloning = true
					if exitCode := container.State.Terminated.ExitCode; exitCode != 0 {
						return false, fmt.Errorf("clonerefs failed with code %d", exitCode)
					}
				}
				return finishedCloning, nil
			}

			timeout := 90 * time.Second
			pollInterval := 500 * time.Millisecond
			waitErr := wait.Poll(pollInterval, timeout, expectCloneSuccess)
			if waitErr == nil {
				// Only clean up the ProwJob if it succeeded (save the ProwJob for debugging if it failed).
				t.Cleanup(func() {
					if pod != nil {
						if err := kubeClient.Delete(ctx, pod); err != nil {
							t.Logf("Failed cleanup resource %q: %v", pod.Name, err)
						}
					}
				})
			}

			if pod == nil {
				t.Fatalf("Could not find test pod. Wait error: %v", waitErr)
			}
			// Retrieve logs from clonerefs.
			podLogs, err := getPodLogs(clientset, "test-pods", pod.Name, &coreapi.PodLogOptions{Container: "clonerefs"})
			if err != nil {
				t.Fatalf("failed getting logs for clonerefs")
			}
			if waitErr != nil {
				// Print for debugging purpose
				t.Logf("logs for clonerefs:\n\n%s\n\n", podLogs)
				t.Fatalf("Failed waiting for cloneref to succeed: %v", waitErr)
			}

			for _, want := range tt.expected {
				if got := podLogs; !strings.Contains(got, want) {
					t.Fatalf("Clone log mismatch. Want contains '%s', got:\n%s", want, got)
				}
			}
		})
	}
}

func enforceUniqueRepoDirs(dirs []string) error {
	seen := make(map[string]bool)
	for _, dir := range dirs {
		_, ok := seen[dir]
		if ok {
			return fmt.Errorf("directory %q already used", dir)
		}
		seen[dir] = true
	}
	return nil
}
