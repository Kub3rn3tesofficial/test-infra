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
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
)

var configPath = flag.String("config", "resources.json", "Path to init resource file")
var storage = flag.String("storage", "", "Path to presistent volume to load the state")

func main() {
	flag.Parse()
	logrus.SetFormatter(&logrus.JSONFormatter{})

	r, err := NewRanch(*configPath, *storage)
	if err != nil {
		logrus.WithError(err).Fatal("Fail to create my ranch! Config: %v, storage : %v", *configPath, *storage)
	}

	http.Handle("/", handleDefault())
	http.Handle("/acquire", handleAcquire(r))
	http.Handle("/release", handleRelease(r))
	http.Handle("/reset", handleReset(r))
	http.Handle("/update", handleUpdate(r))
	http.Handle("/metric", handleMetric(r))

	go func() {
		logTick := time.NewTicker(time.Minute).C
		saveTick := time.NewTicker(time.Minute).C
		configTick := time.NewTicker(time.Minute * 10).C
		for {
			select {
			case <-logTick:
				r.logStatus()
			case <-saveTick:
				r.saveState()
			case <-configTick:
				r.syncConfig(*configPath)
			}
		}
	}()

	logrus.Info("Start Service")
	logrus.WithError(http.ListenAndServe(":8080", nil)).Fatal("ListenAndServe returned.")
}

//  handleDefault: Handler for /, always pass with 200
func handleDefault() http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		logrus.WithField("handler", "handleDefault").Infof("From %v", req.RemoteAddr)
	}
}

//  handleAcquire: Handler for /acquire
// 	URLParams:
//		Required: type=[string]  : type of requested resource
//		Required: state=[string] : state of the requested resource
//		Required: owner=[string] : requester of the resource
func handleAcquire(r *Ranch) http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		logrus.WithField("handler", "handleStart").Infof("From %v", req.RemoteAddr)

		if req.Method != "POST" {
			msg := fmt.Sprintf("Method %v, /acquire only accepts POST.", req.Method)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusMethodNotAllowed)
			return
		}

		// TODO(krzyzacy) - sanitize user input
		rtype := req.URL.Query().Get("type")
		state := req.URL.Query().Get("state")
		owner := req.URL.Query().Get("owner")
		if rtype == "" || state == "" || owner == "" {
			msg := fmt.Sprintf("Type: %v, state: %v, owner: %v, all of them must not be empty in the request.", rtype, state, owner)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusBadRequest)
			return
		}

		logrus.Infof("Request for a %v %v from %v", state, rtype, owner)

		status, resource := r.Acquire(rtype, state, owner)

		if resource != nil {
			resJSON, err := json.Marshal(resource)
			if err != nil {
				logrus.WithError(err).Errorf("json.Marshal failed : %v, resource will be released", resource)
				http.Error(res, err.Error(), http.StatusInternalServerError)
				resource.Owner = "" // release the resource, though this is not expected to happen.
				return
			}
			logrus.Infof("Resource leased: %v", string(resJSON))
			fmt.Fprint(res, string(resJSON))
		} else {
			logrus.Infof("No available resource")
			http.Error(res, "No resource", status)
		}
	}
}

//  handleRelease: Handler for /release
//	URL Params:
//		Required: name=[string]  : name of finished resource
//		Required: owner=[string] : owner of the resource
//		Required: dest=[string]  : dest state
func handleRelease(r *Ranch) http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		logrus.WithField("handler", "handleDone").Infof("From %v", req.RemoteAddr)

		if req.Method != "POST" {
			msg := fmt.Sprintf("Method %v, /release only accepts POST.", req.Method)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusMethodNotAllowed)
			return
		}

		name := req.URL.Query().Get("name")
		dest := req.URL.Query().Get("dest")
		owner := req.URL.Query().Get("owner")
		if name == "" || dest == "" || owner == "" {
			msg := fmt.Sprintf("Name: %v, dest: %v, owner: %v, all of them must not be empty in the request.", name, dest, owner)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusBadRequest)
			return
		}

		if status, err := r.Release(name, dest, owner); err != nil {
			logrus.WithError(err).Errorf("Done failed: %v - %v (from %v)", name, dest, owner)
			http.Error(res, err.Error(), status)
			return
		}

		logrus.Infof("Done with resource %v, set to state %v", name, dest)
	}
}

