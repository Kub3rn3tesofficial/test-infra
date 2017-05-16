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

type semaphore chan bool

// acquire 1 resources
func (s semaphore) P() {
	s <- true
}

// release 1 resources
func (s semaphore) V() {
	<-s
}

var (
	clean       = janitorClean
	janitorPool = make(semaphore, 10)
)

func main() {
	logrus.SetFormatter(&logrus.JSONFormatter{})
	boskos := client.NewClient("Janitor", "http://boskos")
	logrus.Info("Initialized boskos client!")
	for {
		if proj, err := boskos.Acquire("project", "dirty", "cleaning"); err != nil {
			logrus.WithError(err).Error("Boskos acquire failed!")
			time.Sleep(time.Minute)
		} else if proj == "" {
			time.Sleep(time.Minute)
		} else {
			go janitor(boskos, proj)
		}
	}
}

// Clean by janitor script
func janitorClean(proj string) error {
	script := "../../jenkins/janitor.py"
	return exec.Command(fmt.Sprintf("%s --project=%s --hour=0", script, proj)).Run()
}

type boskosClient interface {
	Acquire(rtype string, state string, dest string) (string, error)
	ReleaseOne(name string, dest string) error
}

// async janitor goroutine
func janitor(c boskosClient, proj string) {
	janitorPool.P()

	dest := "free"
	if err := clean(proj); err != nil {
		logrus.WithError(err).Error("janitor.py failed!")
		dest = "dirty"
	}

	if err := c.ReleaseOne(proj, dest); err != nil {
		logrus.WithError(err).Error("Boskos release failed!")
	}

	janitorPool.V()
}
