// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	valid := Config{
		BaseURL:        "http://opensandbox-server",
		PauseAfter:     time.Hour,
		GracePeriod:    30 * time.Second,
		CheckInterval:  5 * time.Minute,
		RequestTimeout: 30 * time.Second,
		OptInLabel:     "opensandbox.ai/auto-pause",
		OptInValue:     "true",
	}
	require.NoError(t, valid.Validate())

	invalid := valid
	invalid.PauseAfter = 0
	require.Error(t, invalid.Validate())
}

func TestDurationEnvRejectsInvalidValue(t *testing.T) {
	t.Setenv("TEST_DURATION", "invalid")
	_, err := durationEnv("TEST_DURATION", time.Minute)
	require.Error(t, err)
}

func TestBoolEnvRejectsInvalidValue(t *testing.T) {
	t.Setenv("TEST_BOOL", "invalid")
	_, err := boolEnv("TEST_BOOL", true)
	require.Error(t, err)
}