//  handleReset: Handler for /reset
//	URL Params:
//		Required: type=[string] : type of resource in interest
//		Required: state=[string] : original state
//		Required: dest=[string] : dest state, for expired resource
//		Required: expire=[durationStr*] resource has not been updated since before {expire}.
func handleReset(r *Ranch) http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		logrus.WithField("handler", "handleReset").Infof("From %v", req.RemoteAddr)

		if req.Method != "POST" {
			msg := fmt.Sprintf("Method %v, /reset only accepts POST.", req.Method)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusMethodNotAllowed)
			return
		}

		rtype := req.URL.Query().Get("type")
		state := req.URL.Query().Get("state")
		expireStr := req.URL.Query().Get("expire")
		dest := req.URL.Query().Get("dest")

		logrus.Infof("%v, %v, %v, %v", rtype, state, expireStr, dest)

		if rtype == "" || state == "" || expireStr == "" || dest == "" {
			msg := fmt.Sprintf("Type: %v, state: %v, expire: %v, dest: %v, all of them must not be empty in the request.", rtype, state, expireStr, dest)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusBadRequest)
			return
		}

		expire, err := time.ParseDuration(expireStr)
		if err != nil {
			logrus.WithError(err).Errorf("Invalid expire: %v", expireStr)
			http.Error(res, err.Error(), http.StatusBadRequest)
			return
		}

		_, rmap := r.Reset(rtype, state, expire, dest)
		resJSON, err := json.Marshal(rmap)
		if err != nil {
			logrus.WithError(err).Errorf("json.Marshal failed : %v", rmap)
			http.Error(res, err.Error(), http.StatusInternalServerError)
			return
		}
		logrus.Infof("Resource %v reset successful, %d items moved to state %v", rtype, len(rmap), dest)
		fmt.Fprint(res, string(resJSON))
	}
}

//  handleUpdate: Handler for /update
//  URLParams
//		Required: name=[string]  : name of target resource
//		Required: owner=[string] : owner of the resource
//		Required: state=[string] : current state of the resource
func handleUpdate(r *Ranch) http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		logrus.WithField("handler", "handleUpdate").Infof("From %v", req.RemoteAddr)

		if req.Method != "POST" {
			msg := fmt.Sprintf("Method %v, /update only accepts POST.", req.Method)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusMethodNotAllowed)
			return
		}

		name := req.URL.Query().Get("name")
		owner := req.URL.Query().Get("owner")
		state := req.URL.Query().Get("state")
		if name == "" || owner == "" || state == "" {
			msg := fmt.Sprintf("Name: %v, owner: %v, state : %v, all of them must not be empty in the request.", name, owner, state)
			logrus.Warning(msg)
			http.Error(res, msg, http.StatusBadRequest)
			return
		}

		if status, err := r.Update(name, owner, state); err != nil {
			logrus.WithError(err).Errorf("Update failed: %v - %v (%v)", name, state, owner)
			http.Error(res, err.Error(), status)
			return
		}

		logrus.Info("Updated resource %v", name)
	}
}

func handleMetric(r *Ranch) http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		logrus.WithField("handler", "handleMetric").Infof("From %v", req.RemoteAddr)

		if req.Method != "GET" {
			logrus.Warning("[BadRequest]method %v, expect GET", req.Method)
			http.Error(res, "/metric only accepts GET", http.StatusMethodNotAllowed)
			return
		}

		fmt.Fprint(res, "To be implemented.\n")
	}
}

type Resource struct {
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	State      string    `json:"state"`
	Owner      string    `json:"owner"`
	LastUpdate time.Time `json:"lastupdate"`
}

type Ranch struct {
	Resources   []Resource
	lock        sync.RWMutex
	storagePath string
}

