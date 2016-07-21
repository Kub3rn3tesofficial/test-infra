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

import logging
import datetime
import os
import re
import ast

import jinja2

import kubelet_parser
import regex
    
def hilight(line, hilight_words):
    # Join all the words that need to be bolded into one regex
    words_re = regex.combine_wordsRE(hilight_words)
    line = words_re.sub(r'<span class="keyword">\1</span>', line)
    return '<span class="hilight">%s</span>' % line


def digest(data, skip_fmt=lambda l: '... skipping %d lines ...' % l, objref_dict={},
    filters={"uid":"", "pod":"", "namespace":""}, error_re=regex.error_re):
    """
    Given a build log, return a chunk of HTML code suitable for
    inclusion in a <pre> tag, with "interesting" errors hilighted.

    This is similar to the output of `grep -C4` with an appropriate regex.
    """
    lines = unicode(jinja2.escape(data)).split('\n')

    hilight_words=["error", "fatal", "failed", "build timed out"]
    if filters["pod"]:
        hilight_words = [filters["pod"]]

    if not (filters["uid"] or filters["namespace"]):
        matched_lines = [n for n, line in enumerate(lines) if error_re.search(line)]
    else:
        matched_lines, hilight_words = kubelet_parser.parse(lines, error_re, 
            hilight_words, filters, objref_dict)

    output = []
    CONTEXT = 4

    matched_lines.append(len(lines))  # sentinel value

    last_match = None
    for match in matched_lines:
        if last_match is not None:
            previous_end = min(match, last_match + CONTEXT + 1)
            output.extend(lines[last_match + 1: previous_end])
        else:
            previous_end = 0
        skip_amount = match - previous_end - CONTEXT
        if skip_amount > 1:
            output.append('<span class="skip">%s</span>' % skip_fmt(skip_amount))
        elif skip_amount == 1:  # pointless say we skipped 1 line
            output.append(lines[previous_end])
        if match == len(lines):
            break
        output.extend(lines[max(previous_end, match - CONTEXT): match])
        output.append(hilight(lines[match], hilight_words))
        last_match = match

    return '\n'.join(output)


def make_dict(data, pod_re):
    """
    Given the kubelet log file and the failed pod name, returns a dictionary
    containing the namespace and UID associated with the pod.

    This dictionary is lifted from the line with the ObjectReference
    """
    lines = unicode(jinja2.escape(data)).split('\n')
    for line in lines:
        if pod_re.search(line):
            objref = regex.objref(line)
            if objref and objref.group(1) != "":
                objref_dict = objref.group(1)        
                keys = regex.keys_re.findall(objref_dict)
                
                for k in keys:
                    objref_dict = regex.key_to_string(k, objref_dict)

                # Convert string into dictionary
                objref_dict = ast.literal_eval(regex.fix_quotes(objref_dict))
                return objref_dict


if __name__ == '__main__':
    import sys
    for f in sys.argv[1:]:
        print digest(open(f).read().decode('utf8'))