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
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/alibaba/opensandbox/execd/pkg/activity"
)

func TestActivityMiddlewareTracksRequestLifetime(t *testing.T) {
	tracker := activity.NewTracker()
	router := activityTestRouter(tracker)
	var during activity.Snapshot
	router.POST("/command", func(ctx *gin.Context) {
		during = tracker.Snapshot()
		ctx.Status(http.StatusOK)
	})

	serveActivityRequest(router, http.MethodPost, "/command")

	if !during.Busy || during.ActiveOperations != 1 {
		t.Fatalf("during request = %+v, want busy with one active operation", during)
	}
	after := tracker.Snapshot()
	if after.Busy || after.ActiveOperations != 0 || after.Revision != 2 {
		t.Fatalf("after request = %+v, want idle revision=2", after)
	}
}

func TestActivityMiddlewareTracksSuccessfulPointEvent(t *testing.T) {
	tracker := activity.NewTracker()
	router := activityTestRouter(tracker)
	router.POST("/pty", func(ctx *gin.Context) { ctx.Status(http.StatusCreated) })

	serveActivityRequest(router, http.MethodPost, "/pty")

	if revision := tracker.Snapshot().Revision; revision != 1 {
		t.Fatalf("revision = %d, want 1", revision)
	}
}

func TestActivityMiddlewareDoesNotTrackFailedPointEvent(t *testing.T) {
	tracker := activity.NewTracker()
	router := activityTestRouter(tracker)
	router.POST("/pty", func(ctx *gin.Context) { ctx.Status(http.StatusBadRequest) })

	serveActivityRequest(router, http.MethodPost, "/pty")

	if revision := tracker.Snapshot().Revision; revision != 0 {
		t.Fatalf("revision = %d, want 0", revision)
	}
}

func TestActivityMiddlewareLeavesUntrackedRouteUnchanged(t *testing.T) {
	tracker := activity.NewTracker()
	router := activityTestRouter(tracker)
	router.GET("/ping", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })

	serveActivityRequest(router, http.MethodGet, "/ping")

	if revision := tracker.Snapshot().Revision; revision != 0 {
		t.Fatalf("revision = %d, want 0", revision)
	}
}

func activityTestRouter(tracker *activity.Tracker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(activityMiddleware(tracker))
	return router
}

func serveActivityRequest(router http.Handler, method, path string) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	router.ServeHTTP(recorder, request)
}
