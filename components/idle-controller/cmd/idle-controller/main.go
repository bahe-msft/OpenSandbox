// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	sandboxv1alpha1 "github.com/alibaba/OpenSandbox/sandbox-k8s/apis/sandbox/v1alpha1"
	"github.com/alibaba/opensandbox/idle-controller/internal/config"
	idlecontroller "github.com/alibaba/opensandbox/idle-controller/internal/controller"
	"github.com/alibaba/opensandbox/idle-controller/internal/opensandbox"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: false, TimeEncoder: zapcore.ISO8601TimeEncoder})))
	logger := ctrl.Log.WithName("setup")

	cfg, err := config.Parse()
	if err != nil {
		logger.Error(err, "invalid configuration")
		os.Exit(1)
	}
	lifecycle, err := opensandbox.NewClient(cfg.BaseURL, cfg.APIKey, cfg.RequestTimeout)
	if err != nil {
		logger.Error(err, "create lifecycle client")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		logger.Error(err, "add Kubernetes scheme")
		os.Exit(1)
	}
	if err := sandboxv1alpha1.AddToScheme(scheme); err != nil {
		logger.Error(err, "add BatchSandbox scheme")
		os.Exit(1)
	}

	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: cfg.MetricsAddress,
		},
		HealthProbeBindAddress: cfg.ProbeAddress,
		LeaderElection:         cfg.LeaderElection,
		LeaderElectionID:       cfg.LeaderElectionID,
	})
	if err != nil {
		logger.Error(err, "create manager")
		os.Exit(1)
	}

	policy, err := idlecontroller.NewIdlePolicy(lifecycle, idlecontroller.Config{
		PauseAfter:    cfg.PauseAfter,
		GracePeriod:   cfg.GracePeriod,
		CheckInterval: cfg.CheckInterval,
		DryRun:        cfg.DryRun,
		OptInLabel:    cfg.OptInLabel,
		OptInValue:    cfg.OptInValue,
	})
	if err != nil {
		logger.Error(err, "create idle policy")
		os.Exit(1)
	}
	reconciler := &idlecontroller.BatchSandboxReconciler{
		Client: manager.GetClient(),
		Policy: policy,
	}
	if err := reconciler.SetupWithManager(manager); err != nil {
		logger.Error(err, "register BatchSandbox controller")
		os.Exit(1)
	}
	agentReconciler := &idlecontroller.AgentSandboxReconciler{
		Client: manager.GetClient(),
		Policy: policy,
	}
	if err := agentReconciler.SetupWithManager(manager); err != nil {
		if meta.IsNoMatchError(err) {
			logger.Info("AgentSandbox CRD is not installed; skipping AgentSandbox watch")
		} else {
			logger.Error(err, "register AgentSandbox controller")
			os.Exit(1)
		}
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "add health check")
		os.Exit(1)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "add ready check")
		os.Exit(1)
	}

	logger.Info("starting idle controller", "dryRun", cfg.DryRun, "pauseAfter", cfg.PauseAfter, "gracePeriod", cfg.GracePeriod)
	if err := manager.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "manager stopped")
		os.Exit(1)
	}
}
