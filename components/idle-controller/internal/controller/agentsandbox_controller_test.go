// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/alibaba/opensandbox/idle-controller/internal/opensandbox"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAgentSandboxReconcilePausesAfterStableObservation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	lifecycle := &fakeLifecycle{snapshots: []opensandbox.ActivitySnapshot{
		idleSnapshot(now, 4),
		idleSnapshot(now, 4),
	}}
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(newAgentSandbox().GroupVersionKind(), &unstructured.Unstructured{})
	sandbox := newAgentSandbox()
	sandbox.SetName("agent-s1")
	sandbox.SetNamespace("opensandbox")
	sandbox.SetLabels(map[string]string{
		"opensandbox.io/id":         "s1",
		"opensandbox.ai/auto-pause": "true",
	})
	sandbox.Object["spec"] = map[string]any{"replicas": int64(1)}
	sandbox.Object["status"] = map[string]any{
		"conditions": []any{map[string]any{
			"type":               "Ready",
			"status":             "True",
			"reason":             "DependenciesReady",
			"message":            "ready",
			"lastTransitionTime": "2026-07-26T20:00:00Z",
		}},
	}
	policy := &BatchSandboxReconciler{
		Lifecycle: lifecycle,
		Config: Config{
			PauseAfter:    time.Hour,
			GracePeriod:   30 * time.Second,
			CheckInterval: 5 * time.Minute,
			OptInLabel:    "opensandbox.ai/auto-pause",
			OptInValue:    "true",
		},
		Now:          func() time.Time { return now },
		observations: make(map[string]observation),
	}
	reconciler := &AgentSandboxReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(sandbox).Build(),
		Policy: policy,
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "opensandbox", Name: "agent-s1"}}

	result, err := reconciler.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, result.RequeueAfter)

	now = now.Add(30 * time.Second)
	_, err = reconciler.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, []string{"s1"}, lifecycle.pauses)
}

func TestAgentSandboxRunning(t *testing.T) {
	t.Parallel()
	sandbox := newAgentSandbox()
	sandbox.Object["spec"] = map[string]any{"replicas": int64(1)}
	sandbox.Object["status"] = map[string]any{
		"conditions": []any{map[string]any{
			"type":               "Ready",
			"status":             string(metav1.ConditionTrue),
			"reason":             "DependenciesReady",
			"lastTransitionTime": "2026-07-26T20:00:00Z",
		}},
	}
	require.True(t, agentSandboxRunning(sandbox))

	sandbox.Object["spec"] = map[string]any{"replicas": int64(0)}
	require.False(t, agentSandboxRunning(sandbox))
}
