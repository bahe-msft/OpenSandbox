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

type activityRoute struct {
	method string
	path   string
}

type activityHandler func(*activity.Tracker, *gin.Context)

// activityMiddleware builds a route-to-behavior table once and captures it in
// the returned Gin handler. Routes absent from the table are intentionally not
// tracked.
func activityMiddleware(tracker *activity.Tracker) gin.HandlerFunc {
	routes := activityRoutes()
	return func(ctx *gin.Context) {
		handler := routes[activityRoute{method: ctx.Request.Method, path: ctx.FullPath()}]
		if strings.HasPrefix(ctx.Request.URL.Path, "/proxy/") && !strings.EqualFold(ctx.GetHeader("Upgrade"), "websocket") {
			handler = trackRequestLifetime
		}
		if handler == nil {
			ctx.Next()
			return
		}
		handler(tracker, ctx)
	}
}

func activityRoutes() map[activityRoute]activityHandler {
	return map[activityRoute]activityHandler{
		{http.MethodPost, "/command"}:                                          trackRequestLifetime,
		{http.MethodDelete, "/command"}:                                        trackPointInTime,
		{http.MethodPost, "/code"}:                                             trackRequestLifetime,
		{http.MethodDelete, "/code"}:                                           trackPointInTime,
		{http.MethodPost, "/code/context"}:                                     trackPointInTime,
		{http.MethodDelete, "/code/contexts"}:                                  trackPointInTime,
		{http.MethodDelete, "/code/contexts/:contextId"}:                       trackPointInTime,
		{http.MethodPost, "/session"}:                                          trackPointInTime,
		{http.MethodPost, "/session/:sessionId/run"}:                           trackRequestLifetime,
		{http.MethodDelete, "/session/:sessionId"}:                             trackPointInTime,
		{http.MethodPost, "/pty"}:                                              trackPointInTime,
		{http.MethodDelete, "/pty/:sessionId"}:                                 trackPointInTime,
		{http.MethodDelete, "/files"}:                                          trackRequestLifetime,
		{http.MethodGet, "/files/info"}:                                        trackPointInTime,
		{http.MethodPost, "/files/mv"}:                                         trackRequestLifetime,
		{http.MethodPost, "/files/permissions"}:                                trackRequestLifetime,
		{http.MethodGet, "/files/search"}:                                      trackPointInTime,
		{http.MethodPost, "/files/replace"}:                                    trackRequestLifetime,
		{http.MethodPost, "/files/upload"}:                                     trackRequestLifetime,
		{http.MethodGet, "/files/download"}:                                    trackRequestLifetime,
		{http.MethodGet, "/directories/list"}:                                  trackPointInTime,
		{http.MethodPost, "/directories"}:                                      trackRequestLifetime,
		{http.MethodDelete, "/directories"}:                                    trackRequestLifetime,
		{http.MethodPost, "/v1/isolated/session"}:                              trackPointInTime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/run"}:               trackRequestLifetime,
		{http.MethodDelete, "/v1/isolated/session/:sessionId"}:                 trackPointInTime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/diff"}:               trackPointInTime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/commit"}:            trackRequestLifetime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/info"}:         trackPointInTime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/download"}:     trackRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/upload"}:      trackRequestLifetime,
		{http.MethodDelete, "/v1/isolated/session/:sessionId/files"}:           trackRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/mv"}:          trackRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/permissions"}: trackRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/replace"}:     trackRequestLifetime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/search"}:       trackPointInTime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/directories/list"}:   trackPointInTime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/directories"}:       trackRequestLifetime,
		{http.MethodDelete, "/v1/isolated/session/:sessionId/directories"}:     trackRequestLifetime,
	}
}

// trackPointInTime records one successful user interaction after the handler returns.
func trackPointInTime(tracker *activity.Tracker, ctx *gin.Context) {
	ctx.Next()
	if ctx.Writer.Status() < http.StatusBadRequest {
		tracker.Touch()
	}
}

// trackRequestLifetime keeps active_operations incremented for the full request lifetime.
func trackRequestLifetime(tracker *activity.Tracker, ctx *gin.Context) {
	end := tracker.Begin()
	defer end()
	ctx.Next()
}
