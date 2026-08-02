// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config controls idle candidate selection and lifecycle operations.
type Config struct {
	BaseURL          string
	APIKey           string
	PauseAfter       time.Duration
	GracePeriod      time.Duration
	CheckInterval    time.Duration
	RequestTimeout   time.Duration
	DryRun           bool
	OptInLabel       string
	OptInValue       string
	LeaderElection   bool
	LeaderElectionID string
	MetricsAddress   string
	ProbeAddress     string
}

// Parse reads environment defaults and command-line overrides.
func Parse() (Config, error) {
	pauseAfter, err := durationEnv("PAUSE_AFTER", time.Hour)
	if err != nil {
		return Config{}, err
	}
	gracePeriod, err := durationEnv("GRACE_PERIOD", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	checkInterval, err := durationEnv("CHECK_INTERVAL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	requestTimeout, err := durationEnv("REQUEST_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	dryRun, err := boolEnv("DRY_RUN", true)
	if err != nil {
		return Config{}, err
	}
	leaderElection, err := boolEnv("LEADER_ELECTION", true)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		BaseURL:          os.Getenv("OPENSANDBOX_BASE_URL"),
		APIKey:           os.Getenv("OPENSANDBOX_API_KEY"),
		PauseAfter:       pauseAfter,
		GracePeriod:      gracePeriod,
		CheckInterval:    checkInterval,
		RequestTimeout:   requestTimeout,
		DryRun:           dryRun,
		OptInLabel:       stringEnv("OPT_IN_LABEL", "opensandbox.ai/auto-pause"),
		OptInValue:       stringEnv("OPT_IN_VALUE", "true"),
		LeaderElection:   leaderElection,
		LeaderElectionID: stringEnv("LEADER_ELECTION_ID", "opensandbox-idle-controller"),
		MetricsAddress:   stringEnv("METRICS_ADDRESS", ":8080"),
		ProbeAddress:     stringEnv("PROBE_ADDRESS", ":8081"),
	}

	flag.StringVar(&cfg.BaseURL, "opensandbox-url", cfg.BaseURL, "OpenSandbox lifecycle API base URL")
	flag.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "OpenSandbox lifecycle API key")
	flag.DurationVar(&cfg.PauseAfter, "pause-after", cfg.PauseAfter, "Idle duration before pause eligibility")
	flag.DurationVar(&cfg.GracePeriod, "grace-period", cfg.GracePeriod, "Delay between matching activity observations")
	flag.DurationVar(&cfg.CheckInterval, "check-interval", cfg.CheckInterval, "Maximum interval between activity checks")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "Lifecycle and execd HTTP request timeout")
	flag.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "Log pause decisions without pausing")
	flag.StringVar(&cfg.OptInLabel, "opt-in-label", cfg.OptInLabel, "BatchSandbox label required for auto-pause")
	flag.StringVar(&cfg.OptInValue, "opt-in-value", cfg.OptInValue, "Required opt-in label value")
	flag.BoolVar(&cfg.LeaderElection, "leader-elect", cfg.LeaderElection, "Enable controller-runtime leader election")
	flag.StringVar(&cfg.LeaderElectionID, "leader-election-id", cfg.LeaderElectionID, "Leader election lease name")
	flag.StringVar(&cfg.MetricsAddress, "metrics-bind-address", cfg.MetricsAddress, "Metrics listener address; use 0 to disable")
	flag.StringVar(&cfg.ProbeAddress, "health-probe-bind-address", cfg.ProbeAddress, "Health probe listener address")
	flag.Parse()

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects unsafe or incomplete configuration.
func (c Config) Validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("opensandbox URL is required")
	}
	if c.PauseAfter <= 0 {
		return fmt.Errorf("pause-after must be positive")
	}
	if c.GracePeriod < 0 {
		return fmt.Errorf("grace-period must be non-negative")
	}
	if c.CheckInterval <= 0 {
		return fmt.Errorf("check-interval must be positive")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request-timeout must be positive")
	}
	if c.OptInLabel == "" || c.OptInValue == "" {
		return fmt.Errorf("opt-in label and value are required")
	}
	return nil
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func stringEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
