/*
Copyright 2016 The Kubernetes Authors.

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

package main

import (
	"flag"
	"fmt"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kubernetes/test-infra/ciongke/gcs"
	"github.com/kubernetes/test-infra/ciongke/kube"
)

var (
	repoOwner = flag.String("repo-owner", "", "Owner of the repo.")
	repoURL   = flag.String("repo-url", "", "URL of the repo to test.")
	repoName  = flag.String("repo-name", "", "Name of the repo to test.")
	pr        = flag.Int("pr", 0, "Pull request to test.")
	branch    = flag.String("branch", "", "Target branch.")
	workspace = flag.String("workspace", "/workspace", "Where to checkout the repo.")
	namespace = flag.String("namespace", "default", "Namespace for all CI objects.")
	dryRun    = flag.Bool("dry-run", true, "Whether or not to make mutation GitHub calls.")

	sourceBucket = flag.String("source-bucket", "", "Bucket for source tars.")
	runTestImage = flag.String("run-test-image", "", "Image that runs tests.")
)

type testDescription struct {
	Name    string `yaml:"name"`
	Image   string `yaml:"image"`
	Path    string `yaml:"path"`
	Timeout string `yaml:"timeout"`
}

type testClient struct {
	RepoOwner string
	RepoURL   string
	RepoName  string
	PRNumber  int
	Branch    string
	DryRun    bool

	Workspace    string
	SourceBucket string
	Namespace    string
	RunTestImage string

	GCSClient  gcsClient
	KubeClient kubeClient
}

// kubeClient is satisfied by kube.Client.
type kubeClient interface {
	CreateJob(j kube.Job) (kube.Job, error)
}

// gcsClient is satisfied by gcs.Client.
type gcsClient interface {
	Upload(r io.Reader, bucket, name string) error
}

func main() {
	flag.Parse()

	gc, err := gcs.NewClient()
	if err != nil {
		log.Printf("Error getting GCS client: %s", err)
		return
	}

	kc, err := kube.NewClientInCluster(*namespace)
	if err != nil {
		log.Printf("Error getting Kubernetes client: %s", err)
		return
	}

	client := &testClient{
		RepoOwner: *repoOwner,
		RepoURL:   *repoURL,
		RepoName:  *repoName,
		PRNumber:  *pr,
		Branch:    *branch,
		DryRun:    *dryRun,

		Workspace:    *workspace,
		SourceBucket: *sourceBucket,
		Namespace:    *namespace,
		RunTestImage: *runTestImage,

		GCSClient:  gc,
		KubeClient: kc,
	}
	if err := client.TestPR(); err != nil {
		log.Printf("Error testing PR: %s", err)
		return
	}
}

func (c *testClient) TestPR() error {
	mergeable, head, err := c.checkoutPR()
	if err != nil {
		return fmt.Errorf("error checking out git repo: %s", err)
	}
	if !mergeable {
		return fmt.Errorf("needs rebase")
	}

	if err = c.uploadSource(); err != nil {
		return fmt.Errorf("error uploading source: %s", err)
	}

	if err = c.startTests(head); err != nil {
		return fmt.Errorf("error starting tests: %s", err)
	}

	return nil
}

// checkoutPR does the checkout and returns whether or not the PR can be merged
// as well as its head SHA.
func (c *testClient) checkoutPR() (bool, string, error) {
	clonePath := filepath.Join(c.Workspace, c.RepoName)
	cloneCommand := exec.Command("git", "clone", "--no-checkout", c.RepoURL, clonePath)
	checkoutCommand := exec.Command("git", "checkout", c.Branch)
	fetchCommand := exec.Command("git", "fetch", "origin", fmt.Sprintf("pull/%d/head:pr", c.PRNumber))
	mergeCommand := exec.Command("git", "merge", "pr", "--no-edit")
	headCommand := exec.Command("git", "rev-parse", "pr")
	if err := runAndLogCommand(cloneCommand); err != nil {
		return false, "", err
	}
	checkoutCommand.Dir = clonePath
	if err := runAndLogCommand(checkoutCommand); err != nil {
		return false, "", err
	}
	fetchCommand.Dir = clonePath
	if err := runAndLogCommand(fetchCommand); err != nil {
		return false, "", err
	}
	headCommand.Dir = clonePath
	headBytes, err := headCommand.Output()
	if err != nil {
		return false, "", err
	}
	head := strings.TrimSpace(string(headBytes))
	mergeCommand.Dir = clonePath
	if err := runAndLogCommand(mergeCommand); err != nil {
		return false, head, nil
	}
	return true, head, nil
}

// uploadSource tars and uploads the repo to GCS.
func (c *testClient) uploadSource() error {
	tarName := fmt.Sprintf("%d.tar.gz", c.PRNumber)
	sourcePath := filepath.Join(c.Workspace, tarName)
	tar := exec.Command("tar", "czf", sourcePath, c.RepoName)
	tar.Dir = c.Workspace
	if err := runAndLogCommand(tar); err != nil {
		return fmt.Errorf("tar failed: %s", err)
	}
	tarFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("could not open tar: %s", err)
	}
	defer tarFile.Close()
	if err := c.GCSClient.Upload(tarFile, c.SourceBucket, tarName); err != nil {
		return fmt.Errorf("source upload failed: %s", err)
	}
	return nil
}

// startTests starts the tests in the tests YAML file within the repo.
func (c *testClient) startTests(head string) error {
	testPath := filepath.Join(c.Workspace, c.RepoName, ".test.yml")
	// If .test.yml doesn't exist, just quit here.
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		return nil
	}
	b, err := ioutil.ReadFile(testPath)
	if err != nil {
		return err
	}
	var tests []testDescription
	if err := yaml.Unmarshal(b, &tests); err != nil {
		return err
	}
	for _, test := range tests {
		// TODO: Validate the test.
		log.Printf("Test: %s", test.Name)
		if err := c.startTest(test, head); err != nil {
			return err
		}
	}
	return nil
}

// startTest starts a single test job.
func (c *testClient) startTest(test testDescription, head string) error {
	name := fmt.Sprintf("%s-pr-%d-%s", c.RepoName, c.PRNumber, test.Name)
	job := kube.Job{
		Metadata: kube.ObjectMeta{
			Name:      name,
			Namespace: c.Namespace,
			Labels: map[string]string{
				"repo": c.RepoName,
				"pr":   strconv.Itoa(c.PRNumber),
			},
		},
		Spec: kube.JobSpec{
			Template: kube.PodTemplateSpec{
				Spec: kube.PodSpec{
					RestartPolicy: "Never",
					Volumes: []kube.Volume{
						{
							Name: "oauth",
							Secret: &kube.SecretSource{
								Name: "oauth-token",
							},
						},
					},
					Containers: []kube.Container{
						{
							Name:  "run-test",
							Image: c.RunTestImage,
							Args: []string{
								"--repo-owner=" + c.RepoOwner,
								"--repo-name=" + c.RepoName,
								"--pr=" + strconv.Itoa(c.PRNumber),
								"--head=" + head,
								"--test-name=" + test.Name,
								"--test-image=" + test.Image,
								"--test-path=" + test.Path,
								"--timeout=" + test.Timeout,
								"--source-bucket=" + c.SourceBucket,
								"--dry-run=" + strconv.FormatBool(c.DryRun),
							},
							SecurityContext: &kube.SecurityContext{
								Privileged: true,
							},
							VolumeMounts: []kube.VolumeMount{
								{
									Name:      "oauth",
									ReadOnly:  true,
									MountPath: "/etc/oauth",
								},
							},
						},
					},
				},
			},
		},
	}
	j, err := c.KubeClient.CreateJob(job)
	log.Printf("Created job: %s", j.Metadata.Name)
	return err
}

func runAndLogCommand(cmd *exec.Cmd) error {
	log.Printf("Running: %s", strings.Join(cmd.Args, " "))
	b, err := cmd.CombinedOutput()
	if len(b) > 0 {
		log.Print(string(b))
	}
	return err
}
