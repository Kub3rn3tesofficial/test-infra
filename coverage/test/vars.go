/*
 * Copyright 2018 The Kubernetes Authors.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *     http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package test

const (
	covTargetRootRel = "testTarget"
	//CovTargetRelPath points to the path to the target directory for test coverage,
	// relative to project root directory
	CovTargetRelPath = covTargetRootRel + "/presubmit"
)

var (
	tmpArtsDir = absPath("test_output/tmp_artifacts")
	//InputArtifactsDir is the absolute path to artifacts as test data
	InputArtifactsDir = absPath("testdata/artifacts")
	//CovTargetDir points to the absolute path to the target directory for test coverage
	CovTargetDir = absPath(CovTargetRelPath) + "/"
)
