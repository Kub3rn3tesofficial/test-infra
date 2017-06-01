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
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestXmlWrap(t *testing.T) {
	cases := []struct {
		name            string
		interrupted     bool
		shouldInterrupt bool
		err             string
		expectSkipped   bool
		expectError     bool
	}{
		{
			name: "xmlWrap can pass",
		},
		{
			name:        "xmlWrap can error",
			err:         "hello there",
			expectError: true,
		},
		{
			name:            "xmlWrap always errors on interrupt",
			err:             "",
			shouldInterrupt: true,
			expectError:     true,
		},
		{
			name:            "xmlWrap errors on interrupt",
			shouldInterrupt: true,
			err:             "the step failed",
			expectError:     true,
		},
		{
			name:          "xmlWrap skips errors when already interrupted",
			interrupted:   true,
			err:           "this failed because we interrupted the previous step",
			expectSkipped: true,
		},
		{
			name:        "xmlWrap can pass when interrupted",
			interrupted: true,
			err:         "",
		},
	}

	for _, tc := range cases {
		interrupted = tc.interrupted
		suite.Cases = suite.Cases[:0]
		suite.Failures = 6
		suite.Tests = 9
		err := xmlWrap(tc.name, func() error {
			if tc.shouldInterrupt {
				interrupted = true
			}
			if tc.err != "" {
				return errors.New(tc.err)
			}
			return nil
		})
		if tc.shouldInterrupt && tc.expectError {
			if err == nil {
				t.Fatalf("Case %s did not error", tc.name)
			}
			if tc.err == "" {
				tc.err = err.Error()
			}
		}
		if (tc.err == "") != (err == nil) {
			t.Errorf("Case %s expected err: %s != actual: %v", tc.name, tc.err, err)
		}
		if tc.shouldInterrupt && !interrupted {
			t.Errorf("Case %s did not interrupt", tc.name)
		}
		if len(suite.Cases) != 1 {
			t.Fatalf("Case %s did not result in a single suite testcase: %v", tc.name, suite.Cases)
		}
		sc := suite.Cases[0]
		if sc.Name != tc.name {
			t.Errorf("Case %s resulted in wrong test case name %s", tc.name, sc.Name)
		}
		if tc.expectError {
			if sc.Failure != tc.err {
				t.Errorf("Case %s expected error %s but got %s", tc.name, tc.err, sc.Failure)
			}
			if suite.Failures != 7 {
				t.Errorf("Case %s failed and should increase suite failures from 6 to 7, found: %d", tc.name, suite.Failures)
			}
		} else if tc.expectSkipped {
			if sc.Skipped != tc.err {
				t.Errorf("Case %s expected skipped %s but got %s", tc.name, tc.err, sc.Skipped)
			}
			if suite.Failures != 7 {
				t.Errorf("Case %s interrupted and increase suite failures from 6 to 7, found: %d", tc.name, suite.Failures)
			}
		} else {
			if suite.Failures != 6 {
				t.Errorf("Case %s passed so suite failures should remain at 6, found: %d", tc.name, suite.Failures)
			}
		}

	}
}

func TestOutput(t *testing.T) {
	cases := []struct {
		name              string
		terminated        bool
		interrupted       bool
		causeTermination  bool
		causeInterruption bool
		pass              bool
		sleep             int
		output            bool
		shouldError       bool
		shouldInterrupt   bool
		shouldTerminate   bool
	}{
		{
			name: "finishRunning can pass",
			pass: true,
		},
		{
			name:   "output can pass",
			output: true,
			pass:   true,
		},
		{
			name:        "finishRuning can fail",
			pass:        false,
			shouldError: true,
		},
		{
			name:        "output can fail",
			pass:        false,
			output:      true,
			shouldError: true,
		},
		{
			name:        "finishRunning should error when terminated",
			terminated:  true,
			pass:        true,
			shouldError: true,
		},
		{
			name:        "output should error when terminated",
			terminated:  true,
			pass:        true,
			output:      true,
			shouldError: true,
		},
		{
			name:              "finishRunning should interrupt when interrupted",
			pass:              true,
			sleep:             60,
			causeInterruption: true,
			shouldError:       true,
		},
		{
			name:              "output should interrupt when interrupted",
			pass:              true,
			sleep:             60,
			output:            true,
			causeInterruption: true,
			shouldError:       true,
		},
		{
			name:             "output should terminate when terminated",
			pass:             true,
			sleep:            60,
			output:           true,
			causeTermination: true,
			shouldError:      true,
		},
		{
			name:             "finishRunning should terminate when terminated",
			pass:             true,
			sleep:            60,
			causeTermination: true,
			shouldError:      true,
		},
	}

	clearTimers := func() {
		if !terminate.Stop() {
			<-terminate.C
		}
		if !interrupt.Stop() {
			<-interrupt.C
		}
	}

	for _, tc := range cases {
		log.Println(tc.name)
		terminated = tc.terminated
		interrupted = tc.interrupted
		interrupt = time.NewTimer(time.Duration(0))
		terminate = time.NewTimer(time.Duration(0))
		clearTimers()
		if tc.causeInterruption {
			interrupt.Reset(0)
		}
		if tc.causeTermination {
			terminate.Reset(0)
		}
		var cmd *exec.Cmd
		if !tc.pass {
			cmd = exec.Command("false")
		} else if tc.sleep == 0 {
			cmd = exec.Command("true")
		} else {
			cmd = exec.Command("sleep", strconv.Itoa(tc.sleep))
		}
		runner := finishRunning
		if tc.output {
			runner = func(c *exec.Cmd) error {
				_, _, err := output(c)
				return err
			}
		}
		err := runner(cmd)
		if err == nil == tc.shouldError {
			t.Errorf("Step %s shouldError=%v error: %v", tc.name, tc.shouldError, err)
		}
		if tc.causeInterruption && !interrupted {
			t.Errorf("Step %s did not interrupt, err: %v", tc.name, err)
		} else if tc.causeInterruption && !terminate.Reset(0) {
			t.Errorf("Step %s did not reset the terminate timer: %v", tc.name, err)
		}
		if tc.causeTermination && !terminated {
			t.Errorf("Step %s did not terminate, err: %v", tc.name, err)
		}
	}
}

func TestHttpFileScheme(t *testing.T) {
	expected := "some testdata"
	tmpfile, err := ioutil.TempFile("", "test_http_file_scheme")
	if err != nil {
		t.Errorf("Error creating temporary file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.WriteString(expected); err != nil {
		t.Errorf("Error writing to temporary file: %v", err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Errorf("Error closing temporary file: %v", err)
	}

	fileUrl := fmt.Sprintf("file://%s", tmpfile.Name())
	buf := new(bytes.Buffer)
	if err := httpRead(fileUrl, buf); err != nil {
		t.Errorf("Error reading temporary file through httpRead: %v", err)
	}

	if buf.String() != expected {
		t.Errorf("httpRead(%s): expected %v, got %v", fileUrl, expected, buf)
	}
}
