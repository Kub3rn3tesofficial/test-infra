/*
Copyright 2019 The Kubernetes Authors.

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

package bumper

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

	imagebumper "k8s.io/test-infra/experiment/image-bumper/bumper"
	"k8s.io/test-infra/prow/config/secret"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/robots/pr-creator/updater"
)

const (
	prowPrefix      = "gcr.io/k8s-prow/"
	testImagePrefix = "gcr.io/k8s-testimages/"
	prowRepo        = "https://github.com/kubernetes/test-infra"
	testImageRepo   = prowRepo

	latestVersion          = "latest"
	upstreamVersion        = "upstream"
	upstreamStagingVersion = "upstream-staging"
	tagVersion             = "vYYYYMMDD-deadbeef"

	upstreamBranch           = "https://raw.githubusercontent.com/kubernetes/test-infra/master"
	prowRefConfigFile        = "config/prow/cluster/deck_deployment.yaml"
	prowStagingRefConfigFile = "config/prow-staging/cluster/deck_deployment.yaml"
)

var (
	tagRegexp = regexp.MustCompile("v[0-9]{8}-[a-f0-9]{6,9}")
)

type FileArrayFlag []string

func (af *FileArrayFlag) String() string {
	return fmt.Sprint(*af)
}

func (af *FileArrayFlag) Set(value string) error {
	for _, e := range strings.Split(value, ",") {
		fn := strings.TrimSpace(e)
		info, err := os.Stat(fn)
		if err != nil {
			return fmt.Errorf("error getting file info for %q", fn)
		}
		if info.IsDir() && !strings.HasSuffix(fn, string(os.PathSeparator)) {
			fn = fn + string(os.PathSeparator)
		}
		*af = append(*af, fn)
	}
	return nil
}

type Options struct {
	GitHubOrg     string
	GitHubRepo    string
	GitHubLogin   string
	GitHubToken   string
	GitName       string
	GitEmail      string
	OncallAddress string

	BumpProwImages bool
	BumpTestImages bool
	TargetVersion  string

	IncludedConfigPaths FileArrayFlag
	ExcludedConfigPaths FileArrayFlag
	ExtraFiles          FileArrayFlag

	SkipPullRequest bool
}

func validateOptions(o *Options) error {
	if !o.SkipPullRequest && o.GitHubToken == "" {
		return fmt.Errorf("--github-token is mandatory when --skip-pull-request is false")
	}
	if !o.SkipPullRequest && (o.GitHubOrg == "" || o.GitHubRepo == "") {
		return fmt.Errorf("--github-org and --github-repo are mandatory when --skip-pull-request is false")
	}
	if (o.GitEmail == "") != (o.GitName == "") {
		return fmt.Errorf("--git-name and --git-email must be specified together")
	}

	if o.TargetVersion != latestVersion && o.TargetVersion != upstreamVersion &&
		o.TargetVersion != upstreamStagingVersion && !tagRegexp.MatchString(o.TargetVersion) {
		return fmt.Errorf("invalid --target-version, must be one of %v",
			[]string{latestVersion, upstreamVersion, upstreamStagingVersion, tagVersion})
	}
	if !o.BumpProwImages && !o.BumpTestImages {
		return fmt.Errorf("at least one of --bump-prow-images and --bump-test-images must be specified")
	}
	if o.BumpProwImages && o.BumpTestImages && o.TargetVersion != latestVersion {
		return fmt.Errorf("--target-version must be latest if you want to bump both prow and test images")
	}
	if o.BumpTestImages && (o.TargetVersion == upstreamVersion || o.TargetVersion == upstreamStagingVersion) {
		return fmt.Errorf("%q and %q versions can only be specified to bump prow images", upstreamVersion, upstreamStagingVersion)
	}

	if len(o.IncludedConfigPaths) == 0 {
		return fmt.Errorf("--include-config-paths is mandatory")
	}

	return nil
}

func Run(o *Options) error {
	if err := validateOptions(o); err != nil {
		return fmt.Errorf("error validating options: %v", err)
	}

	if err := cdToRootDir(); err != nil {
		return fmt.Errorf("failed to change to root dir: %v", err)
	}

	images, err := updateReferences(
		o.BumpProwImages, o.BumpTestImages, o.TargetVersion,
		o.IncludedConfigPaths, o.ExcludedConfigPaths, o.ExtraFiles)
	if err != nil {
		return fmt.Errorf("failed to update image references: %v", err)
	}

	changed, err := hasChanges()
	if err != nil {
		return fmt.Errorf("error occurred when checking changes: %v", err)
	}

	if !changed {
		logrus.Info("no images updated, exiting ...")
		return nil
	}

	if o.SkipPullRequest {
		logrus.Debugf("--skip-pull-request is set to true, won't create a pull request.")
	} else {
		sa := &secret.Agent{}
		if err := sa.Start([]string{o.GitHubToken}); err != nil {
			return fmt.Errorf("failed to start secrets agent: %v", err)
		}

		gc := github.NewClient(sa.GetTokenGenerator(o.GitHubToken), sa.Censor, github.DefaultGraphQLEndpoint, github.DefaultAPIEndpoint)

		if o.GitHubLogin == "" || o.GitName == "" || o.GitEmail == "" {
			user, err := gc.BotUser()
			if err != nil {
				return fmt.Errorf("failed to get the user data for the provided GH token: %v", err)
			}
			if o.GitHubLogin == "" {
				o.GitHubLogin = user.Login
			}
			if o.GitName == "" {
				o.GitName = user.Name
			}
			if o.GitEmail == "" {
				o.GitEmail = user.Email
			}
		}

		remoteBranch := "autobump"
		stdout := hideSecretsWriter{delegate: os.Stdout, censor: sa}
		stderr := hideSecretsWriter{delegate: os.Stderr, censor: sa}
		if err := makeGitCommit(fmt.Sprintf("git@github.com:%s/test-infra.git", o.GitHubLogin), remoteBranch, o.GitName, o.GitEmail, images, stdout, stderr); err != nil {
			return fmt.Errorf("failed to push changes to the remote branch: %v", err)
		}

		if err := updatePR(gc, o.GitHubOrg, o.GitHubRepo, images, getAssignment(o.OncallAddress), "Update prow to", o.GitHubLogin+":"+remoteBranch, "master", updater.PreventMods); err != nil {
			return fmt.Errorf("failed to create the PR: %v", err)
		}
	}

	return nil
}

func cdToRootDir() error {
	if bazelWorkspace := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); bazelWorkspace != "" {
		if err := os.Chdir(bazelWorkspace); err != nil {
			return fmt.Errorf("failed to chdir to bazel workspace (%s): %v", bazelWorkspace, err)
		}
		return nil
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return err
	}
	d := strings.TrimSpace(string(output))
	logrus.Infof("Changing working directory to %s...", d)
	return os.Chdir(d)
}

func call(stdout, stderr io.Writer, cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Run()
}

type censor interface {
	Censor(content []byte) []byte
}

type hideSecretsWriter struct {
	delegate io.Writer
	censor   censor
}

func (w hideSecretsWriter) Write(content []byte) (int, error) {
	_, err := w.delegate.Write(w.censor.Censor(content))
	if err != nil {
		return 0, err
	}
	return len(content), nil
}

// UpdatePR updates with github client "gc" the PR of github repo org/repo
// with "matchTitle" from "source" to "branch"
// "images" contains the tag replacements that have been made which is returned from "UpdateReferences([]string{"."}, extraFiles)"
// "images" and "extraLineInPRBody" are used to generate commit summary and body of the PR
func updatePR(gc github.Client, org, repo string, images map[string]string, extraLineInPRBody string, matchTitle, source, branch string, allowMods bool) error {
	return updatePullRequest(gc, org, repo, makeCommitSummary(images), generatePRBody(images, extraLineInPRBody), matchTitle, source, branch, allowMods)
}

// UpdatePullRequest updates with github client "gc" the PR of github repo org/repo
// with "title" and "body" of PR matching "matchTitle" from "source" to "branch"
func updatePullRequest(gc github.Client, org, repo, title, body, matchTitle, source, branch string, allowMods bool) error {
	return updatePullRequestWithLabels(gc, org, repo, title, body, matchTitle, source, branch, allowMods, nil)
}

func updatePullRequestWithLabels(gc github.Client, org, repo, title, body, matchTitle, source, branch string, allowMods bool, labels []string) error {
	logrus.Info("Creating or updating PR...")
	n, err := updater.EnsurePRWithLabels(org, repo, title, body, source, branch, matchTitle, allowMods, gc, labels)
	if err != nil {
		return fmt.Errorf("failed to ensure PR exists: %v", err)
	}

	logrus.Infof("PR %s/%s#%d will merge %s into %s: %s", org, repo, *n, source, branch, title)
	return nil
}

// UpdateReferences update the references of prow-images and/or testimages
// in the files in any of "subfolders" of the includeConfigPaths but not in excludeConfigPaths
// if the file is a yaml file (*.yaml) or extraFiles[file]=true
func updateReferences(bumpProwImages, bumpTestImages bool, targetVersion string,
	includeConfigPaths []string, excludeConfigPaths []string, extraFiles []string) (map[string]string, error) {
	logrus.Info("Bumping image references...")
	filters := make([]string, 0)
	if bumpProwImages {
		filters = append(filters, prowPrefix)
	}
	if bumpTestImages {
		filters = append(filters, testImagePrefix)
	}
	filterRegexp := regexp.MustCompile(strings.Join(filters, "|"))

	var tagPicker func(string, string, string) (string, error)
	var err error
	switch targetVersion {
	case latestVersion:
		tagPicker = imagebumper.FindLatestTag
	case upstreamVersion:
		tagPicker, err = upstreamImageVersionResolver(upstreamBranch + "/" + prowRefConfigFile)
		if err != nil {
			return nil, err
		}
	case upstreamStagingVersion:
		tagPicker, err = upstreamImageVersionResolver(upstreamBranch + "/" + prowStagingRefConfigFile)
		if err != nil {
			return nil, err
		}
	default:
		tagPicker = func(imageHost, imageName, currentTag string) (string, error) { return tagVersion, nil }
	}

	updateFile := func(name string) error {
		fmt.Printf("Updating file %s\n", name)
		if err := imagebumper.UpdateFile(tagPicker, name, filterRegexp); err != nil {
			logrus.WithError(err).Errorf("Failed to update file %q.", name)
			return err
		}
		return nil
	}
	updateYAMLFile := func(name string) error {
		if strings.HasSuffix(name, ".yaml") && !isUnderPath(name, excludeConfigPaths) {
			return updateFile(name)
		}
		return nil
	}

	// Updated all .yaml files under the included config paths but not under excluded config paths.
	for _, path := range includeConfigPaths {
		info, err := os.Stat(path)
		if err != nil {
			logrus.WithError(err).Errorf("Error getting file info for %q.", path)
			return nil, err
		}
		if info.IsDir() {
			err := filepath.Walk(path, func(subpath string, info os.FileInfo, err error) error {
				return updateYAMLFile(subpath)
			})
			if err != nil {
				return nil, err
			}
		} else {
			if err := updateYAMLFile(path); err != nil {
				return nil, err
			}
		}
	}

	// Update the extra files in any case.
	for _, file := range extraFiles {
		if err := updateFile(file); err != nil {
			return nil, err
		}
	}

	return imagebumper.GetReplacements(), nil
}

func upstreamImageVersionResolver(upstreamAddress string) (func(imageHost, imageName, currentTag string) (string, error), error) {
	version, err := parseUpstreamImageVersion(upstreamAddress)
	if err != nil {
		return nil, fmt.Errorf("error resolving the upstream Prow version from %q: %v", upstreamAddress, err)
	}
	return func(imageHost, imageName, currentTag string) (string, error) {
		// Skip boskos images as they do not have the same image tag as other Prow components.
		// TODO(chizhg): remove this check after all Prow instances are using boskos images not in gcr.io/k8s-prow/boskos
		if strings.Contains(imageName, "boskos/") {
			return currentTag, nil
		}
		return version, nil
	}, nil
}

func parseUpstreamImageVersion(upstreamAddress string) (string, error) {
	resp, err := http.Get(upstreamAddress)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error %d (%q) fetching upstream config file", resp.StatusCode, resp.Status)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading the response body: %v", err)
	}
	imagematcher := regexp.MustCompile(`(?s)^.+image:.+:(v[a-zA-Z0-9_.-]+)`)
	res := imagematcher.FindStringSubmatch(string(body))
	return res[1], nil
}

func isUnderPath(name string, paths []string) bool {
	for _, p := range paths {
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func getNewProwVersion(images map[string]string) string {
	for k, v := range images {
		if strings.HasPrefix(k, prowPrefix) {
			return v
		}
	}
	return ""
}

// hasChanges checks if the current git repo contains any changes
func hasChanges() (bool, error) {
	cmd := "git"
	args := []string{"status", "--porcelain"}
	logrus.WithField("cmd", cmd).WithField("args", args).Info("running command ...")
	combinedOutput, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		logrus.WithField("cmd", cmd).Debugf("output is '%s'", string(combinedOutput))
		return false, err
	}
	return len(strings.TrimSuffix(string(combinedOutput), "\n")) > 0, nil
}

func makeCommitSummary(images map[string]string) string {
	return fmt.Sprintf("Update prow to %s, and other images as necessary.", getNewProwVersion(images))
}

// makeGitCommit runs a sequence of git commands to
// commit and push the changes the "remote" on "remoteBranch"
// "name" and "email" are used for git-commit command
// "images" contains the tag replacements that have been made which is returned from "UpdateReferences([]string{"."}, extraFiles)"
// "images" is used to generate commit message
func makeGitCommit(remote, remoteBranch, name, email string, images map[string]string, stdout, stderr io.Writer) error {
	return gitCommitAndPush(remote, remoteBranch, name, email, makeCommitSummary(images), stdout, stderr)
}

// gitCommitAndPush runs a sequence of git commands to commit.
// The "name", "email", and "message" are used for git-commit command
func gitCommitAndPush(remote, remoteBranch, name, email, message string, stdout, stderr io.Writer) error {
	logrus.Info("Making git commit...")

	if err := call(stdout, stderr, "git", "add", "-A"); err != nil {
		return fmt.Errorf("failed to git add: %v", err)
	}
	commitArgs := []string{"commit", "-m", message}
	if name != "" && email != "" {
		commitArgs = append(commitArgs, "--author", fmt.Sprintf("%s <%s>", name, email))
	}
	if err := call(stdout, stderr, "git", commitArgs...); err != nil {
		return fmt.Errorf("failed to git commit: %v", err)
	}
	if err := gitPush(remote, remoteBranch, stdout, stderr); err != nil {
		return fmt.Errorf("%v", err)
	}
	return nil
}

// gitPush push the changes to the given remote and branch.
func gitPush(remote, remoteBranch string, stdout, stderr io.Writer) error {
	logrus.Info("Pushing to remote...")
	if err := call(stdout, stderr, "git", "push", "-f", remote, fmt.Sprintf("HEAD:%s", remoteBranch)); err != nil {
		return fmt.Errorf("failed to git push: %v", err)
	}
	return nil
}

func tagFromName(name string) string {
	parts := strings.Split(name, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func componentFromName(name string) string {
	s := strings.SplitN(strings.Split(name, ":")[0], "/", 3)
	return s[len(s)-1]
}

func formatTagDate(d string) string {
	if len(d) != 8 {
		return d
	}
	// &#x2011; = U+2011 NON-BREAKING HYPHEN, to prevent line wraps.
	return fmt.Sprintf("%s&#x2011;%s&#x2011;%s", d[0:4], d[4:6], d[6:8])
}

func generateSummary(name, repo, prefix string, summarise bool, images map[string]string) string {
	type delta struct {
		oldCommit string
		newCommit string
		oldDate   string
		newDate   string
		variant   string
		component string
	}
	versions := map[string][]delta{}
	for image, newTag := range images {
		if !strings.HasPrefix(image, prefix) {
			continue
		}
		if strings.HasSuffix(image, ":"+newTag) {
			continue
		}
		oldDate, oldCommit, oldVariant := imagebumper.DeconstructTag(tagFromName(image))
		newDate, newCommit, _ := imagebumper.DeconstructTag(newTag)
		k := oldCommit + ":" + newCommit
		d := delta{
			oldCommit: oldCommit,
			newCommit: newCommit,
			oldDate:   oldDate,
			newDate:   newDate,
			variant:   oldVariant,
			component: componentFromName(image),
		}
		versions[k] = append(versions[k], d)
	}

	switch {
	case len(versions) == 0:
		return fmt.Sprintf("No %s changes.", name)
	case len(versions) == 1 && summarise:
		for k, v := range versions {
			s := strings.Split(k, ":")
			return fmt.Sprintf("%s changes: %s/compare/%s...%s (%s → %s)", name, repo, s[0], s[1], formatTagDate(v[0].oldDate), formatTagDate(v[0].newDate))
		}
	default:
		changes := make([]string, 0, len(versions))
		for k, v := range versions {
			s := strings.Split(k, ":")
			names := make([]string, 0, len(v))
			for _, d := range v {
				names = append(names, d.component+d.variant)
			}
			sort.Strings(names)
			changes = append(changes, fmt.Sprintf("%s/compare/%s...%s | %s&nbsp;&#x2192;&nbsp;%s | %s",
				repo, s[0], s[1], formatTagDate(v[0].oldDate), formatTagDate(v[0].newDate), strings.Join(names, ", ")))
		}
		sort.Slice(changes, func(i, j int) bool { return strings.Split(changes[i], "|")[1] < strings.Split(changes[j], "|")[1] })
		return fmt.Sprintf("Multiple distinct %s changes:\n\nCommits | Dates | Images\n--- | --- | ---\n%s\n", name, strings.Join(changes, "\n"))
	}
	panic("unreachable!")
}

func generatePRBody(images map[string]string, assignment string) string {
	prowSummary := generateSummary("Prow", prowRepo, prowPrefix, true, images)
	testImagesSummary := generateSummary("test-image", testImageRepo, testImagePrefix, false, images)
	return prowSummary + "\n\n" + testImagesSummary + "\n\n" + assignment + "\n"
}

func getAssignment(oncallAddress string) string {
	if oncallAddress == "" {
		return ""
	}
	oncaller, err := getOncaller(oncallAddress)
	if err == nil {
		if oncaller != "" {
			return "/cc @" + oncaller
		}
		return "Nobody is currently oncall, so falling back to Blunderbuss."
	}
	return fmt.Sprintf("An error occurred while finding an assignee: `%s`.\nFalling back to Blunderbuss.", err)
}

func getOncaller(oncallAddress string) (string, error) {
	req, err := http.Get(oncallAddress)
	if err != nil {
		return "", err
	}
	defer req.Body.Close()
	if req.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error %d (%q) fetching current oncaller", req.StatusCode, req.Status)
	}
	oncall := struct {
		Oncall struct {
			TestInfra string `json:"testinfra"`
		} `json:"Oncall"`
	}{}
	if err := json.NewDecoder(req.Body).Decode(&oncall); err != nil {
		return "", err
	}
	return oncall.Oncall.TestInfra, nil
}
