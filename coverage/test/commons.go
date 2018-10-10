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

package test

import (
	"testing"
	"reflect"
)

//AssertEqual checks equality of expected and actual results, fail the test if not equal
func AssertEqual(t *testing.T, expected, actual interface{}) {
	if expected != actual {
		t.Fatalf("expected='%v'; actual='%v'", expected, actual)
	}
}

//AssertDeepEqual checks deep equality of expected and actual results, fail the test if not equal
func AssertDeepEqual(t *testing.T, expected, actual interface{}) {
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("expected='%v'; actual='%v'", expected, actual)
	}
}
