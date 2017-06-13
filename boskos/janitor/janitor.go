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

package main

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/Sirupsen/logrus"
	"k8s.io/test-infra/boskos/client"
)

var (
	clean      = janitorClean
	POOLSIZE   = 10 // Maximum concurrent janitor goroutines
	BUFFERSIZE = 1  // Maximum holding resources
)

func main() {
	logrus.SetFormatter(&logrus.JSONFormatter{})
	boskos := client.NewClient("Janitor", "http://boskos")
	logrus.Info("Initialized boskos client!")
	buffer := setup(boskos, POOLSIZE, BUFFERSIZE)
	for {
		run(boskos, buffer)
		time.Sleep(time.Minute)
	}
}

// Clean by janitor script
func janitorClean(proj string) error {
	script := "../../jenkins/janitor.py"
	return exec.Command(script, fmt.Sprintf("--project=%s", proj), "--hour=0").Run()
}

type boskosClient interface {
	Acquire(rtype string, state string, dest string) (string, error)
	ReleaseOne(name string, dest string) error
}

func setup(c boskosClient, janitorCount int, bufferSize int) chan string {
	buffer := make(chan string, 1)
	for i := 0; i < janitorCount; i++ {
		go janitor(c, buffer)
	}
	return buffer
}

func run(c boskosClient, buffer chan string) int {
	totalAcquire := 0

	for {
		if proj, err := c.Acquire("project", "dirty", "cleaning"); err != nil {
			logrus.WithError(err).Error("Boskos acquire failed!")
			return totalAcquire
		} else if proj == "" {
			return totalAcquire
		} else {
			buffer <- proj // will block until buffer has a free slot
			totalAcquire++
		}
	}
}

// async janitor goroutine
func janitor(c boskosClient, buffer chan string) {
	for {
		proj := <-buffer

		dest := "free"
		if err := clean(proj); err != nil {
			logrus.WithError(err).Error("janitor.py failed!")
			dest = "dirty"
		}

		if err := c.ReleaseOne(proj, dest); err != nil {
			logrus.WithError(err).Error("Boskos release failed!")
		}
	}
}
