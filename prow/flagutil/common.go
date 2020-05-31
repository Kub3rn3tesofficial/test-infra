/*
Copyright 2020 The Kubernetes Authors.

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
)

// CommonOptions holds common options which are used across Prow components
type CommonOptions struct {
	// MetricsPort is the port which is used to serve metrics
	MetricsPort int
	// PProfPort is the port which is used to serve pprof
	PProfPort int
}

// AddFlags injects common options into the given FlagSet.
func (o *CommonOptions) AddFlags(fs *flag.FlagSet) {
	fs.IntVar(&o.MetricsPort, "metrics-port", 9090, "port to serve metrics")
	fs.IntVar(&o.PProfPort, "pprof-port", 6060, "port to serve pprof")
}
