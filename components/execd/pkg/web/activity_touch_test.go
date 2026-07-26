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

package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alibaba/opensandbox/execd/pkg/activity"
	"github.com/alibaba/opensandbox/execd/pkg/web/controller"
	"github.com/alibaba/opensandbox/execd/pkg/web/model"
)

func TestActivityTouchUpdatesRevision(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tracker := activity.NewTrackerWithClock(func() time.Time { return now })
	router := NewRouter("", tracker)

	initial := getActivity(t, router)
	now = now.Add(time.Second)
	resp := postActivityTouch(t, router, nil)

	if resp.Revision != initial.Revision+1 {
		t.Fatalf("revision = %d, want %d", resp.Revision, initial.Revision+1)
	}
	if !resp.LastActivityAt.Equal(now) {
		t.Fatalf("last_activity_at = %s, want %s", resp.LastActivityAt, now)
	}
	if resp.KeepAwakeUntil != nil {
		t.Fatalf("keep_awake_until = %s, want nil", *resp.KeepAwakeUntil)
	}
}

func TestActivityTouchKeepAlive(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tracker := activity.NewTrackerWithClock(func() time.Time { return now })
	router := NewRouter("", tracker)

	resp := postActivityTouch(t, router, map[string]any{"keep_alive_seconds": 60})
	if resp.KeepAwakeUntil == nil {
		t.Fatal("keep_awake_until is nil")
	}
	want := now.Add(time.Minute)
	if !resp.KeepAwakeUntil.Equal(want) {
		t.Fatalf("keep_awake_until = %s, want %s", *resp.KeepAwakeUntil, want)
	}
	if resp.Busy {
		t.Fatal("keepalive should not mark busy")
	}
}

func TestActivityTouchRejectsTooLongKeepAlive(t *testing.T) {
	tracker := activity.NewTracker()
	router := NewRouter("", tracker)

	body, err := json.Marshal(map[string]any{"keep_alive_seconds": 24*60*60 + 1})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/activity/touch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestActivityTouchRejectsHugeKeepAliveWithoutOverflow(t *testing.T) {
	tracker := activity.NewTracker()
	router := NewRouter("", tracker)

	body, err := json.Marshal(map[string]any{"keep_alive_seconds": int64(1<<63 - 1)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/activity/touch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestActivityTouchUsesConfiguredMaxKeepAlive(t *testing.T) {
	tracker := activity.NewTracker()
	router := NewRouter("", tracker, controller.ActivityConfig{MaxKeepAliveDuration: time.Minute})

	ok := postActivityTouch(t, router, map[string]any{"keep_alive_seconds": 60})
	if ok.KeepAwakeUntil == nil {
		t.Fatal("keep_awake_until is nil")
	}

	body, err := json.Marshal(map[string]any{"keep_alive_seconds": 61})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/activity/touch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func postActivityTouch(t *testing.T, handler http.Handler, payload map[string]any) model.ActivityResponse {
	t.Helper()
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/activity/touch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp model.ActivityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal activity response: %v", err)
	}
	return resp
}
