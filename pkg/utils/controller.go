// Copyright (c) KAITO authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/*
Copyright 2014 The Kubernetes Authors.

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

// notes: This file contains code copied from https://github.com/kubernetes/kubernetes/blob/01c55353873dcb80b1280c0d470ca29f07b48466/pkg/controller/controller_utils.go#L61-L319
// import it from k8s once it is available in the go module.

package utils

import (
	"fmt"
	"sync/atomic"
	"time"

	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const (
	// If a watch drops a delete event for a pod, it'll take this long
	// before a dormant controller waiting for those packets is woken up anyway. It is
	// specifically targeted at the case where some problem prevents an update
	// of expectations, without it the controller could stay asleep forever. This should
	// be set based on the expected latency of watch events.
	//
	// Currently a controller can service (create *and* observe the watch events for said
	// creation) about 10 pods a second, so it takes about 1 min to service
	// 500 pods. Just creation is limited to 20qps, and watching happens with ~10-30s
	// latency/pod at the scale of 3000 pods over 100 nodes.
	ExpectationsTimeout = 5 * time.Minute
)

// Expectations are a way for controllers to tell the controller manager what they expect. eg:
//	ControllerExpectations: {
//		controller1: expects  2 adds in 2 minutes
//		controller2: expects  2 dels in 2 minutes
//		controller3: expects -1 adds in 2 minutes => controller3's expectations have already been met
//	}
//
// Implementation:
//	ControlleeExpectation = pair of atomic counters to track controllee's creation/deletion
//	ControllerExpectationsStore = TTLStore + a ControlleeExpectation per controller
//
// * Once set expectations can only be lowered
// * A controller isn't synced till its expectations are either fulfilled, or expire
// * Controllers that don't set expectations will get woken up for every matching controllee

// ExpKeyFunc to parse out the key from a ControlleeExpectation
var ExpKeyFunc = func(obj interface{}) (string, error) {
	if e, ok := obj.(*ControlleeExpectations); ok {
		return e.key, nil
	}
	return "", fmt.Errorf("could not find key for obj %#v", obj)
}

// ControllerExpectationsInterface is an interface that allows users to set and wait on expectations.
// Only abstracted out for testing.
// Warning: if using KeyFunc it is not safe to use a single ControllerExpectationsInterface with different
// types of controllers, because the keys might conflict across types.
type ControllerExpectationsInterface interface {
	GetExpectations(controllerKey string) (*ControlleeExpectations, bool, error)
	SatisfiedExpectations(logger klog.Logger, controllerKey string) bool
	DeleteExpectations(logger klog.Logger, controllerKey string)
	SetExpectations(logger klog.Logger, controllerKey string, add, del int) error
	ExpectCreations(logger klog.Logger, controllerKey string, adds int) error
	ExpectDeletions(logger klog.Logger, controllerKey string, dels int) error
	CreationObserved(logger klog.Logger, controllerKey string)
	DeletionObserved(logger klog.Logger, controllerKey string)
	RaiseExpectations(logger klog.Logger, controllerKey string, add, del int)
	LowerExpectations(logger klog.Logger, controllerKey string, add, del int)
	GetExpectationStartTime(controllerKey string) *time.Time
}

// ControllerExpectations is a cache mapping controllers to what they expect to see before being woken up for a sync.
type ControllerExpectations struct {
	cache.Store
}

// GetExpectations returns the ControlleeExpectations of the given controller.
func (r *ControllerExpectations) GetExpectations(controllerKey string) (*ControlleeExpectations, bool, error) {
	exp, exists, err := r.GetByKey(controllerKey)
	if err == nil && exists {
		return exp.(*ControlleeExpectations), true, nil
	}
	return nil, false, err
}

// DeleteExpectations deletes the expectations of the given controller from the TTLStore.
func (r *ControllerExpectations) DeleteExpectations(logger klog.Logger, controllerKey string) {
	if exp, exists, err := r.GetByKey(controllerKey); err == nil && exists {
		if err := r.Delete(exp); err != nil {

			logger.V(2).Info("Error deleting expectations", "controller", controllerKey, "err", err)
		}
	}
}

// SatisfiedExpectations returns true if the required adds/dels for the given controller have been observed.
// Add/del counts are established by the controller at sync time, and updated as controllees are observed by the controller
// manager.
func (r *ControllerExpectations) SatisfiedExpectations(logger klog.Logger, controllerKey string) bool {
	if exp, exists, err := r.GetExpectations(controllerKey); exists {
		if exp.Fulfilled() {
			logger.V(4).Info("Controller expectations fulfilled", "expectations", exp)
			return true
		} else if exp.isExpired() {
			logger.V(4).Info("Controller expectations expired", "expectations", exp)
			return true
		} else {
			logger.V(4).Info("Controller still waiting on expectations", "expectations", exp)
			return false
		}
	} else if err != nil {
		logger.V(2).Info("Error encountered while checking expectations, forcing sync", "err", err)
	} else {
		// When a new controller is created, it doesn't have expectations.
		// When it doesn't see expected watch events for > TTL, the expectations expire.
		//	- In this case it wakes up, creates/deletes controllees, and sets expectations again.
		// When it has satisfied expectations and no controllees need to be created/destroyed > TTL, the expectations expire.
		//	- In this case it continues without setting expectations till it needs to create/delete controllees.
		logger.V(4).Info("Controller either never recorded expectations, or the ttl expired", "controller", controllerKey)
	}
	// Trigger a sync if we either encountered and error (which shouldn't happen since we're
	// getting from local store) or this controller hasn't established expectations.
	return true
}

// GetExpectationStartTime returns the time when the expectations for the given controller were set.
func (r *ControllerExpectations) GetExpectationStartTime(controllerKey string) *time.Time {
	if exp, exists, _ := r.GetExpectations(controllerKey); exists {
		return &exp.timestamp
	}
	return nil
}

// TODO: Extend ExpirationCache to support explicit expiration.
// TODO: Make this possible to disable in tests.
// TODO: Support injection of clock.
func (exp *ControlleeExpectations) isExpired() bool {
	return clock.RealClock{}.Since(exp.timestamp) > ExpectationsTimeout
}

// SetExpectations registers new expectations for the given controller. Forgets existing expectations.
func (r *ControllerExpectations) SetExpectations(logger klog.Logger, controllerKey string, add, del int) error {
	exp := &ControlleeExpectations{add: int64(add), del: int64(del), key: controllerKey, timestamp: clock.RealClock{}.Now()}
	logger.V(4).Info("Setting expectations", "expectations", exp)
	return r.Add(exp)
}

func (r *ControllerExpectations) ExpectCreations(logger klog.Logger, controllerKey string, adds int) error {
	return r.SetExpectations(logger, controllerKey, adds, 0)
}

func (r *ControllerExpectations) ExpectDeletions(logger klog.Logger, controllerKey string, dels int) error {
	return r.SetExpectations(logger, controllerKey, 0, dels)
}

// Decrements the expectation counts of the given controller.
func (r *ControllerExpectations) LowerExpectations(logger klog.Logger, controllerKey string, add, del int) {
	if exp, exists, err := r.GetExpectations(controllerKey); err == nil && exists {
		exp.Add(int64(-add), int64(-del))
		// The expectations might've been modified since the update on the previous line.
		logger.V(4).Info("Lowered expectations", "expectations", exp)
	}
}

// Increments the expectation counts of the given controller.
func (r *ControllerExpectations) RaiseExpectations(logger klog.Logger, controllerKey string, add, del int) {
	if exp, exists, err := r.GetExpectations(controllerKey); err == nil && exists {
		exp.Add(int64(add), int64(del))
		// The expectations might've been modified since the update on the previous line.
		logger.V(4).Info("Raised expectations", "expectations", exp)
	}
}

// CreationObserved atomically decrements the `add` expectation count of the given controller.
func (r *ControllerExpectations) CreationObserved(logger klog.Logger, controllerKey string) {
	r.LowerExpectations(logger, controllerKey, 1, 0)
}

// DeletionObserved atomically decrements the `del` expectation count of the given controller.
func (r *ControllerExpectations) DeletionObserved(logger klog.Logger, controllerKey string) {
	r.LowerExpectations(logger, controllerKey, 0, 1)
}

// ControlleeExpectations track controllee creates/deletes.
type ControlleeExpectations struct {
	// Important: Since these two int64 fields are using sync/atomic, they have to be at the top of the struct due to a bug on 32-bit platforms
	// See: https://golang.org/pkg/sync/atomic/ for more information
	add       int64
	del       int64
	key       string
	timestamp time.Time
}

// Add increments the add and del counters.
func (e *ControlleeExpectations) Add(add, del int64) {
	atomic.AddInt64(&e.add, add)
	atomic.AddInt64(&e.del, del)
}

// Fulfilled returns true if this expectation has been fulfilled.
func (e *ControlleeExpectations) Fulfilled() bool {
	// TODO: think about why this line being atomic doesn't matter
	return atomic.LoadInt64(&e.add) <= 0 && atomic.LoadInt64(&e.del) <= 0
}

// GetExpectations returns the add and del expectations of the controllee.
func (e *ControlleeExpectations) GetExpectations() (int64, int64) {
	return atomic.LoadInt64(&e.add), atomic.LoadInt64(&e.del)
}

// NewControllerExpectations returns a store for ControllerExpectations.
func NewControllerExpectations() *ControllerExpectations {
	return &ControllerExpectations{cache.NewStore(ExpKeyFunc)}
}
