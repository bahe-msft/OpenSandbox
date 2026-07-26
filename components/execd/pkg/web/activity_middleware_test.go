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
	"net/http"
	"testing"
)

func TestClassifyActivity(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		upgrade string
		want    activityMode
	}{
		{name: "activity endpoint ignored", method: http.MethodGet, path: "/v1/activity", want: activityIgnore},
		{name: "health ignored", method: http.MethodGet, path: "/ping", want: activityIgnore},
		{name: "command status ignored", method: http.MethodGet, path: "/command/status/abc", want: activityIgnore},
		{name: "command logs ignored", method: http.MethodGet, path: "/command/abc/logs", want: activityIgnore},
		{name: "foreground command busy", method: http.MethodPost, path: "/command", want: activityBusy},
		{name: "command interrupt point", method: http.MethodDelete, path: "/command", want: activityPoint},
		{name: "jupyter run busy", method: http.MethodPost, path: "/code", want: activityBusy},
		{name: "context create point", method: http.MethodPost, path: "/code/context", want: activityPoint},
		{name: "context get ignored", method: http.MethodGet, path: "/code/contexts/abc", want: activityIgnore},
		{name: "session run busy", method: http.MethodPost, path: "/session/abc/run", want: activityBusy},
		{name: "pty websocket point", method: http.MethodGet, path: "/pty/abc/ws", want: activityPoint},
		{name: "pty status ignored", method: http.MethodGet, path: "/pty/abc", want: activityIgnore},
		{name: "upload busy", method: http.MethodPost, path: "/files/upload", want: activityBusy},
		{name: "file info point", method: http.MethodGet, path: "/files/info", want: activityPoint},
		{name: "directory mutation busy", method: http.MethodDelete, path: "/directories", want: activityBusy},
		{name: "proxy http busy", method: http.MethodGet, path: "/proxy/8080/events", want: activityBusy},
		{name: "proxy websocket point", method: http.MethodGet, path: "/proxy/8080/ws", upgrade: "websocket", want: activityPoint},
		{name: "isolated run busy", method: http.MethodPost, path: "/v1/isolated/session/abc/run", want: activityBusy},
		{name: "isolated capabilities ignored", method: http.MethodGet, path: "/v1/isolated/capabilities", want: activityIgnore},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyActivity(tt.method, tt.path, tt.upgrade)
			if got != tt.want {
				t.Fatalf("classifyActivity(%q, %q, %q) = %v, want %v", tt.method, tt.path, tt.upgrade, got, tt.want)
			}
		})
	}
}
