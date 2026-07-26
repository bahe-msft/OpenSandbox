// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	sandboxv1alpha1 "github.com/alibaba/OpenSandbox/sandbox-k8s/apis/sandbox/v1alpha1"
	"github.com/alibaba/opensandbox/idle-controller/internal/opensandbox"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=sandbox.opensandbox.io,resources=batchsandboxes,verbs=get;list;watch

// Config controls reconciliation policy.
type Config struct {
	PauseAfter    time.Duration
	GracePeriod   time.Duration
	CheckInterval time.Duration
	DryRun        bool
	OptInLabel    string
	OptInValue    string
}

type observation struct {
	revision uint64
	at       time.Time
}

// BatchSandboxReconciler watches opted-in BatchSandbox resources and applies idle policy.
type BatchSandboxReconciler struct {
	client.Client
	Lifecycle opensandbox.Lifecycle
	Config    Config
	Now       func() time.Time

	mu           sync.Mutex
	observations map[string]observation
}

// SetupWithManager registers the BatchSandbox watch.
func (r *BatchSandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Lifecycle == nil {
		return fmt.Errorf("lifecycle client is required")
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.observations == nil {
		r.observations = make(map[string]observation)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&sandboxv1alpha1.BatchSandbox{}).
		Complete(r)
}

// Reconcile checks one running sandbox and requeues at the next policy boundary.
func (r *BatchSandboxReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	key := candidateKey("batchsandbox", request.NamespacedName)
	var sandbox sandboxv1alpha1.BatchSandbox
	if err := r.Get(ctx, request.NamespacedName, &sandbox); err != nil {
		r.forget(key)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !sandbox.DeletionTimestamp.IsZero() || sandbox.Labels[r.Config.OptInLabel] != r.Config.OptInValue {
		r.forget(key)
		return ctrl.Result{}, nil
	}
	if sandbox.Status.Phase != sandboxv1alpha1.BatchSandboxPhaseSucceed || sandbox.Status.Ready == 0 || paused(&sandbox) {
		r.forget(key)
		return ctrl.Result{}, nil
	}
	sandboxID := sandbox.Labels["opensandbox.io/id"]
	if sandboxID == "" {
		sandboxID = sandbox.Name
	}
	return r.reconcileCandidate(ctx, key, sandboxID)
}

func (r *BatchSandboxReconciler) reconcileCandidate(ctx context.Context, key, sandboxID string) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("candidate", key, "sandboxID", sandboxID)
	endpoint, err := r.Lifecycle.ResolveExecdEndpoint(ctx, sandboxID)
	if err != nil {
		return ctrl.Result{RequeueAfter: r.Config.CheckInterval}, fmt.Errorf("resolve execd endpoint: %w", err)
	}
	snapshot, err := r.Lifecycle.Activity(ctx, endpoint)
	if err != nil {
		return ctrl.Result{RequeueAfter: r.Config.CheckInterval}, fmt.Errorf("read execd activity: %w", err)
	}

	now := r.Now().UTC()
	if delay, reason := r.untilIdle(snapshot, now); delay > 0 {
		r.forget(key)
		logger.V(1).Info("sandbox is not idle", "reason", reason, "requeueAfter", delay)
		return ctrl.Result{RequeueAfter: min(delay, r.Config.CheckInterval)}, nil
	}

	first, ok := r.observation(key)
	if !ok || first.revision != snapshot.Revision {
		r.remember(key, observation{revision: snapshot.Revision, at: now})
		return ctrl.Result{RequeueAfter: r.Config.GracePeriod}, nil
	}
	if graceRemaining := r.Config.GracePeriod - now.Sub(first.at); graceRemaining > 0 {
		return ctrl.Result{RequeueAfter: graceRemaining}, nil
	}

	if r.Config.DryRun {
		logger.Info("sandbox is eligible for idle pause", "dryRun", true, "revision", snapshot.Revision)
		r.forget(key)
		return ctrl.Result{RequeueAfter: r.Config.CheckInterval}, nil
	}
	if err := r.Lifecycle.Pause(ctx, sandboxID); err != nil {
		return ctrl.Result{RequeueAfter: r.Config.CheckInterval}, fmt.Errorf("pause sandbox: %w", err)
	}
	logger.Info("requested idle pause", "revision", snapshot.Revision)
	r.forget(key)
	return ctrl.Result{}, nil
}

func (r *BatchSandboxReconciler) untilIdle(snapshot opensandbox.ActivitySnapshot, now time.Time) (time.Duration, string) {
	if snapshot.Busy || snapshot.ActiveOperations > 0 {
		return r.Config.CheckInterval, "active operation"
	}
	if snapshot.KeepAwakeUntil != nil && snapshot.KeepAwakeUntil.After(now) {
		return snapshot.KeepAwakeUntil.Sub(now), "keep-awake deadline"
	}
	idleAt := snapshot.LastActivityAt.Add(r.Config.PauseAfter)
	if idleAt.After(now) {
		return idleAt.Sub(now), "idle threshold"
	}
	return 0, "idle"
}

func paused(sandbox *sandboxv1alpha1.BatchSandbox) bool {
	return sandbox.Spec.Pause != nil && *sandbox.Spec.Pause
}

func (r *BatchSandboxReconciler) observation(key string) (observation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.observations[key]
	return value, ok
}

func (r *BatchSandboxReconciler) remember(key string, value observation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations[key] = value
}

func (r *BatchSandboxReconciler) forget(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.observations, key)
}

func candidateKey(kind string, name types.NamespacedName) string {
	return kind + ":" + name.String()
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
