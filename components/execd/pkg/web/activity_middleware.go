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
	// activityIgnore excludes operational polling and endpoints that instrument themselves.
	activityIgnore activityMode = iota
	// activityPoint records one successful user interaction after the handler returns.
	activityPoint
	// activityBusy keeps active_operations incremented for the full request lifetime.
	activityBusy
)

type activityRoute struct {
	method string
	path   string
}

type activityClassifier map[activityRoute]activityMode

// newActivityClassifier uses Gin route templates so instrumentation stays
// declarative and additions are reviewable alongside router.go.
func newActivityClassifier() activityClassifier {
	return activityClassifier{
		{http.MethodGet, "/ping"}:                                              activityIgnore,
		{http.MethodGet, "/v1/activity"}:                                       activityIgnore,
		{http.MethodPost, "/v1/activity/touch"}:                                activityIgnore,
		{http.MethodGet, "/metrics"}:                                           activityIgnore,
		{http.MethodGet, "/metrics/watch"}:                                     activityIgnore,
		{http.MethodPost, "/command"}:                                          activityBusy,
		{http.MethodDelete, "/command"}:                                        activityPoint,
		{http.MethodGet, "/command/status/:id"}:                                activityIgnore,
		{http.MethodGet, "/command/:id/logs"}:                                  activityIgnore,
		{http.MethodPost, "/code"}:                                             activityBusy,
		{http.MethodDelete, "/code"}:                                           activityPoint,
		{http.MethodPost, "/code/context"}:                                     activityPoint,
		{http.MethodGet, "/code/contexts"}:                                     activityIgnore,
		{http.MethodDelete, "/code/contexts"}:                                  activityPoint,
		{http.MethodGet, "/code/contexts/:contextId"}:                          activityIgnore,
		{http.MethodDelete, "/code/contexts/:contextId"}:                       activityPoint,
		{http.MethodPost, "/session"}:                                          activityPoint,
		{http.MethodPost, "/session/:sessionId/run"}:                           activityBusy,
		{http.MethodDelete, "/session/:sessionId"}:                             activityPoint,
		{http.MethodPost, "/pty"}:                                              activityPoint,
		{http.MethodGet, "/pty/:sessionId"}:                                    activityIgnore,
		{http.MethodDelete, "/pty/:sessionId"}:                                 activityPoint,
		{http.MethodGet, "/pty/:sessionId/ws"}:                                 activityIgnore, // PTY frames instrument themselves.
		{http.MethodDelete, "/files"}:                                          activityBusy,
		{http.MethodGet, "/files/info"}:                                        activityPoint,
		{http.MethodPost, "/files/mv"}:                                         activityBusy,
		{http.MethodPost, "/files/permissions"}:                                activityBusy,
		{http.MethodGet, "/files/search"}:                                      activityPoint,
		{http.MethodPost, "/files/replace"}:                                    activityBusy,
		{http.MethodPost, "/files/upload"}:                                     activityBusy,
		{http.MethodGet, "/files/download"}:                                    activityBusy,
		{http.MethodGet, "/directories/list"}:                                  activityPoint,
		{http.MethodPost, "/directories"}:                                      activityBusy,
		{http.MethodDelete, "/directories"}:                                    activityBusy,
		{http.MethodPost, "/v1/isolated/session"}:                              activityPoint,
		{http.MethodGet, "/v1/isolated/sessions"}:                              activityIgnore,
		{http.MethodGet, "/v1/isolated/session/:sessionId"}:                    activityIgnore,
		{http.MethodPost, "/v1/isolated/session/:sessionId/run"}:               activityBusy,
		{http.MethodDelete, "/v1/isolated/session/:sessionId"}:                 activityPoint,
		{http.MethodGet, "/v1/isolated/session/:sessionId/diff"}:               activityPoint,
		{http.MethodPost, "/v1/isolated/session/:sessionId/commit"}:            activityBusy,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/info"}:         activityPoint,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/download"}:     activityBusy,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/upload"}:      activityBusy,
		{http.MethodDelete, "/v1/isolated/session/:sessionId/files"}:           activityBusy,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/mv"}:          activityBusy,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/permissions"}: activityBusy,
		{http.MethodPost, "/v1/isolated/session/:sessionId/files/replace"}:     activityBusy,
		{http.MethodGet, "/v1/isolated/session/:sessionId/files/search"}:       activityPoint,
		{http.MethodGet, "/v1/isolated/session/:sessionId/directories/list"}:   activityPoint,
		{http.MethodPost, "/v1/isolated/session/:sessionId/directories"}:       activityBusy,
		{http.MethodDelete, "/v1/isolated/session/:sessionId/directories"}:     activityBusy,
		{http.MethodGet, "/v1/isolated/capabilities"}:                          activityIgnore,
	}
}

func activityMiddleware(tracker *activity.Tracker) gin.HandlerFunc {
	classifier := newActivityClassifier()
	return func(ctx *gin.Context) {
		mode := classifier.classify(ctx.Request.Method, ctx.FullPath(), ctx.Request.URL.Path, ctx.GetHeader("Upgrade"))
		switch mode {
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

func (c activityClassifier) classify(method, routePath, requestPath, upgrade string) activityMode {
	if strings.HasPrefix(requestPath, "/proxy/") {
		if strings.EqualFold(upgrade, "websocket") {
			return activityPoint
		}
		return activityBusy
	}
	return c[activityRoute{method: method, path: routePath}]
}
