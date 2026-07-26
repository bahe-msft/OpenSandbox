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

package model

import "time"

// ActivityTouchRequest records point-in-time activity and optionally holds the sandbox awake.
type ActivityTouchRequest struct {
	// KeepAliveSeconds prevents idle controllers from pausing the sandbox until the returned keep_awake_until.
	// A value of 0 records activity without a keep-awake deadline.
	KeepAliveSeconds int64 `json:"keep_alive_seconds,omitempty"`
}

// ActivityResponse describes execd-local activity for external idle controllers.
type ActivityResponse struct {
	LastActivityAt   time.Time  `json:"last_activity_at"`
	ObservedAt       time.Time  `json:"observed_at"`
	Busy             bool       `json:"busy"`
	ActiveOperations uint64     `json:"active_operations"`
	Revision         uint64     `json:"revision"`
	KeepAwakeUntil   *time.Time `json:"keep_awake_until,omitempty"`
}
