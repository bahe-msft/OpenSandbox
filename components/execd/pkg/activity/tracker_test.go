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
	"testing"
	"time"
)

func TestTrackerInitializesAtStartup(t *testing.T) {
	start := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tr := NewTrackerWithClock(func() time.Time { return start })

	snap := tr.Snapshot()
	if !snap.LastActivityAt.Equal(start) {
		t.Fatalf("last activity = %s, want %s", snap.LastActivityAt, start)
	}
	if snap.Busy {
		t.Fatal("new tracker should not be busy")
	}
	if snap.ActiveOperations != 0 {
		t.Fatalf("active operations = %d, want 0", snap.ActiveOperations)
	}
	if snap.Revision != 0 {
		t.Fatalf("revision = %d, want 0", snap.Revision)
	}
}

func TestTrackerBeginEndTransitions(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tr := NewTrackerWithClock(func() time.Time { return now })

	now = now.Add(time.Second)
	end := tr.Begin()
	snap := tr.Snapshot()
	if !snap.Busy || snap.ActiveOperations != 1 || snap.Revision != 1 {
		t.Fatalf("begin snapshot = %+v, want busy active=1 revision=1", snap)
	}
	if !snap.LastActivityAt.Equal(now) {
		t.Fatalf("last activity after begin = %s, want %s", snap.LastActivityAt, now)
	}

	now = now.Add(time.Second)
	end()
	snap = tr.Snapshot()
	if snap.Busy || snap.ActiveOperations != 0 || snap.Revision != 2 {
		t.Fatalf("end snapshot = %+v, want idle active=0 revision=2", snap)
	}
	if !snap.LastActivityAt.Equal(now) {
		t.Fatalf("last activity after end = %s, want %s", snap.LastActivityAt, now)
	}
}

func TestTrackerCompletionIsIdempotent(t *testing.T) {
	now := time.Now()
	tr := NewTrackerWithClock(func() time.Time { return now })
	end := tr.Begin()
	end()
	end()

	snap := tr.Snapshot()
	if snap.ActiveOperations != 0 {
		t.Fatalf("active operations = %d, want 0", snap.ActiveOperations)
	}
	if snap.Revision != 2 {
		t.Fatalf("revision = %d, want 2", snap.Revision)
	}
}

func TestTrackerTouch(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tr := NewTrackerWithClock(func() time.Time { return now })
	now = now.Add(10 * time.Second)
	tr.Touch()

	snap := tr.Snapshot()
	if snap.Busy {
		t.Fatal("touch should not mark tracker busy")
	}
	if snap.Revision != 1 {
		t.Fatalf("revision = %d, want 1", snap.Revision)
	}
	if !snap.LastActivityAt.Equal(now) {
		t.Fatalf("last activity = %s, want %s", snap.LastActivityAt, now)
	}
}

func TestTrackerIgnoresClockRegression(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tr := NewTrackerWithClock(func() time.Time { return now })
	advance := now.Add(time.Minute)
	now = advance
	tr.Touch()

	now = advance.Add(-time.Hour)
	tr.Touch()
	snap := tr.Snapshot()
	if !snap.LastActivityAt.Equal(advance) {
		t.Fatalf("last activity regressed to %s, want %s", snap.LastActivityAt, advance)
	}
	if !snap.ObservedAt.Equal(advance) {
		t.Fatalf("observed at = %s, want clamped %s", snap.ObservedAt, advance)
	}
}

func TestTrackerKeepAwake(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tr := NewTrackerWithClock(func() time.Time { return now })

	tr.KeepAwake(30 * time.Second)
	snap := tr.Snapshot()
	wantUntil := now.Add(30 * time.Second)
	if !snap.KeepAwakeUntil.Equal(wantUntil) {
		t.Fatalf("keep awake until = %s, want %s", snap.KeepAwakeUntil, wantUntil)
	}
	if snap.Busy {
		t.Fatal("keep awake should not make tracker busy")
	}
	if snap.Revision != 1 {
		t.Fatalf("revision = %d, want 1", snap.Revision)
	}

	now = now.Add(time.Minute)
	snap = tr.Snapshot()
	if !snap.KeepAwakeUntil.IsZero() {
		t.Fatalf("expired keep awake should be omitted from snapshot, got %s", snap.KeepAwakeUntil)
	}
	if snap.Revision != 1 {
		t.Fatalf("snapshot should not mutate revision, got %d", snap.Revision)
	}
}

func TestTrackerKeepAwakeOnlyExtends(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tr := NewTrackerWithClock(func() time.Time { return now })

	tr.KeepAwake(time.Hour)
	first := tr.Snapshot().KeepAwakeUntil
	now = now.Add(time.Minute)
	tr.KeepAwake(time.Minute)
	if got := tr.Snapshot().KeepAwakeUntil; !got.Equal(first) {
		t.Fatalf("shorter keep awake moved deadline to %s, want %s", got, first)
	}
}

func TestTrackerConcurrentSnapshotConsistency(t *testing.T) {
	tr := NewTracker()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				end := tr.Begin()
				tr.Touch()
				_ = tr.Snapshot()
				end()
			}
		}()
	}
	wg.Wait()

	snap := tr.Snapshot()
	if snap.Busy || snap.ActiveOperations != 0 {
		t.Fatalf("final snapshot = %+v, want idle", snap)
	}
}
