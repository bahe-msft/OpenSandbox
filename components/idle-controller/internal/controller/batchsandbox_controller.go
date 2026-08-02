// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	sandboxv1alpha1 "github.com/alibaba/OpenSandbox/sandbox-k8s/apis/sandbox/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=sandbox.opensandbox.io,resources=batchsandboxes,verbs=get;list;watch

// BatchSandboxReconciler watches opted-in BatchSandbox resources and applies idle policy.
type BatchSandboxReconciler struct {
	client.Client
	Policy *IdlePolicy
}

// SetupWithManager registers the BatchSandbox watch.
func (r *BatchSandboxReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Policy == nil {
		return fmt.Errorf("idle policy is required")
	}
	return ctrl.NewControllerManagedBy(manager).
		For(&sandboxv1alpha1.BatchSandbox{}).
		Complete(r)
}

// Reconcile selects OpenSandbox BatchSandbox resources that are ready and opted in.
func (r *BatchSandboxReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	key := candidateKey("batchsandbox", request.NamespacedName)
	var sandbox sandboxv1alpha1.BatchSandbox
	if err := r.Get(ctx, request.NamespacedName, &sandbox); err != nil {
		r.Policy.forget(key)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !sandbox.DeletionTimestamp.IsZero() || sandbox.Labels[r.Policy.config.OptInLabel] != r.Policy.config.OptInValue {
		r.Policy.forget(key)
		return ctrl.Result{}, nil
	}
	if sandbox.Status.Phase != sandboxv1alpha1.BatchSandboxPhaseSucceed || sandbox.Status.Ready == 0 || paused(&sandbox) {
		r.Policy.forget(key)
		return ctrl.Result{}, nil
	}
	sandboxID := sandbox.Labels[sandboxIDLabel]
	if sandboxID == "" {
		sandboxID = sandbox.Name
	}
	return r.Policy.reconcileCandidate(ctx, key, sandboxID)
}

func paused(sandbox *sandboxv1alpha1.BatchSandbox) bool {
	return sandbox.Spec.Pause != nil && *sandbox.Spec.Pause
}

func candidateKey(kind string, name types.NamespacedName) string {
	return kind + ":" + name.String()
}
