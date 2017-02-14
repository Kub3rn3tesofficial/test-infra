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

package jobs

import (
	"fmt"
	"io/ioutil"
	"regexp"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/ghodss/yaml"

	"k8s.io/test-infra/prow/kube"
)

// Brancher is for shared code between jobs that only run against certain
// branches. An empty brancher runs against all branches.
type Brancher struct {
	// Do not run against these branches. Default is no branches.
	SkipBranches []string `json:"skip_branches"`
	// Only run against these branches. Default is all branches.
	Branches []string `json:"branches"`
}

// Presubmit is the job-specific trigger info.
type Presubmit struct {
	// eg kubernetes-pull-build-test-e2e-gce
	Name string `json:"name"`
	// Run for every PR, or only when a comment triggers it.
	AlwaysRun bool `json:"always_run"`
	// Run if the PR modifies a file that matches this regex.
	RunIfChanged string `json:"run_if_changed"`
	// Context line for GitHub status.
	Context string `json:"context"`
	// eg @k8s-bot e2e test this
	Trigger string `json:"trigger"`
	// Valid rerun command to give users. Must match Trigger.
	RerunCommand string `json:"rerun_command"`
	// Whether or not to skip commenting and setting status on GitHub.
	SkipReport bool `json:"skip_report"`
	// Kubernetes pod spec.
	Spec *kube.PodSpec `json:"spec,omitempty"`
	// Run these jobs after successfully running this one.
	RunAfterSuccess []Presubmit `json:"run_after_success"`

	Brancher

	// We'll set these when we load it.
	re        *regexp.Regexp // from RerunCommand
	reChanges *regexp.Regexp // from RunIfChanged
}

// Postsubmit runs on push events.
type Postsubmit struct {
	Name string        `json:"name"`
	Spec *kube.PodSpec `json:"spec,omitempty"`

	Brancher

	RunAfterSuccess []Postsubmit `json:"run_after_success"`
}

// Cron runs on a timer.
type Cron struct {
	Name     string        `json:"name"`
	Spec     *kube.PodSpec `json:"spec,omitempty"`
	Duration string        `json:"duration"`

	duration time.Duration
}

func (br Brancher) RunsAgainstBranch(branch string) bool {
	// Favor SkipBranches over Branches
	if len(br.SkipBranches) == 0 && len(br.Branches) == 0 {
		return true
	}

	for _, s := range br.SkipBranches {
		if s == branch {
			return false
		}
	}
	if len(br.Branches) == 0 {
		return true
	}
	for _, b := range br.Branches {
		if b == branch {
			return true
		}
	}
	return false
}

func (ps Presubmit) RunsAgainstChanges(changes []string) bool {
	for _, change := range changes {
		if ps.reChanges.MatchString(change) {
			return true
		}
	}
	return false
}

type JobAgent struct {
	mut sync.Mutex
	// Repo FullName (eg "kubernetes/kubernetes") -> []Job
	presubmits  map[string][]Presubmit
	postsubmits map[string][]Postsubmit
	crons       map[string][]Cron
}

func (ja *JobAgent) Start(pre, post, cron string) error {
	if err := ja.LoadOnce(pre, post, cron); err != nil {
		return err
	}
	ticker := time.Tick(1 * time.Minute)
	go func() {
		for range ticker {
			ja.tryLoad(pre, post, cron)
		}
	}()
	return nil
}

func (ja *JobAgent) AllJobNames() []string {
	var listPres func(ps []Presubmit) []string
	var listPost func(ps []Postsubmit) []string
	listPres = func(ps []Presubmit) []string {
		res := []string{}
		for _, p := range ps {
			res = append(res, p.Name)
			res = append(res, listPres(p.RunAfterSuccess)...)
		}
		return res
	}
	listPost = func(ps []Postsubmit) []string {
		res := []string{}
		for _, p := range ps {
			res = append(res, p.Name)
			res = append(res, listPost(p.RunAfterSuccess)...)
		}
		return res
	}
	ja.mut.Lock()
	defer ja.mut.Unlock()
	res := []string{}
	for _, v := range ja.presubmits {
		res = append(res, listPres(v)...)
	}
	for _, v := range ja.postsubmits {
		res = append(res, listPost(v)...)
	}
	for _, v := range ja.crons {
		for _, j := range v {
			res = append(res, j.Name)
		}
	}
	return res
}

