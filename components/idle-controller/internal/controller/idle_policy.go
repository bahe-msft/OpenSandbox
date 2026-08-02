// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/alibaba/opensandbox/idle-controller/internal/opensandbox"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Config controls idle-policy reconciliation.
type Config struct {
	PauseAfter    time.Duration
	GracePeriod   time.Duration
	CheckInterval time.Duration
	DryRun        bool
	OptInLabel    string
	OptInValue    string
}

func (c Config) validate() error {
	if c.PauseAfter <= 0 {
		return fmt.Errorf("pause-after must be positive")
	}
	if c.GracePeriod < 0 {
		return fmt.Errorf("grace-period must be non-negative")
	}
	if c.CheckInterval <= 0 {
		return fmt.Errorf("check-interval must be positive")
	}
	if c.OptInLabel == "" || c.OptInValue == "" {
		return fmt.Errorf("opt-in label and value are required")
	}
	return nil
}

type observation struct {
	revision uint64
	at       time.Time
}

// IdlePolicy owns provider-neutral activity observations and pause decisions.
type IdlePolicy struct {
	lifecycle opensandbox.Lifecycle
	config    Config
	now       func() time.Time

	mu           sync.Mutex
	observations map[string]observation
}

// NewIdlePolicy constructs a provider-neutral idle policy.
func NewIdlePolicy(lifecycle opensandbox.Lifecycle, config Config) (*IdlePolicy, error) {
	if lifecycle == nil {
		return nil, fmt.Errorf("lifecycle client is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &IdlePolicy{
		lifecycle:    lifecycle,
		config:       config,
		now:          time.Now,
		observations: make(map[string]observation),
	}, nil
}

func (p *IdlePolicy) reconcileCandidate(ctx context.Context, key, sandboxID string) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("candidate", key, "sandboxID", sandboxID)
	endpoint, err := p.lifecycle.ResolveExecdEndpoint(ctx, sandboxID)
	if err != nil {
		logger.Error(err, "resolve execd endpoint")
		return ctrl.Result{RequeueAfter: p.config.CheckInterval}, nil
	}
	snapshot, err := p.lifecycle.Activity(ctx, endpoint)
	if err != nil {
		logger.Error(err, "read execd activity")
		return ctrl.Result{RequeueAfter: p.config.CheckInterval}, nil
	}

	now := p.now().UTC()
	if delay, reason := p.untilIdle(snapshot, now); delay > 0 {
		p.forget(key)
		logger.V(1).Info("sandbox is not idle", "reason", reason, "requeueAfter", delay)
		return ctrl.Result{RequeueAfter: min(delay, p.config.CheckInterval)}, nil
	}

	first, ok := p.observation(key)
	if !ok || first.revision != snapshot.Revision {
		p.remember(key, observation{revision: snapshot.Revision, at: now})
		return ctrl.Result{RequeueAfter: p.config.GracePeriod}, nil
	}
	if graceRemaining := p.config.GracePeriod - now.Sub(first.at); graceRemaining > 0 {
		return ctrl.Result{RequeueAfter: graceRemaining}, nil
	}

	if p.config.DryRun {
		logger.Info("sandbox is eligible for idle pause", "dryRun", true, "revision", snapshot.Revision)
		p.forget(key)
		return ctrl.Result{RequeueAfter: p.config.CheckInterval}, nil
	}
	if err := p.lifecycle.Pause(ctx, sandboxID); err != nil {
		logger.Error(err, "pause sandbox")
		return ctrl.Result{RequeueAfter: p.config.CheckInterval}, nil
	}
	logger.Info("requested idle pause", "revision", snapshot.Revision)
	p.forget(key)
	return ctrl.Result{}, nil
}

func (p *IdlePolicy) untilIdle(snapshot opensandbox.ActivitySnapshot, now time.Time) (time.Duration, string) {
	if snapshot.Busy || snapshot.ActiveOperations > 0 {
		return p.config.CheckInterval, "active operation"
	}
	if snapshot.KeepAwakeUntil != nil && snapshot.KeepAwakeUntil.After(now) {
		return snapshot.KeepAwakeUntil.Sub(now), "keep-awake deadline"
	}
	idleAt := snapshot.LastActivityAt.Add(p.config.PauseAfter)
	if idleAt.After(now) {
		return idleAt.Sub(now), "idle threshold"
	}
	return 0, "idle"
}

func (p *IdlePolicy) observation(key string) (observation, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value, ok := p.observations[key]
	return value, ok
}

func (p *IdlePolicy) remember(key string, value observation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observations[key] = value
}

func (p *IdlePolicy) forget(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.observations, key)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
