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

type activityTrackingMode int

const (
	// activityUntracked excludes operational polling and endpoints that instrument themselves.
	activityUntracked activityTrackingMode = iota
	// activityPointInTime records one successful user interaction after the handler returns.
	activityPointInTime
	// activityRequestLifetime keeps active_operations incremented for the full request lifetime.
	activityRequestLifetime
)

type activityRoute struct {
	method string
	path   string
}

type activityClassifier map[activityRoute]activityTrackingMode

// newActivityClassifier uses Gin route templates so instrumentation stays
// declarative and additions are reviewable alongside router.go.
func newActivityClassifier() activityClassifier {
	return activityClassifier{
		{http.MethodPost, "/command"}:                                          activityRequestLifetime,
		{http.MethodDelete, "/command"}:                                        activityPointInTime,
		{http.MethodPost, "/code"}:                                             activityRequestLifetime,
		{http.MethodDelete, "/code"}:                                           activityPointInTime,
		{http.MethodPost, "/code/context"}:                                     activityPointInTime,
		{http.MethodDelete, "/code/contexts"}:                                  activityPointInTime,
		{http.MethodDelete, "/code/contexts/:contextId"}:                       activityPointInTime,
		{http.MethodPost, "/session"}:                                          activityPointInTime,
		{http.MethodPost, "/session/:sessionId/run"}:                           activityRequestLifetime,
		{http.MethodDelete, "/session/:sessionId"}:                             activityPointInTime,
		{http.MethodPost, "/pty"}:                                              activityPointInTime,
		{http.MethodDelete, "/pty/:sessionId"}:                                 activityPointInTime,
		{http.MethodDelete, "/files"}:                                          activityRequestLifetime,
		{http.MethodGet, "/files/info"}:                                        activityPointInTime,
		{http.MethodPost, "/files/mv"}:                                         activityRequestLifetime,
		{http.MethodPost, "/files/permissions"}:                                activityRequestLifetime,
		{http.MethodGet, "/files/search"}:                                      activityPointInTime,
		{http.MethodPost, "/files/replace"}:                                    activityRequestLifetime,
		{http.MethodPost, "/files/upload"}:                                     activityRequestLifetime,
		{http.MethodGet, "/files/download"}:                                    activityRequestLifetime,
		{http.MethodGet, "/directories/list"}:                                  activityPointInTime,
		{http.MethodPost, "/directories"}:                                      activityRequestLifetime,
		{http.MethodDelete, "/directories"}:                                    activityRequestLifetime,
		{http.MethodPost, "/v1/isolated/session"}:                              activityPointInTime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/run"}:               activityRequestLifetime,
		{http.MethodDelete, "/v1/isolated/session/:sessionId"}:                 activityPointInTime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/diff"}:               activityPointInTime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/commit"}:            activityRequestLifetime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/info"}:         activityPointInTime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/download"}:     activityRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/upload"}:      activityRequestLifetime,
		{http.MethodDelete, "/v1/isolated/session/:sessionId/files"}:           activityRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/mv"}:          activityRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/permissions"}: activityRequestLifetime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/replace"}:     activityRequestLifetime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/search"}:       activityPointInTime,
		{http.MethodGet, "/v1/isolated/session/:sessionId/directories/list"}:   activityPointInTime,
		{http.MethodPost, "/v1/isolated/session/:sessionId/directories"}:       activityRequestLifetime,
		{http.MethodDelete, "/v1/isolated/session/:sessionId/directories"}:     activityRequestLifetime,
	}
}

func activityMiddleware(tracker *activity.Tracker) gin.HandlerFunc {
	classifier := newActivityClassifier()
	return func(ctx *gin.Context) {
		mode := classifier.classify(ctx.Request.Method, ctx.FullPath(), ctx.Request.URL.Path, ctx.GetHeader("Upgrade"))
		switch mode {
		case activityRequestLifetime:
			end := tracker.Begin()
			defer end()
			ctx.Next()
		case activityPointInTime:
			ctx.Next()
			if ctx.Writer.Status() < http.StatusBadRequest {
				tracker.Touch()
			}
		default:
			ctx.Next()
		}
	}
}

func (c activityClassifier) classify(method, routePath, requestPath, upgrade string) activityTrackingMode {
	if strings.HasPrefix(requestPath, "/proxy/") {
		if strings.EqualFold(upgrade, "websocket") {
			return activityPointInTime
		}
		return activityRequestLifetime
	}
	return c[activityRoute{method: method, path: routePath}]
}