func NewRanch(config string, storage string) (*Ranch, error) {

	newRanch := &Ranch{
		storagePath: storage,
	}

	if storage != "" {
		buf, err := ioutil.ReadFile(storage)
		if err == nil {
			logrus.Infof("Current state: %v.", buf)
			err = json.Unmarshal(buf, newRanch)
			if err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	if err := newRanch.syncConfig(config); err != nil {
		return nil, err
	}

	newRanch.logStatus()

	return newRanch, nil
}

func (r *Ranch) logStatus() {
	r.lock.RLock()
	defer r.lock.RUnlock()

	for _, res := range r.Resources {
		resJSON, _ := json.Marshal(res)
		logrus.Infof("Current Resources : %v", string(resJSON))
	}
}

func (r *Ranch) syncConfig(config string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	file, err := ioutil.ReadFile(config)
	if err != nil {
		return err
	}

	var data []Resource
	err = json.Unmarshal(file, &data)
	if err != nil {
		return err
	}

	return r.syncConfigHelper(data)
}

func (r *Ranch) syncConfigHelper(data []Resource) error {
	// delete non-exist resource
	valid := 0
	for _, res := range r.Resources {
		// If currently busy, yield deletion to later cycles.
		if res.Owner != "" {
			r.Resources[valid] = res
			valid++
			continue
		}

		for _, newRes := range data {
			if res.Name == newRes.Name {
				r.Resources[valid] = res
				valid++
				break
			}
		}
	}
	r.Resources = r.Resources[:valid]

	// add new resource
	for _, p := range data {
		found := false
		for _, exist := range r.Resources {
			if p.Name == exist.Name {
				found = true
				break
			}
		}

		if !found {
			if p.State == "" {
				p.State = "free"
			}
			r.Resources = append(r.Resources, p)
		}
	}
	return nil
}

func (r *Ranch) saveState() {
	if r.storagePath == "" {
		return
	}

	r.lock.RLock()
	defer r.lock.RUnlock()

	// If fail to save data, fatal and restart the server
	buf, err := json.Marshal(r)
	if err != nil {
		logrus.WithError(err).Fatal("Error marshal ranch")
	}
	err = ioutil.WriteFile(r.storagePath+".tmp", buf, 0644)
	if err != nil {
		logrus.WithError(err).Fatal("Error write file")
	}
	err = os.Rename(r.storagePath+".tmp", r.storagePath)
	if err != nil {
		logrus.WithError(err).Fatal("Error rename file")
	}
}

func (r *Ranch) Acquire(rtype string, state string, owner string) (int, *Resource) {
	r.lock.Lock()
	defer r.lock.Unlock()

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if rtype == res.Type && state == res.State && res.Owner == "" {
			res.LastUpdate = time.Now()
			res.Owner = owner
			return http.StatusOK, res
		}
	}

	return http.StatusNotFound, nil
}

func (r *Ranch) Release(name string, dest string, owner string) (int, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if name == res.Name {
			if owner != res.Owner {
				return http.StatusUnauthorized, fmt.Errorf("Owner not match, got %v, expect %v", res.Owner, owner)
			}
			res.LastUpdate = time.Now()
			res.Owner = ""
			res.State = dest
			return http.StatusOK, nil
		}
	}

	return http.StatusNotFound, fmt.Errorf("Cannot find resource %v", name)
}

func (r *Ranch) Update(name string, owner string, state string) (int, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if name == res.Name {
			if owner != res.Owner {
				return http.StatusUnauthorized, fmt.Errorf("Owner not match, got %v, expect %v", res.Owner, owner)
			}

			if state != res.State {
				return http.StatusConflict, fmt.Errorf("State not match, got %v, expect %v", res.State, state)
			}
			res.LastUpdate = time.Now()
			return http.StatusOK, nil
		}
	}

	return http.StatusNotFound, fmt.Errorf("Cannot find resource %v", name)
}

func (r *Ranch) Reset(rtype string, state string, expire time.Duration, dest string) (int, map[string]string) {
	r.lock.Lock()
	defer r.lock.Unlock()

	ret := make(map[string]string)

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if rtype == res.Type && state == res.State && res.Owner != "" {
			if time.Now().Sub(res.LastUpdate) > expire {
				res.LastUpdate = time.Now()
				ret[res.Name] = res.Owner
				res.Owner = ""
				res.State = dest
			}
		}
	}

	return http.StatusOK, ret
}
