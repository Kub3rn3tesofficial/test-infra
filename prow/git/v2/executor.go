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

package git

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

// Executor knows how to execute Git commands
type Executor interface {
	Run(args ...string) ([]byte, error)
}

func NewCensoringExecutor(dir string, censor func(content []byte) []byte, logger *logrus.Entry) (Executor, error) {
	g, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	return &censoringExecutor{
		logger: logger.WithField("client", "git"),
		dir:    dir,
		git:    g,
		censor: censor,
		execute: func(dir, command string, args ...string) ([]byte, error) {
			c := exec.Command(command, args...)
			c.Dir = dir
			return c.CombinedOutput()
		},
	}, nil
}

type censoringExecutor struct {
	// logger will be used to log git operations
	logger *logrus.Entry
	// dir is the location of this repo.
	dir string
	// git is the path to the git binary.
	git string
	// censor removes sensitive data from output
	censor func(content []byte) []byte
	// execute executes a command
	execute func(dir, command string, args ...string) ([]byte, error)
}

func (e *censoringExecutor) Run(args ...string) ([]byte, error) {
	logger := e.logger.WithField("args", strings.Join(args, " "))
	b, err := e.execute(e.dir, e.git, args...)
	b = e.censor(b)
	if err != nil {
		logger.WithError(err).WithField("output", string(b)).Warning("Running command failed.")
	} else {
		logger.Debug("Running command succeeded.")
	}
	return b, err
}

type response struct {
	out []byte
	err error
}

// fakeExecutor is useful in testing for mocking an Executor
type fakeExecutor struct {
	records   [][]string
	responses map[string]response
}

func (e *fakeExecutor) Run(args ...string) ([]byte, error) {
	e.records = append(e.records, args)
	key := strings.Join(args, " ")
	if response, ok := e.responses[key]; ok {
		return response.out, response.err
	}
	return []byte{}, fmt.Errorf("no response configured for %s", key)
}
