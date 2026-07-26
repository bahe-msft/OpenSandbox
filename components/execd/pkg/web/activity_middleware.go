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
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/alibaba/opensandbox/execd/pkg/activity"
)

type activityMode int

const (
	activityIgnore activityMode = iota
	activityPoint
	activityBusy
)

func activityMiddleware(tracker *activity.Tracker) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if tracker == nil {
			ctx.Next()
			return
		}

		switch classifyActivity(ctx.Request.Method, ctx.Request.URL.Path, ctx.GetHeader("Upgrade")) {
		case activityBusy:
			end := tracker.Begin()
			defer end()
			ctx.Next()
		case activityPoint:
			ctx.Next()
			if ctx.Writer.Status() < http.StatusBadRequest {
				tracker.Touch()
			}
		default:
			ctx.Next()
		}
	}
}

func classifyActivity(method, path, upgrade string) activityMode {
	if path == "/v1/activity" || path == "/ping" || strings.HasPrefix(path, "/metrics") {
		return activityIgnore
	}

	if strings.HasPrefix(path, "/proxy/") {
		if strings.EqualFold(upgrade, "websocket") {
			return activityPoint
		}
		return activityBusy
	}

	if strings.HasPrefix(path, "/command/status/") || strings.HasSuffix(path, "/logs") {
		return activityIgnore
	}
	if method == http.MethodPost && path == "/command" {
		return activityBusy
	}
	if method == http.MethodDelete && path == "/command" {
		return activityPoint
	}

	if method == http.MethodPost && path == "/code" {
		return activityBusy
	}
	if method == http.MethodPost && path == "/code/context" {
		return activityPoint
	}
	if strings.HasPrefix(path, "/code/contexts") {
		if method == http.MethodDelete {
			return activityPoint
		}
		return activityIgnore
	}
	if method == http.MethodDelete && path == "/code" {
		return activityPoint
	}

	if method == http.MethodPost && path == "/session" {
		return activityPoint
	}
	if method == http.MethodPost && strings.HasPrefix(path, "/session/") && strings.HasSuffix(path, "/run") {
		return activityBusy
	}
	if method == http.MethodDelete && strings.HasPrefix(path, "/session/") {
		return activityPoint
	}

	if strings.HasPrefix(path, "/pty/") {
		if method == http.MethodGet && strings.HasSuffix(path, "/ws") {
			return activityPoint
		}
		if method == http.MethodDelete {
			return activityPoint
		}
		return activityIgnore
	}
	if method == http.MethodPost && path == "/pty" {
		return activityPoint
	}

	if strings.HasPrefix(path, "/v1/isolated/") {
		return classifyIsolatedActivity(method, path)
	}

	if strings.HasPrefix(path, "/files") || strings.HasPrefix(path, "/directories") {
		return classifyFilesystemActivity(method, path)
	}

	return activityIgnore
}

func classifyFilesystemActivity(method, path string) activityMode {
	if path == "/files/upload" || path == "/files/download" {
		return activityBusy
	}
	if method == http.MethodGet {
		return activityPoint
	}
	if method == http.MethodPost || method == http.MethodDelete {
		return activityBusy
	}
	return activityIgnore
}

func classifyIsolatedActivity(method, path string) activityMode {
	if method == http.MethodPost && path == "/v1/isolated/session" {
		return activityPoint
	}
	if method == http.MethodPost && strings.Contains(path, "/run") {
		return activityBusy
	}
	if strings.Contains(path, "/files/upload") || strings.Contains(path, "/files/download") {
		return activityBusy
	}
	if method == http.MethodGet {
		if strings.HasSuffix(path, "/capabilities") || strings.HasSuffix(path, "/sessions") {
			return activityIgnore
		}
		return activityPoint
	}
	if method == http.MethodPost || method == http.MethodDelete {
		return activityBusy
	}
	return activityIgnore
}
