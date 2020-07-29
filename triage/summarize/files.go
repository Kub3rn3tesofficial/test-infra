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

/*
Contains functions that manage the reading and writing of files related to package summarize.
This includes reading and interpreting JSON files as actionable data, memoizing function
results to JSON, and outputting results once the summarization process is complete.
*/

package summarize

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"strconv"
	"strings"
)

// loadFailures loads a builds file and one or more test failure files. It maps build paths to builds
// and groups test failures by test name.
func loadFailures(buildsFilepath string, testsFilepaths []string) (map[string]build, map[string][]failure, error) {
	const memoMessage string = "loading failed tests"

	builds := make(map[string]build)
	tests := make(map[string][]failure)

	// Try to retrieve memoized results first to avoid another computation
	if getMemoizedResults("memo_load_failures-builds.json", "", &builds) &&
		getMemoizedResults("memo_load_failures-tests.json", "", &tests) {
		logInfo("Done (cached) " + memoMessage)
		return builds, tests, nil
	}

	builds, err := loadBuilds(buildsFilepath)
	if err != nil {
		return nil, nil, fmt.Errorf("Could not retrieve builds: %s", err)
	}

	tests, err = loadTests(testsFilepaths)
	if err != nil {
		return nil, nil, fmt.Errorf("Could not retrieve tests: %s", err)
	}

	memoizeResults("memo_load_failures-builds.json", "", builds)
	memoizeResults("memo_load_failures-tests.json", "", tests)
	logInfo("Done " + memoMessage)
	return builds, tests, nil
}

// loadPrevious loads a previous output and returns the 'clustered' field.
func loadPrevious(filepath string) ([]jsonCluster, error) {
	var previous jsonOutput

	err := getJSON(filepath, &previous)
	if err != nil {
		return nil, fmt.Errorf("Could not get previous results JSON: %s", err)
	}

	return previous.clustered, nil
}

// loadOwners loads an owners JSON file and returns it.
func loadOwners(filepath string) (map[string][]string, error) {
	var owners map[string][]string

	err := getJSON(filepath, &owners)
	if err != nil {
		return nil, fmt.Errorf("Could not get owners JSON: %s", err)
	}

	return owners, nil
}

// writeResults outputs the results of clustering to a file.
func writeResults(filepath string, data jsonOutput) error {
	err := writeJSON(filepath, data)
	if err != nil {
		return fmt.Errorf("Could not write results to disk: %s", err)
	}
	return nil
}

// writeRenderedSlice outputs the results of a call to renderSlice() to a file.
func writeRenderedSlice(filepath string, clustered []jsonCluster, cols columns) error {
	output := struct {
		clustered []jsonCluster
		cols      columns
	}{
		clustered,
		cols,
	}

	err := writeJSON(filepath, output)
	if err != nil {
		return fmt.Errorf("Could not write subset to disk: %s", err)
	}
	return nil
}

/*
getMemoizedResults attempts to retrieve memoized function results from the given filepath. If it
succeeds, it places the results into v and returns true. Otherwise, it returns false. Internally,
it calls encoding/json's Unmarshal using v as the second argument. Therefore, v mut be a non-nil
pointer.

message is a message that gets printed on success, appended to "Done (cached) ". If it is the empty
string, no message is printed.
*/
func getMemoizedResults(filepath string, message string, v interface{}) (ok bool) {
	err := getJSON(filepath, v)
	if err == nil {
		if message != "" {
			logInfo("Done (cached) " + message)
		}
		return true
	}
	return false
}

/*
memoizeResults saves the results stored in v to a JSON file. v should be a value, not a pointer. It
prints a warning if the results could not be memoized.

message is a message that gets printed on success, appended to "Done ". If it is the empty
string, no message is printed.
*/
func memoizeResults(filepath string, message string, v interface{}) {
	err := writeJSON(filepath, v)
	if err == nil && message != "" {
		logInfo("Done " + message)
		return
	}

	logWarning("Could not memoize results to '%s': %s", filepath, err)
}

/* Functions below this comment are only used within this file as of this commit. */

// jsonBuild represents a build as reported by the JSON. All values are strings.
// This should not be instantiated directly, but rather via the encoding/json package's
// Unmarshal method. This is an intermediary state for the data until it can be put into
// a build object.
type jsonBuild struct {
	path         string
	started      string
	elapsed      string
	tests_run    string
	tests_failed string
	result       string
	executor     string
	job          string
	number       string
	pr           string
	key          string // Often nonexistent
}

// asBuild is a factory function that creates a build object from a jsonBuild object, appropriately
// handling all type conversions.
func (jb *jsonBuild) asBuild() (build, error) {
	// The build object that will be returned, initialized with the values that
	// don't need conversion.
	b := build{
		path:     jb.path,
		result:   jb.result,
		executor: jb.executor,
		job:      jb.job,
		pr:       jb.pr,
		key:      jb.key,
	}

	// To avoid assignment issues
	var err error

	// started
	if jb.started != "" {
		b.started, err = strconv.Atoi(jb.started)
		if err != nil {
			return build{}, fmt.Errorf("Error converting JSON string '%s' to int for build field 'started': %s", jb.started, err)
		}
	}

	// elapsed
	if jb.elapsed != "" {
		tempElapsed, err := strconv.ParseFloat(jb.elapsed, 32)
		if err != nil {
			return build{}, fmt.Errorf("Error converting JSON string '%s' to float32 for build field 'elapsed': %s", jb.elapsed, err)
		}
		b.elapsed = int(tempElapsed)
	}

	// testsRun
	if jb.tests_run != "" {
		b.testsRun, err = strconv.Atoi(jb.tests_run)
		if err != nil {
			return build{}, fmt.Errorf("Error converting JSON string '%s' to int for build field 'testsRun': %s", jb.tests_run, err)
		}
	}

	// testsFailed
	if jb.tests_failed != "" {
		b.testsFailed, err = strconv.Atoi(jb.tests_failed)
		if err != nil {
			return build{}, fmt.Errorf("Error converting JSON string '%s' to int for build field 'testsFailed': %s", jb.tests_failed, err)
		}
	}

	// number
	if jb.number != "" {
		b.number, err = strconv.Atoi(jb.number)
		if err != nil {
			return build{}, fmt.Errorf("Error converting JSON string '%s' to int for build field 'number': %s", jb.number, err)
		}
	}

	return b, nil
}

