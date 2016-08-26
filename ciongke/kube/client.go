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

package kube

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
)

const (
	inClusterBaseURL = "https://kubernetes"
)

// Client interacts with the Kubernetes api-server.
type Client struct {
	baseURL   string
	client    *http.Client
	token     string
	namespace string
}

func (c *Client) request(method, urlPath string, query map[string]string, body io.Reader) ([]byte, error) {
	url := c.baseURL + urlPath
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	q := req.URL.Query()
	for k, v := range query {
		q.Add(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("response has status \"%s\" and body \"%s\"", resp.Status, string(rb))
	}
	return rb, nil
}

// NewClientInCluster creates a Client that works from within a pod.
func NewClientInCluster(namespace string) (*Client, error) {
	tokenFile := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	token, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}

	rootCAFile := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	certData, err := ioutil.ReadFile(rootCAFile)
	if err != nil {
		return nil, err
	}

	cp := x509.NewCertPool()
	cp.AppendCertsFromPEM(certData)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    cp,
		},
	}
	c := &http.Client{Transport: tr}
	return &Client{
		baseURL:   inClusterBaseURL,
		client:    c,
		token:     string(token),
		namespace: namespace,
	}, nil
}

func (c *Client) ListPods(labels map[string]string) ([]Pod, error) {
	var sel []string
	for k, v := range labels {
		sel = append(sel, fmt.Sprintf("%s = %s", k, v))
	}
	labelSelector := strings.Join(sel, ",")
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods", c.namespace)
	b, err := c.request(http.MethodGet, path, map[string]string{
		"labelSelector": labelSelector,
	}, nil)
	if err != nil {
		return nil, err
	}
	var pl struct {
		Items []Pod `json:"items"`
	}
	err = json.Unmarshal(b, &pl)
	if err != nil {
		return nil, err
	}
	return pl.Items, nil
}

func (c *Client) DeletePod(name string) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", c.namespace, name)
	_, err := c.request(http.MethodDelete, path, map[string]string{}, nil)
	return err
}

func (c *Client) GetJob(name string) (Job, error) {
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", c.namespace, name)
	body, err := c.request(http.MethodGet, path, map[string]string{}, nil)
	if err != nil {
		return Job{}, err
	}
	var retJob Job
	if err = json.Unmarshal(body, &retJob); err != nil {
		return Job{}, err
	}
	return retJob, nil
}

func (c *Client) ListJobs(labels map[string]string) ([]Job, error) {
	var sel []string
	for k, v := range labels {
		sel = append(sel, fmt.Sprintf("%s = %s", k, v))
	}
	labelSelector := strings.Join(sel, ",")
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", c.namespace)
	b, err := c.request(http.MethodGet, path, map[string]string{
		"labelSelector": labelSelector,
	}, nil)
	if err != nil {
		return nil, err
	}
	var jl struct {
		Items []Job `json:"items"`
	}
	err = json.Unmarshal(b, &jl)
	if err != nil {
		return nil, err
	}
	return jl.Items, nil
}

func (c *Client) CreateJob(j Job) (Job, error) {
	b, err := json.Marshal(j)
	if err != nil {
		return Job{}, err
	}
	buf := bytes.NewBuffer(b)
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", c.namespace)
	body, err := c.request(http.MethodPost, path, map[string]string{}, buf)
	if err != nil {
		return Job{}, err
	}
	var retJob Job
	if err = json.Unmarshal(body, &retJob); err != nil {
		return Job{}, err
	}
	return retJob, nil
}

func (c *Client) DeleteJob(name string) error {
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", c.namespace, name)
	_, err := c.request(http.MethodDelete, path, map[string]string{}, nil)
	return err
}
