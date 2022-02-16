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

// Code generated by counterfeiter. DO NOT EDIT.
package testfreezefakes

import (
	"sync"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type FakeImpl struct {
	ListRefsStub        func(*git.Remote) ([]*plumbing.Reference, error)
	listRefsMutex       sync.RWMutex
	listRefsArgsForCall []struct {
		arg1 *git.Remote
	}
	listRefsReturns struct {
		result1 []*plumbing.Reference
		result2 error
	}
	listRefsReturnsOnCall map[int]struct {
		result1 []*plumbing.Reference
		result2 error
	}
	invocations      map[string][][]interface{}
	invocationsMutex sync.RWMutex
}

func (fake *FakeImpl) ListRefs(arg1 *git.Remote) ([]*plumbing.Reference, error) {
	fake.listRefsMutex.Lock()
	ret, specificReturn := fake.listRefsReturnsOnCall[len(fake.listRefsArgsForCall)]
	fake.listRefsArgsForCall = append(fake.listRefsArgsForCall, struct {
		arg1 *git.Remote
	}{arg1})
	fake.recordInvocation("ListRefs", []interface{}{arg1})
	fake.listRefsMutex.Unlock()
	if fake.ListRefsStub != nil {
		return fake.ListRefsStub(arg1)
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	fakeReturns := fake.listRefsReturns
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImpl) ListRefsCallCount() int {
	fake.listRefsMutex.RLock()
	defer fake.listRefsMutex.RUnlock()
	return len(fake.listRefsArgsForCall)
}

func (fake *FakeImpl) ListRefsCalls(stub func(*git.Remote) ([]*plumbing.Reference, error)) {
	fake.listRefsMutex.Lock()
	defer fake.listRefsMutex.Unlock()
	fake.ListRefsStub = stub
}

func (fake *FakeImpl) ListRefsArgsForCall(i int) *git.Remote {
	fake.listRefsMutex.RLock()
	defer fake.listRefsMutex.RUnlock()
	argsForCall := fake.listRefsArgsForCall[i]
	return argsForCall.arg1
}

func (fake *FakeImpl) ListRefsReturns(result1 []*plumbing.Reference, result2 error) {
	fake.listRefsMutex.Lock()
	defer fake.listRefsMutex.Unlock()
	fake.ListRefsStub = nil
	fake.listRefsReturns = struct {
		result1 []*plumbing.Reference
		result2 error
	}{result1, result2}
}

func (fake *FakeImpl) ListRefsReturnsOnCall(i int, result1 []*plumbing.Reference, result2 error) {
	fake.listRefsMutex.Lock()
	defer fake.listRefsMutex.Unlock()
	fake.ListRefsStub = nil
	if fake.listRefsReturnsOnCall == nil {
		fake.listRefsReturnsOnCall = make(map[int]struct {
			result1 []*plumbing.Reference
			result2 error
		})
	}
	fake.listRefsReturnsOnCall[i] = struct {
		result1 []*plumbing.Reference
		result2 error
	}{result1, result2}
}

func (fake *FakeImpl) Invocations() map[string][][]interface{} {
	fake.invocationsMutex.RLock()
	defer fake.invocationsMutex.RUnlock()
	fake.listRefsMutex.RLock()
	defer fake.listRefsMutex.RUnlock()
	copiedInvocations := map[string][][]interface{}{}
	for key, value := range fake.invocations {
		copiedInvocations[key] = value
	}
	return copiedInvocations
}

func (fake *FakeImpl) recordInvocation(key string, args []interface{}) {
	fake.invocationsMutex.Lock()
	defer fake.invocationsMutex.Unlock()
	if fake.invocations == nil {
		fake.invocations = map[string][][]interface{}{}
	}
	if fake.invocations[key] == nil {
		fake.invocations[key] = [][]interface{}{}
	}
	fake.invocations[key] = append(fake.invocations[key], args)
}
