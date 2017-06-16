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
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// kops specific flags.
	kopsPath        = flag.String("kops", "", "(kops only) Path to the kops binary. Must be set for kops.")
	kopsCluster     = flag.String("kops-cluster", "", "(kops only) Cluster name. Must be set for kops.")
	kopsState       = flag.String("kops-state", os.Getenv("KOPS_STATE_STORE"), "(kops only) s3:// path to kops state store. Must be set. (This flag defaults to $KOPS_STATE_STORE, and overrides it if set.)")
	kopsSSHKey      = flag.String("kops-ssh-key", os.Getenv("AWS_SSH_KEY"), "(kops only) Path to ssh key-pair for each node. (Defaults to $AWS_SSH_KEY or '~/.ssh/kube_aws_rsa'.)")
	kopsKubeVersion = flag.String("kops-kubernetes-version", "", "(kops only) If set, the version of Kubernetes to deploy (can be a URL to a GCS path where the release is stored) (Defaults to kops default, latest stable release.).")
	kopsZones       = flag.String("kops-zones", "us-west-2a", "(kops AWS only) AWS zones for kops deployment, comma delimited.")
	kopsNodes       = flag.Int("kops-nodes", 2, "(kops only) Number of nodes to create.")
	kopsUpTimeout   = flag.Duration("kops-up-timeout", 20*time.Minute, "(kops only) Time limit between 'kops config / kops update' and a response from the Kubernetes API.")
	kopsAdminAccess = flag.String("kops-admin-access", "", "(kops only) If set, restrict apiserver access to this CIDR range.")
	kopsImage       = flag.String("kops-image", "", "(kops only) Image (AMI) for nodes to use. (Defaults to kops default, a Debian image with a custom kubernetes kernel.)")
	kopsArgs        = flag.String("kops-args", "", "(kops only) Additional space-separated args to pass unvalidated to 'kops create cluster', e.g. '--kops-args=\"--dns private --node-size t2.micro\"'")
)

type kops struct {
	path        string
	kubeVersion string
	sshKey      string
	zones       []string
	nodes       int
	adminAccess string
	cluster     string
	image       string
	args        string
	kubecfg     string
	state       string
	workdir     string
}

func NewKops() (*kops, error) {
	if *kopsPath == "" {
		return nil, fmt.Errorf("--kops must be set to a valid binary path for kops deployment.")
	}
	if *kopsCluster == "" {
		return nil, fmt.Errorf("--kops-cluster must be set to a valid cluster name for kops deployment.")
	}
	if *kopsState == "" {
		return nil, fmt.Errorf("--kops-state must be set to a valid S3 path for kops deployment.")
	}
	sshKey := *kopsSSHKey
	if sshKey == "" {
		usr, err := user.Current()
		if err != nil {
			return nil, err
		}
		sshKey = filepath.Join(usr.HomeDir, ".ssh/kube_aws_rsa")
	}
	f, err := ioutil.TempFile("", "kops-kubecfg")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	kubecfg := f.Name()
	if err := f.Chmod(0600); err != nil {
		return nil, err
	}
	if err := os.Setenv("KUBECONFIG", kubecfg); err != nil {
		return nil, err
	}
	// Set KUBERNETES_CONFORMANCE_TEST so the auth info is picked up
	// from kubectl instead of bash inference.
	if err := os.Setenv("KUBERNETES_CONFORMANCE_TEST", "yes"); err != nil {
		return nil, err
	}
	// Set KUBERNETES_CONFORMANCE_PROVIDER to override the
	// cloudprovider for KUBERNETES_CONFORMANCE_TEST.
	if err := os.Setenv("KUBERNETES_CONFORMANCE_PROVIDER", "aws"); err != nil {
		return nil, err
	}
	// AWS_SSH_KEY is required by the AWS e2e tests.
	if err := os.Setenv("AWS_SSH_KEY", sshKey); err != nil {
		return nil, err
	}
	// ZONE is required by the AWS e2e tests.
	zones := strings.Split(*kopsZones, ",")
	if err := os.Setenv("ZONE", zones[0]); err != nil {
		return nil, err
	}
	return &kops{
		path:        *kopsPath,
		kubeVersion: *kopsKubeVersion,
		sshKey:      sshKey + ".pub", // kops only needs the public key, e2es need the private key.
		zones:       zones,
		nodes:       *kopsNodes,
		adminAccess: *kopsAdminAccess,
		cluster:     *kopsCluster,
		image:       *kopsImage,
		args:        *kopsArgs,
		state:       *kopsState,
		kubecfg:     kubecfg,
	}, nil
}

func (k kops) Up() error {
	createArgs := []string{
		"--state", k.state,
		"create", "cluster",
		"--name", k.cluster,
		"--ssh-public-key", k.sshKey,
		"--node-count", strconv.Itoa(k.nodes),
		"--zones", strings.Join(k.zones, ","),
	}
	if k.kubeVersion != "" {
		createArgs = append(createArgs, "--kubernetes-version", k.kubeVersion)
	}
	if k.adminAccess != "" {
		createArgs = append(createArgs, "--admin-access", k.adminAccess)
	}
	if k.image != "" {
		createArgs = append(createArgs, "--image", k.image)
	}
	if k.args != "" {
		createArgs = append(createArgs, strings.Split(k.args, " ")...)
	}
	createCmd := exec.Command(k.path, createArgs...)
	createCmd.Dir = k.workdir
	if err := finishRunning(createCmd); err != nil {
		return fmt.Errorf("kops configuration failed: %v", err)
	}
	updateCmd := exec.Command(k.path, "--state", k.state, "update", "cluster", k.cluster, "--yes")
	updateCmd.Dir = k.workdir

	if err := finishRunning(updateCmd); err != nil {
		return fmt.Errorf("kops bringup failed: %v", err)
	}

	validateCmd := exec.Command(k.path, "--state", k.state, "validate", "cluster", k.cluster)
	validateCmd.Dir = k.workdir
	return retryFinishRunning(validateCmd, *kopsUpTimeout)
}

func (k kops) IsUp() error {
	validateCmd := exec.Command(k.path, "--state", k.state, "validate", "cluster", k.cluster)
	validateCmd.Dir = k.workdir
	return finishRunning(validateCmd)
}

func (k kops) SetupKubecfg() error {
	if err := useKubeContext(k.cluster); err == nil {
		// Assume that if we already have it, it's good.
		return nil
	}
	// At this point, kube context does not exist. Assume we need to export it from kops
	if err := finishRunning(exec.Command(k.path, "--state", k.state, "export", "kubecfg", k.cluster)); err != nil {
		return fmt.Errorf("Failure exporting kops kubecfg: %v", err)
	}
	return useKubeContext(k.cluster)
}

func (k kops) Down() error {
	// We do a "kops get" first so the exit status of "kops delete" is
	// more sensical in the case of a non-existent cluster. ("kops
	// delete" will exit with status 1 on a non-existent cluster)
	getClusterCmd := exec.Command(k.path, "--state", k.state, "get", "clusters", k.cluster)
	getClusterCmd.Dir = k.workdir
	err := finishRunning(getClusterCmd)
	if err != nil {
		// This is expected if the cluster doesn't exist.
		return nil
	}
	deleteClusterCmd := exec.Command(k.path, "--state", k.state, "delete", "cluster", k.cluster, "--yes")
	deleteClusterCmd.Dir = k.workdir
	return finishRunning(deleteClusterCmd)
}
