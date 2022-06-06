/*
Copyright 2022 The Kubernetes Authors.

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

package tide

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	fuzz "github.com/google/gofuzz"
)

func TestMinStruct(t *testing.T) {
	for i := 0; i < 100; i++ {
		seed := time.Now().UnixNano()
		fuzzer := fuzz.NewWithSeed(seed)

		crc := &CodeReviewCommon{}
		fuzzer.Fuzz(crc)

		want := CodeReviewForDeck{
			Title:      crc.Title,
			Number:     crc.Number,
			HeadRefOID: crc.HeadRefOID,
			Mergeable:  crc.Mergeable,
		}
		wantBytes, err := json.Marshal(&want)
		if err != nil {
			t.Fatalf("Unexpected marshal error from want struct: %v", err)
		}

		casted := MinCodeReviewCommon(*crc)
		gotBytes, err := json.Marshal(&casted)
		if err != nil {
			t.Fatalf("Unexpected marshal error from got struct: %v", err)
		}
		if diff := cmp.Diff(string(wantBytes), string(gotBytes)); diff != "" {
			t.Fatalf("Output mismatch. Want(-), got(+):\n%s", diff)
		}
	}
}
