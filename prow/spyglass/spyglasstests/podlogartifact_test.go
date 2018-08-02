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

package spyglasstests

import (
	"bytes"
	"testing"

	"k8s.io/test-infra/prow/spyglass"
)

func TestPodLogReadAll(t *testing.T) {
	testCases := []struct {
		name     string
		artifact *spyglass.PodLogArtifact
		expected []byte
	}{
		{
			name:     "\"Job\" Podlog readall",
			artifact: spyglass.NewPodLogArtifact("job", "123", "", 500e6, fakeJa),
			expected: []byte("clusterA"),
		},
		{
			name:     "\"Jib\" Podlog readall",
			artifact: spyglass.NewPodLogArtifact("jib", "123", "", 500e6, fakeJa),
			expected: []byte("clusterB"),
		},
	}
	for _, tc := range testCases {
		res, err := tc.artifact.ReadAll()
		if err != nil {
			t.Fatalf("%s failed reading bytes of log. err: %s", tc.name, err)
		}
		if !bytes.Equal(tc.expected, res) {
			t.Errorf("Unexpected result of reading pod logs, expected %s, got %s", string(tc.expected), string(res))
		}

	}

}
