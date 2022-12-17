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

// Package config knows how to read and parse config.yaml.
package config

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/metadata"
)

type Gangway struct {
	// AllowedApiClients encodes identifying information about API clients
	// (AllowedApiClient). An AllowedApiClient has authority to trigger a subset
	// of Prow Jobs.
	AllowedApiClients []AllowedApiClient `json:"allowed_api_clients,omitempty"`
}

type AllowedApiClient struct {
	// ApiClientGcp contains GoogleCloudPlatform details about a web API client.
	// We currently only support GoogleCloudPlatform but other cloud vendors are
	// possible as additional fields in this struct.
	GCP *ApiClientGcp `json:"gcp,omitempty"`

	// AllowedJobsFilters contains information about what kinds of Prow jobs this
	// API client is authorized to trigger.
	AllowedJobsFilters []AllowedJobsFilter `json:"allowed_jobs_filters,omitempty"`
}

// ApiClientGcp encodes GCP Cloud Endpoints-specific HTTP metadata header
// information, which are expected to be populated by the ESPv2 sidecar
// container for GKE applications (in our case, the gangway pod).
type ApiClientGcp struct {
	// EndpointApiConsumerType is the expected value of the
	// x-endpoint-api-consumer-type HTTP metadata header. Typically this will be
	// "PROJECT".
	EndpointApiConsumerType string `json:"endpoint_api_consumer_type,omitempty"`
	// EndpointApiConsumerNumber is the expected value of the
	// x-endpoint-api-consumer-number HTTP metadata header. Typically this
	// encodes the GCP Project number value, which uniquely identifies a GCP
	// Project.
	EndpointApiConsumerNumber string `json:"endpoint_api_consumer_number,omitempty"`
}

type ApiClientCloudVendor interface {
	GetVendorName() string
	GetRequiredMdHeaders() []string
}

func (gcp *ApiClientGcp) GetVendorName() string {
	return "gcp"
}

func (gcp *ApiClientGcp) GetRequiredMdHeaders() []string {
	// These headers were drawn from this example:
	// https://github.com/envoyproxy/envoy/issues/13207 (source code appears
	// to be
	// https://github.com/GoogleCloudPlatform/esp-v2/blob/3828042e5b3f840e17837c1a019f4014276014d8/tests/endpoints/bookstore_grpc/server/server.go).
	// Here's an example of what these headers can look like in practice
	// (whitespace edited for readability):
	//
	//     map[
	//       :authority:[localhost:20785]
	//       accept-encoding:[gzip]
	//       content-type:[application/grpc]
	//       user-agent:[Go-http-client/1.1]
	//       x-endpoint-api-consumer-number:[123456]
	//       x-endpoint-api-consumer-type:[PROJECT]
	//       x-envoy-original-method:[GET]
	//       x-envoy-original-path:[/v1/shelves/200?key=api-key]
	//       x-forwarded-proto:[http]
	//       x-request-id:[44770c9a-ee5f-4e36-944e-198b8d9c5196]
	//       ]
	//
	//  We only use 2 of the above because the others are not that useful at this level.
	return []string{"x-endpoint-api-consumer-type", "x-endpoint-api-consumer-number"}
}

func (allowedApiClient *AllowedApiClient) GetApiClientCloudVendor() (ApiClientCloudVendor, error) {
	if allowedApiClient.GCP != nil {
		return allowedApiClient.GCP, nil
	}

	return nil, errors.New("allowedApiClient did not have a cloud vendor set")
}

// IdentifyAllowedClient looks at the HTTP request headers (metadata) and tries
// to match it up with an allowlisted Client already defined in the main Config.
//
// Each supported client type (e.g., GCP) has custom logic around the HTTP
// metadata headers to know what kind of headers to look for. Different cloud
// vendors will have different HTTP metdata headers, although technically
// nothing stops users from injecting these headers manually on their own.
func (c *Config) IdentifyAllowedClient(md *metadata.MD) (*AllowedApiClient, error) {
	if md == nil {
		return nil, errors.New("metadata cannot be nil")
	}

	if c == nil {
		return nil, errors.New("config cannot be nil")
	}

	for _, client := range c.Gangway.AllowedApiClients {
		cv, err := client.GetApiClientCloudVendor()
		if err != nil {
			return nil, err
		}

		switch cv.GetVendorName() {
		// For GCP (GKE) Prow installations Gangway must receive the special headers
		// "x-endpoint-api-consumer-type" and "x-endpoint-api-consumer-number". This is
		// because in GKE, Gangway must run behind a Cloud Endpoints sidecar container
		// (which acts as a proxy and injects these special headers). These headers
		// allow us to identify the caller's associated GCP Project, which we need in
		// order to filter out only those Prow Jobs that this project is allowed to
		// create. Otherwise, any caller could trigger any Prow Job, which is far from
		// ideal from a security standpoint.
		case "gcp":
			v := md.Get("x-endpoint-api-consumer-type")[0]
			if client.GCP.EndpointApiConsumerType != "PROJECT" {
				return nil, fmt.Errorf("unsupported GCP API consumer type: %q", v)
			}
			v = md.Get("x-endpoint-api-consumer-number")[0]

			// Now check whether we can find the same information in the Config's allowlist.
			if client.GCP.EndpointApiConsumerNumber == v {
				return &client, nil
			}
		}
	}

	return nil, fmt.Errorf("could not find allowed client from %v", md)
}

// AllowedJobsFilter defines filters for jobs that are allowed by an
// authenticated API client.
type AllowedJobsFilter struct {
	TenantID string `json:"tenant_id,omitempty"`
}
