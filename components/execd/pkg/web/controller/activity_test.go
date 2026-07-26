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
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/alibaba/opensandbox/execd/pkg/activity"
	"github.com/alibaba/opensandbox/execd/pkg/web/model"
	"github.com/stretchr/testify/require"
)

func TestActivityControllerGetIsReadOnly(t *testing.T) {
	tracker := activity.NewTracker()
	before := tracker.Snapshot()
	ctx, recorder := newTestContext(http.MethodGet, "/v1/activity", nil)

	NewActivityController(ctx, tracker, DefaultActivityConfig()).Get()

	require.Equal(t, http.StatusOK, recorder.Code)
	var response model.ActivityResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, before.Revision, response.Revision)
	require.Equal(t, before.LastActivityAt, response.LastActivityAt)
}

func TestActivityControllerTouchKeepAlive(t *testing.T) {
	tracker := activity.NewTracker()
	ctx, recorder := newTestContext(
		http.MethodPost,
		"/v1/activity/touch",
		[]byte(`{"keep_alive_seconds":60}`),
	)

	NewActivityController(ctx, tracker, DefaultActivityConfig()).Touch()

	require.Equal(t, http.StatusOK, recorder.Code)
	var response model.ActivityResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.EqualValues(t, 1, response.Revision)
	require.NotNil(t, response.KeepAwakeUntil)
	require.WithinDuration(t, time.Now().UTC().Add(time.Minute), *response.KeepAwakeUntil, time.Second)
}

func TestActivityControllerTouchRejectsConfiguredMaximum(t *testing.T) {
	tracker := activity.NewTracker()
	ctx, recorder := newTestContext(
		http.MethodPost,
		"/v1/activity/touch",
		[]byte(`{"keep_alive_seconds":61}`),
	)

	NewActivityController(ctx, tracker, ActivityConfig{MaxKeepAliveDuration: time.Minute}).Touch()

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.EqualValues(t, 0, tracker.Snapshot().Revision)
}
