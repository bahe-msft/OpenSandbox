// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sandboxIDLabel = "opensandbox.io/id"

// +kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch

// AgentSandboxReconciler watches agent-sandbox Sandbox CRs and delegates idle policy.
type AgentSandboxReconciler struct {
	client.Client
	Policy *BatchSandboxReconciler
}

// SetupWithManager registers the unstructured AgentSandbox watch without taking
// a Go dependency on the external agent-sandbox API module.
func (r *AgentSandboxReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Policy == nil {
		return fmt.Errorf("idle policy reconciler is required")
	}
	if _, err := manager.GetRESTMapper().RESTMapping(
		schema.GroupKind{Group: "agents.x-k8s.io", Kind: "Sandbox"},
		"v1alpha1",
	); err != nil {
		return err
	}
	sandbox := newAgentSandbox()
	return ctrl.NewControllerManagedBy(manager).
		Named("agentsandbox-idle").
		For(sandbox).
		Complete(r)
}

// Reconcile selects OpenSandbox-managed, ready AgentSandbox resources.
func (r *AgentSandboxReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	key := candidateKey("agentsandbox", request.NamespacedName)
	sandbox := newAgentSandbox()
	if err := r.Get(ctx, request.NamespacedName, sandbox); err != nil {
		r.Policy.forget(key)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !sandbox.GetDeletionTimestamp().IsZero() || sandbox.GetLabels()[r.Policy.Config.OptInLabel] != r.Policy.Config.OptInValue {
		r.Policy.forget(key)
		return ctrl.Result{}, nil
	}
	sandboxID := sandbox.GetLabels()[sandboxIDLabel]
	if sandboxID == "" || !agentSandboxRunning(sandbox) {
		r.Policy.forget(key)
		return ctrl.Result{}, nil
	}
	return r.Policy.reconcileCandidate(ctx, key, sandboxID)
}

func newAgentSandbox() *unstructured.Unstructured {
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	return sandbox
}

func agentSandboxRunning(sandbox *unstructured.Unstructured) bool {
	replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
	if err != nil || (found && replicas == 0) {
		return false
	}
	conditions, found, err := unstructured.NestedSlice(sandbox.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	ready := meta.FindStatusCondition(toConditions(conditions), "Ready")
	return ready != nil && ready.Status == metav1.ConditionTrue
}

func toConditions(values []any) []metav1.Condition {
	conditions := make([]metav1.Condition, 0, len(values))
	for _, value := range values {
		raw, ok := value.(map[string]any)
		if !ok {
			continue
		}
		condition := metav1.Condition{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &condition); err == nil {
			conditions = append(conditions, condition)
		}
	}
	return conditions
}
