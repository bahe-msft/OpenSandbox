// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	sandboxv1alpha1 "github.com/alibaba/OpenSandbox/sandbox-k8s/apis/sandbox/v1alpha1"
	"github.com/alibaba/opensandbox/idle-controller/internal/opensandbox"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeLifecycle struct {
	snapshots []opensandbox.ActivitySnapshot
	pauses    []string
}

func (f *fakeLifecycle) ResolveExecdEndpoint(context.Context, string) (opensandbox.Endpoint, error) {
	return opensandbox.Endpoint{URL: "http://execd"}, nil
}

func (f *fakeLifecycle) Activity(context.Context, opensandbox.Endpoint) (opensandbox.ActivitySnapshot, error) {
	value := f.snapshots[0]
	if len(f.snapshots) > 1 {
		f.snapshots = f.snapshots[1:]
	}
	return value, nil
}

func (f *fakeLifecycle) Pause(_ context.Context, sandboxID string) error {
	f.pauses = append(f.pauses, sandboxID)
	return nil
}

func TestNewIdlePolicyValidatesDependencies(t *testing.T) {
	t.Parallel()
	_, err := NewIdlePolicy(nil, Config{})
	require.Error(t, err)

	_, err = NewIdlePolicy(&fakeLifecycle{}, Config{})
	require.Error(t, err)
}

func TestReconcilePausesAfterStableGraceObservation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	lifecycle := &fakeLifecycle{snapshots: []opensandbox.ActivitySnapshot{
		idleSnapshot(now, 7),
		idleSnapshot(now, 7),
	}}
	reconciler := newTestReconciler(t, lifecycle, &now, false)
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "opensandbox", Name: "s1"}}

	result, err := reconciler.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, result.RequeueAfter)
	require.Empty(t, lifecycle.pauses)

	now = now.Add(30 * time.Second)
	_, err = reconciler.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, []string{"s1"}, lifecycle.pauses)
}

func TestReconcileRestartsGraceWhenRevisionChanges(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	lifecycle := &fakeLifecycle{snapshots: []opensandbox.ActivitySnapshot{
		idleSnapshot(now, 7),
		idleSnapshot(now, 8),
	}}
	reconciler := newTestReconciler(t, lifecycle, &now, false)
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "opensandbox", Name: "s1"}}

	_, err := reconciler.Reconcile(context.Background(), request)
	require.NoError(t, err)
	now = now.Add(30 * time.Second)
	result, err := reconciler.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, result.RequeueAfter)
	require.Empty(t, lifecycle.pauses)
}

func TestReconcileHonorsKeepAwake(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	keepAwake := now.Add(10 * time.Minute)
	value := idleSnapshot(now, 7)
	value.KeepAwakeUntil = &keepAwake
	lifecycle := &fakeLifecycle{snapshots: []opensandbox.ActivitySnapshot{value}}
	reconciler := newTestReconciler(t, lifecycle, &now, false)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "opensandbox", Name: "s1"}})
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, result.RequeueAfter)
	require.Empty(t, lifecycle.pauses)
}

func newTestReconciler(t *testing.T, lifecycle opensandbox.Lifecycle, now *time.Time, dryRun bool) *BatchSandboxReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, sandboxv1alpha1.AddToScheme(scheme))
	one := int32(1)
	sandbox := &sandboxv1alpha1.BatchSandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "s1",
			Namespace: "opensandbox",
			UID:       types.UID("uid-s1"),
			Labels:    map[string]string{"opensandbox.ai/auto-pause": "true"},
		},
		Spec: sandboxv1alpha1.BatchSandboxSpec{Replicas: &one},
		Status: sandboxv1alpha1.BatchSandboxStatus{
			Phase: sandboxv1alpha1.BatchSandboxPhaseSucceed,
			Ready: 1,
		},
	}
	policy, err := NewIdlePolicy(lifecycle, Config{
		PauseAfter:    time.Hour,
		GracePeriod:   30 * time.Second,
		CheckInterval: 5 * time.Minute,
		DryRun:        dryRun,
		OptInLabel:    "opensandbox.ai/auto-pause",
		OptInValue:    "true",
	})
	require.NoError(t, err)
	policy.now = func() time.Time { return *now }
	return &BatchSandboxReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(sandbox).Build(),
		Policy: policy,
	}
}

func idleSnapshot(now time.Time, revision uint64) opensandbox.ActivitySnapshot {
	return opensandbox.ActivitySnapshot{
		LastActivityAt: now.Add(-2 * time.Hour),
		ObservedAt:     now,
		Revision:       revision,
	}
}
