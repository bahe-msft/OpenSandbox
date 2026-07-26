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
	"github.com/gin-gonic/gin"

	"github.com/alibaba/opensandbox/execd/pkg/activity"
	"github.com/alibaba/opensandbox/execd/pkg/web/model"
)

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
}

// NewActivityController creates an activity controller.
func NewActivityController(ctx *gin.Context, tracker *activity.Tracker) *ActivityController {
	return &ActivityController{
		basicController: newBasicController(ctx),
		tracker:         tracker,
	}
}

// Get returns a read-only activity snapshot. This endpoint must not update activity.
func (c *ActivityController) Get() {
	snap := c.tracker.Snapshot()
	c.RespondSuccess(model.ActivityResponse{
		LastActivityAt:   snap.LastActivityAt,
		ObservedAt:       snap.ObservedAt,
		Busy:             snap.Busy,
		ActiveOperations: snap.ActiveOperations,
		Revision:         snap.Revision,
	})
}
