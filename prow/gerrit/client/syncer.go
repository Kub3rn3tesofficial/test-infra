/*
Copyright 2021 The Kubernetes Authors.

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

// Package client implements client that interacts with gerrit instances
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/io"
)

// opener has methods to read and write paths
type opener interface {
	Reader(ctx context.Context, path string) (io.ReadCloser, error)
	Writer(ctx context.Context, path string, opts ...io.WriterOptions) (io.WriteCloser, error)
}

type SyncTime struct {
	val       LastSyncState
	lock      sync.RWMutex
	path      string
	opener    opener
	ctx       context.Context
	stateChan chan struct {
		lastSyncState LastSyncState
		forceFlush    bool
	}
	waitChan       chan struct{}
	uploadInterval time.Duration
}

func NewSyncTime(path string, opener opener, ctx context.Context) *SyncTime {
	return &SyncTime{
		path:           path,
		opener:         opener,
		ctx:            ctx,
		uploadInterval: time.Minute,
	}
}

func (st *SyncTime) Init(hostProjects ProjectsFlag) error {
	logrus.WithField("projects", hostProjects).Info(st.val)
	return st.loadFromConfig(ProjectsFlagToConfig(hostProjects))
}

func (st *SyncTime) loadFromConfig(hostProjects map[string]map[string]*config.GerritQueryFilter) error {
	timeNow := time.Now()
	st.lock.Lock()
	defer st.lock.Unlock()
	var state LastSyncState
	var err error
	if st.val == nil {
		state, err = st.currentState()
		if err != nil {
			return err
		}
	} else {
		state = st.val
	}

	if state != nil {
		// Initialize new hosts, projects
		for host, projects := range hostProjects {
			if _, ok := state[host]; !ok {
				state[host] = map[string]time.Time{}
			}
			for project := range projects {
				if _, ok := state[host][project]; !ok {
					state[host][project] = timeNow
				}
			}
		}
		st.val = state
		logrus.WithField("lastSync", st.val).Debug("Initialized successfully from lastSyncFallback.")
	} else {
		targetState := LastSyncState{}
		for host, projects := range hostProjects {
			targetState[host] = map[string]time.Time{}
			for project := range projects {
				targetState[host][project] = timeNow
			}
		}
		st.val = targetState
	}
	return nil
}

func (st *SyncTime) currentState() (LastSyncState, error) {
	r, err := st.opener.Reader(st.ctx, st.path)
	if io.IsNotExist(err) {
		logrus.Warnf("lastSyncFallback not found at %q", st.path)
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer io.LogClose(r)
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var state LastSyncState
	if err := json.Unmarshal(buf, &state); err != nil {
		// Don't error on unmarshall error, let it default
		logrus.WithField("lastSync", st.val).Warnln("Failed to unmarshal lastSyncFallback, resetting all last update times to current.")
		return nil, nil
	}
	return state, nil
}

func (st *SyncTime) Current() LastSyncState {
	st.lock.RLock()
	defer st.lock.RUnlock()
	return st.val
}

func (st *SyncTime) Update(newState LastSyncState, force bool) error {
	st.lock.Lock()
	targetState := st.val.DeepCopy()

	var changed bool
	for host, newLastSyncs := range newState {
		if _, ok := targetState[host]; !ok {
			targetState[host] = map[string]time.Time{}
		}
		for project, newLastSync := range newLastSyncs {
			currentLastSync, ok := targetState[host][project]
			if !ok || currentLastSync.Before(newLastSync) {
				targetState[host][project] = newLastSync
				changed = true
			}
		}
	}

	st.val = targetState

	// Pediodically sync time back to storage instead of writing after every
	// single project is processed, as the I/O would become performance
	// bottleneck when there are lots of projects.
	// Use stateChan to ensure that there is only a single goroutine.
	if st.stateChan == nil {
		st.stateChan = make(chan struct {
			lastSyncState LastSyncState
			forceFlush    bool
		})
		st.waitChan = make(chan struct{})
		go func() {
			ticker := time.NewTicker(st.uploadInterval)
			defer ticker.Stop()

			var curState *LastSyncState
			for {
				select {
				case newState := <-st.stateChan:
					// save the state, if force flush is false then the state
					// will be flushed at time ticks later.
					curState = &newState.lastSyncState
					// Allows caller to force flush instead of waiting for the
					// ticks. This useful for cases like interruptions, that an
					// emergency flushing is supposed to happen right away.
					if !newState.forceFlush {
						continue
					}
					st.uploadOnce(*curState)
					curState = nil
					st.waitChan <- struct{}{}
				case <-ticker.C:
					// curState is reset after uploads, skip uploading if it's
					// not updated.
					if curState == nil {
						continue
					}
					st.uploadOnce(*curState)
					curState = nil
				}
			}
		}()
	}

	st.lock.Unlock()

	if changed || force {
		st.stateChan <- struct {
			lastSyncState LastSyncState
			forceFlush    bool
		}{targetState, force}
	}

	if force {
		// waits for completion
		for range st.waitChan {
			break
		}
	}
	return nil
}

func (st *SyncTime) uploadOnce(curState LastSyncState) {
	log := logrus.WithFields(logrus.Fields{"path": st.path})
	var stateBytes []byte
	var err error
	st.lock.RLock()
	if curState != nil {
		stateBytes, err = json.Marshal(curState)
		if err != nil {
			log.WithError(err).Warn("Could not marshal curState.")
			st.lock.RUnlock()
			return
		}
	}
	st.lock.RUnlock()

	w, err := st.opener.Writer(st.ctx, st.path)
	if err != nil {
		log.WithError(err).Warn("Could not create writer.")
		return
	}
	if _, err := fmt.Fprint(w, string(stateBytes)); err != nil {
		io.LogClose(w)
		log.WithError(err).Warn("Write error.")
		return
	}
	if err := w.Close(); err != nil {
		log.WithError(err).Warn("Close error.")
		return
	}
}
