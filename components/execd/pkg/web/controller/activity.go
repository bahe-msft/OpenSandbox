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

package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/alibaba/opensandbox/execd/pkg/activity"
	"github.com/alibaba/opensandbox/execd/pkg/web/model"
)

// ActivityConfig controls activity endpoint behavior.
type ActivityConfig struct {
	MaxKeepAliveDuration time.Duration
}

// DefaultActivityConfig returns the default activity endpoint configuration.
func DefaultActivityConfig() ActivityConfig {
	return ActivityConfig{MaxKeepAliveDuration: 24 * time.Hour}
}

func (c ActivityConfig) normalized() ActivityConfig {
	if c.MaxKeepAliveDuration <= 0 {
		c.MaxKeepAliveDuration = DefaultActivityConfig().MaxKeepAliveDuration
	}
	return c
}

var activityTracker *activity.Tracker

// InitActivityTracker wires the process-wide execd activity tracker.
func InitActivityTracker(tracker *activity.Tracker) {
	activityTracker = tracker
}

func touchActivity() {
	if activityTracker != nil {
		activityTracker.Touch()
	}
}

// ActivityController handles /v1/activity.
type ActivityController struct {
	*basicController
	tracker *activity.Tracker
	config  ActivityConfig
}

// NewActivityController creates an activity controller.
func NewActivityController(ctx *gin.Context, tracker *activity.Tracker, config ActivityConfig) *ActivityController {
	return &ActivityController{
		basicController: newBasicController(ctx),
		tracker:         tracker,
		config:          config.normalized(),
	}
}

// Get returns a read-only activity snapshot. This endpoint must not update activity.
func (c *ActivityController) Get() {
	c.RespondSuccess(activityResponse(c.tracker.Snapshot()))
}

// Touch records user activity and optionally holds the sandbox awake for a bounded duration.
func (c *ActivityController) Touch() {
	var req model.ActivityTouchRequest
	if err := c.bindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.RespondError(http.StatusBadRequest, model.ErrorCodeInvalidRequest, fmt.Sprintf("error parsing request: %v", err))
		return
	}
	if req.KeepAliveSeconds < 0 {
		c.RespondError(http.StatusBadRequest, model.ErrorCodeInvalidRequest, "keep_alive_seconds must be non-negative")
		return
	}

	keepAlive := time.Duration(req.KeepAliveSeconds) * time.Second
	if keepAlive > c.config.MaxKeepAliveDuration {
		c.RespondError(
			http.StatusBadRequest,
			model.ErrorCodeInvalidRequest,
			fmt.Sprintf("keep_alive_seconds must not exceed %d", int64(c.config.MaxKeepAliveDuration/time.Second)),
		)
		return
	}

	if keepAlive > 0 {
		c.tracker.KeepAwake(keepAlive)
	} else {
		c.tracker.Touch()
	}
	c.RespondSuccess(activityResponse(c.tracker.Snapshot()))
}

func activityResponse(snap activity.Snapshot) model.ActivityResponse {
	resp := model.ActivityResponse{
		LastActivityAt:   snap.LastActivityAt,
		ObservedAt:       snap.ObservedAt,
		Busy:             snap.Busy,
		ActiveOperations: snap.ActiveOperations,
		Revision:         snap.Revision,
	}
	if !snap.KeepAwakeUntil.IsZero() {
		keepAwakeUntil := snap.KeepAwakeUntil
		resp.KeepAwakeUntil = &keepAwakeUntil
	}
	return resp
}
