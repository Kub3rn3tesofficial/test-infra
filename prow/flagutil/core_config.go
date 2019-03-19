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

package flagutil

import (
	"flag"
	"fmt"

	"k8s.io/test-infra/prow/config"
)

// CoreConfigOptions holds options for interacting with Configs.
type CoreConfigOptions struct {
	ConfigPath string
}

// AddFlags injects config options into the given FlagSet.
func (o *CoreConfigOptions) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.ConfigPath, "config-path", "/etc/config/config.yaml", "Path to config.yaml.")
}

// Validate validates config options.
func (o *CoreConfigOptions) Validate() error {
	return nil
}

// Agent returns a started config agent.
func (o *CoreConfigOptions) Agent() (agent *config.Agent, err error) {
	agent = &config.Agent{}
	config, err := config.Load(o.ConfigPath, "")
	if err != nil {
		return nil, err
	}

	err = agent.Start(o.ConfigPath, "")
	if err != nil {
		return nil, err
	}

	return agent, err
}
