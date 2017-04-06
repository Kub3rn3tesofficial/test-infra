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

package plank

import (
	"fmt"
	"time"

	"k8s.io/test-infra/prow/kube"
	"k8s.io/test-infra/prow/line"
)

type kubeClient interface {
	ListJobs(map[string]string) ([]kube.Job, error)
	ListProwJobs(map[string]string) ([]kube.ProwJob, error)
	ReplaceProwJob(string, kube.ProwJob) (kube.ProwJob, error)
	CreateJob(kube.Job) (kube.Job, error)
	GetJob(name string) (kube.Job, error)
	PatchJob(name string, job kube.Job) (kube.Job, error)
	PatchJobStatus(name string, job kube.Job) (kube.Job, error)
}

type Controller struct {
	kc kubeClient
}

func NewController(kc *kube.Client) *Controller {
	return &Controller{
		kc: kc,
	}
}

func (c *Controller) Sync() error {
	pjs, err := c.kc.ListProwJobs(nil)
	if err != nil {
		return fmt.Errorf("error listing prow jobs: %v", err)
	}
	js, err := c.kc.ListJobs(nil)
	if err != nil {
		return fmt.Errorf("error listing jobs: %v", err)
	}
	jm := map[string]*kube.Job{}
	for _, j := range js {
		jm[j.Metadata.Name] = &j
	}
	var errs []error
	for _, pj := range pjs {
		if err := c.syncJob(pj, jm); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	} else {
		return fmt.Errorf("errors syncing: %v", errs)
	}
}

func (c *Controller) syncJob(pj kube.ProwJob, jm map[string]*kube.Job) error {
	if pj.Complete() {
		if j, ok := jm[pj.Status.KubeJobName]; ok && pj.Spec.Type == kube.PresubmitJob && !j.Complete() {
			// Complete prow job, incomplete k8s job, abort it.
			// TODO(spxtr): We currently only abort presubmits. We should
			// consider aborting other kinds of jobs.
			if len(pj.Spec.Refs.Pulls) != 1 {
				return fmt.Errorf("prowjob %s has wrong number of pulls: %+v", pj.Metadata.Name, pj.Spec.Refs)
			}
			return line.DeleteJob(c.kc, pj.Spec.Job, pj.Spec.Refs.Org, pj.Spec.Refs.Repo, pj.Spec.Refs.Pulls[0].Number)
		}
		return nil
	}
	if pj.Status.KubeJobName == "" {
		// Start job.
		name, err := c.startJob(pj)
		if err != nil {
			return err
		}
		pj.Status.KubeJobName = name
		pj.Status.State = kube.PendingState
		pj.Status.StartTime = time.Now()
		if _, err := c.kc.ReplaceProwJob(pj.Metadata.Name, pj); err != nil {
			return err
		}
	} else if j, ok := jm[pj.Status.KubeJobName]; !ok {
		return fmt.Errorf("kube job %s not found", pj.Status.KubeJobName)
	} else if j.Complete() {
		// Kube job finished, update prow job.
		pj.Status.State = kube.ProwJobState(j.Metadata.Annotations["state"])
		pj.Status.CompletionTime = j.Status.CompletionTime
		if _, err := c.kc.ReplaceProwJob(pj.Metadata.Name, pj); err != nil {
			return err
		}
	} else {
		// Kube job still running. Nothing to do.
	}
	return nil
}

func (c *Controller) startJob(pj kube.ProwJob) (string, error) {
	switch pj.Spec.Type {
	case kube.PresubmitJob:
		return line.StartJob(c.kc, pj.Spec.Job, pj.Spec.Context, pjToBR(pj))
	case kube.PostsubmitJob:
		return line.StartJob(c.kc, pj.Spec.Job, "", pjToBR(pj))
	case kube.PeriodicJob:
		return line.StartPeriodicJob(c.kc, pj.Spec.Job)
	case kube.BatchJob:
		return line.StartJob(c.kc, pj.Spec.Job, "", pjToBR(pj))
	}
	return "", fmt.Errorf("unhandled job type: %s", pj.Spec.Type)
}

func pjToBR(pj kube.ProwJob) line.BuildRequest {
	br := line.BuildRequest{
		Org:     pj.Spec.Refs.Org,
		Repo:    pj.Spec.Refs.Repo,
		BaseRef: pj.Spec.Refs.BaseRef,
		BaseSHA: pj.Spec.Refs.BaseSHA,
	}
	for _, p := range pj.Spec.Refs.Pulls {
		br.Pulls = append(br.Pulls, line.Pull{
			Author: p.Author,
			Number: p.Number,
			SHA:    p.SHA,
		})
	}
	return br
}
