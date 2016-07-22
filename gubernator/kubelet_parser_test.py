#!/usr/bin/env python

# Copyright 2016 The Kubernetes Authors All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import unittest

import kubelet_parser
import regex


lines = ["line 0", "pod 2 3", "abcd podName", "line 3", "failed",
"Event(api.ObjectReference{Namespace:\"podName\", Name:\"abc\", UID:\"uid\"}", "uid"]
filters = {"uid":"", "pod":"", "namespace":""}

class KubeletParserTest(unittest.TestCase):
	def test_parse_error_re(self):
		"""Test for build-log.txt filtering by error_re"""
		matched_lines, hilight_words = kubelet_parser.parse(lines,
			["error", "fatal", "failed", "build timed out"], filters, {})
		self.assertEqual(matched_lines, [4])
		self.assertEqual(hilight_words, ["error", "fatal", "failed", "build timed out"])


	def test_parse_empty_lines(self):
		"""Test that it doesn't fail when files are empty"""
		matched_lines, hilight_words = kubelet_parser.parse([],
			["error", "fatal", "failed", "build timed out"], filters, {})
		self.assertEqual(matched_lines, [])
		self.assertEqual(hilight_words, ["error", "fatal", "failed", "build timed out"])		


	def test_parse_pod_RE(self):
		"""Test for initial pod filtering"""
		filters["pod"] = "pod"
		matched_lines, hilight_words = kubelet_parser.parse(lines,
			["pod"], filters,  {"UID":"", "Namespace":""})
		self.assertEqual(matched_lines, [1])
		self.assertEqual(hilight_words, ["pod"])	


	def test_parse_filters(self):
		"""Test for filters"""
		filters["pod"] = "pod"
		filters["uid"] = "on"
		filters["namespace"] = "on"
		matched_lines, hilight_words = kubelet_parser.parse(lines,
			["pod"], filters, {"UID":"uid", "Namespace":"podName"})
		self.assertEqual(matched_lines, [1, 2, 5, 6])
		self.assertEqual(hilight_words, ["pod", "uid", "podName"])	


	def test_make_dict(self):
		"""Test make_dict works"""
		objref_dict = kubelet_parser.make_dict(lines, regex.wordRE("abc"))
		self.assertEqual(objref_dict, {"UID":"uid", "Namespace":"podName", "Name":"abc"})


	def test_make_dict_fail(self):
		"""Test when objref line not in file"""
		lines = ["pod failed"]
		objref_dict = kubelet_parser.make_dict(lines, regex.wordRE("abc"))
		self.assertEqual(objref_dict, None)

if __name__ == '__main__':
    unittest.main()