func (ja *JobAgent) SetPresubmits(jobs map[string][]Presubmit) error {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	nj := map[string][]Presubmit{}
	for k, v := range jobs {
		nj[k] = make([]Presubmit, len(v))
		copy(nj[k], v)
		for i := range v {
			if re, err := regexp.Compile(v[i].Trigger); err != nil {
				return err
			} else {
				nj[k][i].re = re
			}
		}
	}
	ja.presubmits = nj
	return nil
}

func (ja *JobAgent) LoadOnce(pre, post, cron string) error {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	if err := ja.loadPresubmits(pre); err != nil {
		return err
	}
	if err := ja.loadPostsubmits(post); err != nil {
		return err
	}
	return ja.loadCrons(cron)
}

func (ja *JobAgent) MatchingPresubmits(fullRepoName, body string, testAll *regexp.Regexp) []Presubmit {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	var result []Presubmit
	ott := testAll.MatchString(body)
	if jobs, ok := ja.presubmits[fullRepoName]; ok {
		for _, job := range jobs {
			if job.re.MatchString(body) || (ott && job.AlwaysRun) {
				result = append(result, job)
			}
		}
	}
	return result
}

func (ja *JobAgent) AllPresubmits(fullRepoName string) []Presubmit {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	res := make([]Presubmit, len(ja.presubmits[fullRepoName]))
	copy(res, ja.presubmits[fullRepoName])
	return res
}

func (ja *JobAgent) GetPresubmit(repo, job string) (bool, Presubmit) {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	return getPresubmit(ja.presubmits[repo], job)
}

func getPresubmit(jobs []Presubmit, job string) (bool, Presubmit) {
	for _, j := range jobs {
		if j.Name == job {
			return true, j
		}
		if found, p := getPresubmit(j.RunAfterSuccess, job); found {
			return true, p
		}
	}
	return false, Presubmit{}
}

func (ja *JobAgent) AllPostsubmits(fullRepoName string) []Postsubmit {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	res := make([]Postsubmit, len(ja.postsubmits[fullRepoName]))
	copy(res, ja.postsubmits[fullRepoName])
	return res
}

func (ja *JobAgent) GetPostsubmit(repo, job string) (bool, Postsubmit) {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	return getPostsubmit(ja.postsubmits[repo], job)
}

func getPostsubmit(jobs []Postsubmit, job string) (bool, Postsubmit) {
	for _, j := range jobs {
		if j.Name == job {
			return true, j
		}
		if found, p := getPostsubmit(j.RunAfterSuccess, job); found {
			return true, p
		}
	}
	return false, Postsubmit{}
}

// Hold the lock.
func (ja *JobAgent) loadPresubmits(path string) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	nj := map[string][]Presubmit{}
	if err := yaml.Unmarshal(b, &nj); err != nil {
		return err
	}
	for _, v := range nj {
		if err := setRegexes(v); err != nil {
			return err
		}
	}
	ja.presubmits = nj
	return nil
}

func setRegexes(js []Presubmit) error {
	for i, j := range js {
		if re, err := regexp.Compile(j.Trigger); err == nil {
			js[i].re = re
		} else {
			return err
		}
		if err := setRegexes(j.RunAfterSuccess); err != nil {
			return err
		}
		if j.RunIfChanged != "" {
			if re, err := regexp.Compile(j.RunIfChanged); err != nil {
				return err
			} else {
				js[i].reChanges = re
			}
		}
	}
	return nil
}

// Hold the lock.
func (ja *JobAgent) loadPostsubmits(path string) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	nj := map[string][]Postsubmit{}
	if err := yaml.Unmarshal(b, &nj); err != nil {
		return err
	}
	ja.postsubmits = nj
	return nil
}

// Hold the lock.
func (ja *JobAgent) loadCrons(path string) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	nj := map[string][]Cron{}
	if err := yaml.Unmarshal(b, &nj); err != nil {
		return err
	}
	for _, js := range nj {
		for j := range js {
			d, err := time.ParseDuration(js[j].Duration)
			if err != nil {
				return fmt.Errorf("cannot parse duration for %s: %v", js[j].Name, err)
			}
			js[j].duration = d
		}
	}
	ja.crons = nj
	return nil
}

func (ja *JobAgent) tryLoad(pre, post, cron string) {
	ja.mut.Lock()
	defer ja.mut.Unlock()
	if err := ja.loadPresubmits(pre); err != nil {
		logrus.WithField("path", pre).WithError(err).Error("Error loading presubmits.")
	}
	if err := ja.loadPostsubmits(post); err != nil {
		logrus.WithField("path", post).WithError(err).Error("Error loading postsubmits.")
	}
	if err := ja.loadCrons(cron); err != nil {
		logrus.WithField("path", cron).WithError(err).Error("Error loading crons.")
	}
}
