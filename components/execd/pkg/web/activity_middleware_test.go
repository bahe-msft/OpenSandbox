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
		name        string
		method      string
		routePath   string
		requestPath string
		upgrade     string
		want        activityMode
	}{
		{name: "activity endpoint ignored", method: http.MethodGet, routePath: "/v1/activity", requestPath: "/v1/activity", want: activityIgnore},
		{name: "activity touch handled by controller", method: http.MethodPost, routePath: "/v1/activity/touch", requestPath: "/v1/activity/touch", want: activityIgnore},
		{name: "health ignored", method: http.MethodGet, routePath: "/ping", requestPath: "/ping", want: activityIgnore},
		{name: "command status ignored", method: http.MethodGet, routePath: "/command/status/:id", requestPath: "/command/status/abc", want: activityIgnore},
		{name: "command logs ignored", method: http.MethodGet, routePath: "/command/:id/logs", requestPath: "/command/abc/logs", want: activityIgnore},
		{name: "foreground command busy", method: http.MethodPost, routePath: "/command", requestPath: "/command", want: activityBusy},
		{name: "command interrupt point", method: http.MethodDelete, routePath: "/command", requestPath: "/command", want: activityPoint},
		{name: "jupyter run busy", method: http.MethodPost, routePath: "/code", requestPath: "/code", want: activityBusy},
		{name: "context create point", method: http.MethodPost, routePath: "/code/context", requestPath: "/code/context", want: activityPoint},
		{name: "context get ignored", method: http.MethodGet, routePath: "/code/contexts/:contextId", requestPath: "/code/contexts/abc", want: activityIgnore},
		{name: "session run busy", method: http.MethodPost, routePath: "/session/:sessionId/run", requestPath: "/session/abc/run", want: activityBusy},
		{name: "pty websocket self tracked", method: http.MethodGet, routePath: "/pty/:sessionId/ws", requestPath: "/pty/abc/ws", want: activityIgnore},
		{name: "pty status ignored", method: http.MethodGet, routePath: "/pty/:sessionId", requestPath: "/pty/abc", want: activityIgnore},
		{name: "upload busy", method: http.MethodPost, routePath: "/files/upload", requestPath: "/files/upload", want: activityBusy},
		{name: "file info point", method: http.MethodGet, routePath: "/files/info", requestPath: "/files/info", want: activityPoint},
		{name: "directory mutation busy", method: http.MethodDelete, routePath: "/directories", requestPath: "/directories", want: activityBusy},
		{name: "proxy http busy", method: http.MethodGet, requestPath: "/proxy/8080/events", want: activityBusy},
		{name: "proxy websocket point", method: http.MethodGet, requestPath: "/proxy/8080/ws", upgrade: "websocket", want: activityPoint},
		{name: "isolated run busy", method: http.MethodPost, routePath: "/v1/isolated/session/:sessionId/run", requestPath: "/v1/isolated/session/abc/run", want: activityBusy},
		{name: "isolated capabilities ignored", method: http.MethodGet, routePath: "/v1/isolated/capabilities", requestPath: "/v1/isolated/capabilities", want: activityIgnore},
		{name: "unknown route ignored", method: http.MethodGet, routePath: "/unknown", requestPath: "/unknown", want: activityIgnore},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyActivity(tt.method, tt.routePath, tt.requestPath, tt.upgrade)
			if got != tt.want {
				t.Fatalf("classifyActivity(%q, %q, %q, %q) = %v, want %v", tt.method, tt.routePath, tt.requestPath, tt.upgrade, got, tt.want)
			}
		})
	}
}
