/*
Copyright 2016 The Kubernetes Authors.

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

package storage

import (
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/pkg/api"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/api/meta"
	"strconv"
	"fmt"
)

// verifies the cacheWatcher.process goroutine is properly cleaned up even if
// the writes to cacheWatcher.result channel is blocked.
func TestCacheWatcherCleanupNotBlockedByResult(t *testing.T) {
	var lock sync.RWMutex
	count := 0
	filter := func(string, labels.Set, fields.Set) bool { return true }
	forget := func(bool) {
		lock.Lock()
		defer lock.Unlock()
		count++
	}
	initEvents := []*watchCacheEvent{
		{Object: &api.Pod{}},
		{Object: &api.Pod{}},
	}
	// set the size of the buffer of w.result to 0, so that the writes to
	// w.result is blocked.
	w := newCacheWatcher(api.Scheme, 0, 0, initEvents, filter, forget, testVersioner{})
	w.Stop()
	if err := wait.PollImmediate(1*time.Second, 5*time.Second, func() (bool, error) {
		lock.RLock()
		defer lock.RUnlock()
		return count == 2, nil
	}); err != nil {
		t.Fatalf("expected forget() to be called twice, because sendWatchCacheEvent should not be blocked by the result channel: %v", err)
	}
}

type testVersioner struct{}

func (testVersioner) UpdateObject(obj runtime.Object, resourceVersion uint64) error {
	return meta.NewAccessor().SetResourceVersion(obj, strconv.FormatUint(resourceVersion, 10))
}
func (testVersioner) UpdateList(obj runtime.Object, resourceVersion uint64) error {
	return fmt.Errorf("unimplemented")
}
func (testVersioner) CompareResourceVersion(lhs, rhs runtime.Object) error {
	return fmt.Errorf("unimplemented")
}
func (testVersioner) ObjectResourceVersion(obj runtime.Object) (uint64, error) {
	return 0, fmt.Errorf("unimplemented")
}
func (testVersioner) ParseResourceVersion(resourceVersion string) (uint64, error) {
	return strconv.ParseUint(resourceVersion, 10, 64)
}