// loadBuilds parses a JSON file containing build information and returns a map from build paths
// to build objects.
func loadBuilds(filepath string) (map[string]build, error) {
	// The map
	var builds map[string]build

	// jsonBuilds temporarily stores the builds as they are retrieved from the JSON file
	// until they can be converted to build objects
	var jsonBuilds []jsonBuild

	err := getJSON(filepath, &jsonBuilds)
	if err != nil {
		return nil, fmt.Errorf("Could not get builds JSON: %s", err)
	}

	// Convert the build information to internal build objects and store them in the builds map
	for _, jBuild := range jsonBuilds {
		// Skip builds without a start time or build number
		if jBuild.started == "" || jBuild.number == "" {
			continue
		}

		bld, err := jBuild.asBuild()
		if err != nil {
			return nil, fmt.Errorf("Could not create build object from jsonBuild object: %s", err)
		}

		if strings.Contains(bld.path, "pr-logs") {
			parts := strings.Split(bld.path, "/")
			bld.pr = parts[len(parts)-3]
		}

		builds[bld.path] = bld
	}

	return builds, nil
}

// jsonFailure represents a test failure as reported by the JSON. All values are strings.
// This should not be instantiated directly, but rather via the encoding/json package's
// Unmarshal method. This is an intermediary state for the data until it can be put into
// a failure object.
type jsonFailure struct {
	started      string
	build        string
	name         string
	failure_text string
}

// asFailure is a factory function that creates a failure object from the jsonFailure object,
// appropriately handling all type conversions.
func (jf *jsonFailure) asFailure() (failure, error) {
	// The failure object that will be returned, initialized with the values that
	// don't need conversion.
	f := failure{
		build:       jf.build,
		name:        jf.name,
		failureText: jf.failure_text,
	}

	// To avoid assignment issues
	var err error

	// started
	if jf.started != "" {
		f.started, err = strconv.Atoi(jf.started)
		if err != nil {
			return failure{}, fmt.Errorf("Error converting JSON string '%s' to int for failure field 'started': %s", jf.started, err)
		}
	}

	return f, nil
}

// loadTests parses multiple JSON files containing test information for failed tests. It returns a
// map from test names to failure objects.
func loadTests(testsFilepaths []string) (map[string][]failure, error) {
	// The map
	tests := make(map[string][]failure)

	// jsonTests temporarily stores the tests as they are retrieved from the JSON file
	// until they can be converted to failure objects
	jsonFailures := make([]jsonFailure, 0)
	for _, filepath := range testsFilepaths {
		file, err := os.Open(filepath)
		if err != nil {
			return nil, fmt.Errorf("Could not open tests file '%s': %s", filepath, err)
		}
		defer file.Close()

		// Read each line in the file as its own JSON object
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var jf jsonFailure
			err = json.Unmarshal(scanner.Bytes(), &jf)
			if err != nil {
				return nil, fmt.Errorf("Could not unmarshal JSON for text '%s': %s", scanner.Text(), err)
			}
			jsonFailures = append(jsonFailures, jf)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("Could not read file line by line: %s", err)
		}

		// Convert the failure information to internal failure objects and store them in tests
		for _, jf := range jsonFailures {
			// Check if tests of this type are already in the map
			if _, ok := tests[jf.name]; !ok {
				tests[jf.name] = make([]failure, 0)
			}

			test, err := jf.asFailure()
			if err != nil {
				return nil, fmt.Errorf("Could not create failure object from jsonFailure object: %s", err)
			}

			tests[jf.name] = append(tests[jf.name], test)
		}
	}

	// Sort the failures within each test by build
	for _, testSlice := range tests {
		sort.Slice(testSlice, func(i, j int) bool { return testSlice[i].build < testSlice[j].build })
	}

	return tests, nil
}

// getJSON opens a JSON file, parses it according to the schema provided by v, and places the results
// into v. Internally, it calls encoding/json's Unmarshal using v as the second argument. Therefore,
// v mut be a non-nil pointer.
func getJSON(filepath string, v interface{}) error {
	contents, err := ioutil.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("Could not open file '%s': %s", filepath, err)
	}

	// Decode the JSON into the provided interface
	err = json.Unmarshal(contents, v)
	if err != nil {
		return fmt.Errorf("Could not unmarshal JSON: %s", err)
	}

	return nil
}

// writeJSON generates JSON according to v and writes the results to filepath.
func writeJSON(filepath string, v interface{}) error {
	output, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("Could not encode JSON: %s", err)
	}

	err = ioutil.WriteFile(filepath, output, 0644)
	if err != nil {
		return fmt.Errorf("Could not write JSON to file: %s", err)
	}

	return nil
}
