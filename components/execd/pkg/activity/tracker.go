// Copyright 2026 Alibaba Group Holding Ltd.
//
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

package activity

import (
	"sync"
	"time"
)

// Snapshot is a consistent point-in-time view of execd activity state.
type Snapshot struct {
	LastActivityAt   time.Time
	ObservedAt       time.Time
	Busy             bool
	ActiveOperations uint64
	Revision         uint64
}

// Tracker records process-wide execd activity for idle detection.
type Tracker struct {
	mu       sync.Mutex
	last     time.Time
	active   uint64
	revision uint64
	now      func() time.Time
}

// NewTracker creates a tracker initialized with activity at startup time.
func NewTracker() *Tracker {
	return NewTrackerWithClock(time.Now)
}

// NewTrackerWithClock creates a tracker using a custom clock. It is intended for tests.
func NewTrackerWithClock(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	t := &Tracker{now: now}
	t.last = t.currentLocked()
	return t
}

// Touch records point-in-time activity.
func (t *Tracker) Touch() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordLocked()
	t.revision++
}

// Begin marks a long-running operation as active and returns an idempotent completion closure.
func (t *Tracker) Begin() func() {
	if t == nil {
		return func() {}
	}
	t.mu.Lock()
	t.recordLocked()
	t.active++
	t.revision++
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.active > 0 {
				t.active--
			}
			t.recordLocked()
			t.revision++
		})
	}
}

// Snapshot returns a consistent activity view. It does not update activity state.
func (t *Tracker) Snapshot() Snapshot {
	if t == nil {
		now := time.Now().UTC()
		return Snapshot{LastActivityAt: now, ObservedAt: now}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	observed := t.currentLocked()
	if observed.Before(t.last) {
		observed = t.last
	}
	return Snapshot{
		LastActivityAt:   t.last,
		ObservedAt:       observed,
		Busy:             t.active > 0,
		ActiveOperations: t.active,
		Revision:         t.revision,
	}
}

func (t *Tracker) recordLocked() {
	now := t.currentLocked()
	if now.Before(t.last) {
		return
	}
	t.last = now
}

func (t *Tracker) currentLocked() time.Time {
	return t.now().UTC()
}
