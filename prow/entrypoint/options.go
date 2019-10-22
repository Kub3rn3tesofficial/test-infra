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

package entrypoint

import (
	"encoding/json"
	"errors"
	"flag"
	"time"

	"k8s.io/test-infra/prow/pod-utils/wrapper"
	"k8s.io/utils/clock"
)

// NewOptions returns an empty Options with no nil fields
func NewOptions() *Options {
	return &Options{
		clock:   clock.RealClock{},
		Options: &wrapper.Options{},
	}
}

// Options exposes the configuration necessary
// for defining the process being watched and
// where in GCS an upload will land.
type Options struct {
	// Timeout determines how long to wait before the
	// entrypoint sends SIGINT to the process
	Timeout time.Duration `json:"timeout"`
	// GracePeriod determines how long to wait after
	// sending SIGINT before the entrypoint sends
	// SIGKILL.
	GracePeriod time.Duration `json:"grace_period"`
	// DateTimeFormat is the datetime format to prefix log lines with.
	// If omitted or empty, no datetime prefix will be added.
	DateTimeFormat string `json:"datetime_format,omitempty"`
	// ArtifactDir is a directory where test processes can dump artifacts
	// for upload to persistent storage (courtesy of sidecar).
	// If specified, it is created by entrypoint before starting the test process.
	// May be ignored if not using sidecar.
	ArtifactDir string `json:"artifact_dir,omitempty"`

	// PreviousMarker has no effect when empty (default).
	// When set it causes entrypoint to:
	// a) wait until previous_marker exists
	// b) run args as normal if previous_marker == 0
	// c) otherwise immediately write PreviousErrorCode to marker_file without running args
	PreviousMarker string `json:"previous_marker,omitempty"`

	// AlwaysZero will cause entrypoint to exit zero, regardless of the marker it writes.
	// Primarily useful in case a subsequent entrypoint will read this entrypoint's marker
	AlwaysZero bool `json:"always_zero,omitempty"`

	clock clock.Clock

	*wrapper.Options
}

// Validate ensures that the set of options are
// self-consistent and valid
func (o *Options) Validate() error {
	if len(o.Args) == 0 {
		return errors.New("no process to wrap specified")
	}

	return o.Options.Validate()
}

const (
	// JSONConfigEnvVar is the environment variable that
	// utilities expect to find a full JSON configuration
	// in when run.
	JSONConfigEnvVar = "ENTRYPOINT_OPTIONS"
)

// ConfigVar exposes the environment variable used
// to store serialized configuration
func (o *Options) ConfigVar() string {
	return JSONConfigEnvVar
}

// LoadConfig loads options from serialized config
func (o *Options) LoadConfig(config string) error {
	return json.Unmarshal([]byte(config), o)
}

// AddFlags binds flags to options
func (o *Options) AddFlags(flags *flag.FlagSet) {
	flags.DurationVar(&o.Timeout, "timeout", DefaultTimeout, "Timeout for the test command.")
	flags.DurationVar(&o.GracePeriod, "grace-period", DefaultGracePeriod, "Grace period after timeout for the test command.")
	flags.StringVar(&o.ArtifactDir, "artifact-dir", "", "directory where test artifacts should be placed for upload to persistent storage")
	o.Options.AddFlags(flags)
}

// Complete internalizes command line arguments
func (o *Options) Complete(args []string) {
	o.Args = args
}

// Encode will encode the set of options in the format that
// is expected for the configuration environment variable
func Encode(options Options) (string, error) {
	encoded, err := json.Marshal(options)
	return string(encoded), err
}
