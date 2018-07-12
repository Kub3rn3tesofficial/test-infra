/*
Copyright 2018 The Kubernetes Authors.

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

// Package spyglass creates views for Prow job artifacts
package spyglass

import (
	"fmt"
	"sort"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/deck/jobs"
	"k8s.io/test-infra/prow/spyglass/viewers"
)

// Spyglass records which sets of artifacts need views for a prow job
type Spyglass struct {

	// map of names of views to their corresponding lenses
	Lenses map[string]Lens

	// Job Agent for spyglass
	Ja *jobs.JobAgent
}

// Lens is a single view of a set of artifacts
type Lens struct {
	// Name of the view, unique within a job
	Name string
	// Title of view
	Title string
	// Html rendering of the view
	HTMLView string
	// Priority is the relative position of the view on the page
	Priority int
}

// ViewRequest holds data sent by a view
type ViewRequest struct {
	ViewName    string              `json:"name"`
	ViewerCache map[string][]string `json:"viewerCache"`
	ViewData    string              `json:"viewData"`
}

// NewSpyglass constructs a spyglass object from a JobAgent
func NewSpyglass(ja *jobs.JobAgent) *Spyglass {
	return &Spyglass{
		Lenses: make(map[string]Lens),
		Ja:     ja,
	}
}

// Views gets all views of all artifact files matching each regexp with a registered viewer
func (s *Spyglass) Views(eyepiece map[string][]string) []Lens {
	lenses := []Lens{}
	for viewer := range eyepiece {
		title, err := viewers.Title(viewer)
		if err != nil {
			logrus.Error("Could not find artifact viewer with name ", viewer)
		}
		priority, err := viewers.Priority(viewer)
		if err != nil {
			logrus.Error("Could not find artifact viewer with name ", viewer)
		}
		lens := Lens{
			Name:     viewer,
			Title:    title,
			HTMLView: "",
			Priority: priority,
		}
		lenses = append(lenses, lens)
		s.Lenses[viewer] = lens
	}
	sort.Slice(lenses, func(i, j int) bool {
		iname := lenses[i].Name
		jname := lenses[j].Name
		pi := lenses[i].Priority
		pj := lenses[j].Priority
		if pi == pj {
			return iname < jname
		}
		return pi < pj
	})
	return lenses
}

// Refresh reloads the html view for a given set of objects
func (s *Spyglass) Refresh(src string, jobID string, viewReq *ViewRequest) Lens {
	lens, ok := s.Lenses[viewReq.ViewName]
	if !ok {
		logrus.Errorf("Could not find Lens with name %s.", viewReq.ViewName)
		return Lens{}
	}
	artifacts, err := s.FetchArtifacts(src, jobID, viewReq.ViewerCache[viewReq.ViewName])
	if err != nil {
		logrus.WithError(err).Error("Error while fetching artifacts.")
	}

	view, err := viewers.View(viewReq.ViewName, artifacts, viewReq.ViewData)
	if err != nil {
		logrus.WithError(err).Error("Could not find a valid artifact viewer.")
		return Lens{}
	}
	lens.HTMLView = view
	return lens
}

// ListArtifacts handles muxing artifact sources to the correct fetcher implementations to list
// all available artifact names
func (s *Spyglass) ListArtifacts(src string, jobID string) ([]string, error) {
	artifacts := []string{}
	var jobName string
	// First check src
	if isGCSSource(src) {
		artifactFetcher := NewGCSArtifactFetcher()
		gcsJobSource := NewGCSJobSource(src)
		jobName = gcsJobSource.JobName()
		if jobID == "" {
			jobID = gcsJobSource.JobID()
		}

		artStart := time.Now()
		artifacts = append(artifacts, artifactFetcher.Artifacts(gcsJobSource)...)
		artElapsed := time.Since(artStart)
		logrus.Info("Retrieved GCS artifacts in ", artElapsed)

	} else {
		return []string{}, fmt.Errorf("Invalid source: %s", src)
	}

	// Then check prowjob id for pod logs, pod spec, etc
	if jobID != "" && jobName != "" {
		podLog := NewPodLogArtifact(jobName, jobID, s.Ja)
		if podLog.Size() != -1 {
			artifacts = append(artifacts, podLog.JobPath())
		}

	}
	return artifacts, nil
}

// FetchArtifacts creates artifact objects for each artifact name in the list
func (s *Spyglass) FetchArtifacts(src string, jobID string, artifactNames []string) ([]viewers.Artifact, error) {
	artifacts := []viewers.Artifact{}
	var jobName string
	// First check src
	if isGCSSource(src) {
		artifactFetcher := NewGCSArtifactFetcher()
		gcsJobSource := NewGCSJobSource(src)
		jobName = gcsJobSource.JobName()
		if jobID == "" {
			jobID = gcsJobSource.JobID()
		}

		artStart := time.Now()
		for _, name := range artifactNames {
			artifacts = append(artifacts, artifactFetcher.Artifact(gcsJobSource, name))
		}
		artElapsed := time.Since(artStart)
		logrus.Info("Retrieved GCS artifacts in ", artElapsed)

	} else {
		return []viewers.Artifact{}, fmt.Errorf("Invalid source: %s", src)
	}

	// Then check prowjob id for pod logs, pod spec, etc
	if jobID != "" && jobName != "" {
		logrus.Info("Trying pod logs. ")
		podLog := NewPodLogArtifact(jobName, jobID, s.Ja)
		if podLog.Size() != -1 {
			artifacts = append(artifacts, podLog)
		}

	}
	return artifacts, nil

}
