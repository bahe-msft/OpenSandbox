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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alibaba/opensandbox/execd/pkg/activity"
	"github.com/alibaba/opensandbox/execd/pkg/web/model"
)

func TestActivityEndpointDoesNotUpdateActivity(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	tracker := activity.NewTrackerWithClock(func() time.Time { return now })
	router := NewRouter("", tracker)

	first := getActivity(t, router)
	now = now.Add(time.Minute)
	second := getActivity(t, router)

	if !second.LastActivityAt.Equal(first.LastActivityAt) {
		t.Fatalf("activity endpoint changed last_activity_at from %s to %s", first.LastActivityAt, second.LastActivityAt)
	}
	if second.Revision != first.Revision {
		t.Fatalf("activity endpoint changed revision from %d to %d", first.Revision, second.Revision)
	}
	if !second.ObservedAt.Equal(now) {
		t.Fatalf("observed_at = %s, want %s", second.ObservedAt, now)
	}
}

func getActivity(t *testing.T, handler http.Handler) model.ActivityResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/activity", nil)
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
