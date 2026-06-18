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

package envoyproxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/alibaba/opensandbox/egress/pkg/log"
)

type Config struct {
	Path        string
	ListenPort  int
	AdminPort   int
	ExtProcAddr string
	WorkDir     string
	CertPath    string
	KeyPath     string
	UID         uint32
	GID         uint32
	OnExit      func(error)
}

type Running struct {
	Cmd  *exec.Cmd
	done chan error
}

func Launch(cfg Config) (*Running, error) {
	if cfg.Path == "" {
		cfg.Path = "envoy"
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/tmp/opensandbox-envoy"
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}
	bootstrap := filepath.Join(cfg.WorkDir, "envoy.yaml")
	if err := os.WriteFile(bootstrap, []byte(BootstrapYAML(BootstrapConfig{ListenPort: cfg.ListenPort, AdminPort: cfg.AdminPort, ExtProcAddr: cfg.ExtProcAddr, CertPath: cfg.CertPath, KeyPath: cfg.KeyPath})), 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command(cfg.Path, "-c", bootstrap, "--log-level", "info")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cfg.UID != 0 || cfg.GID != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: cfg.UID, Gid: cfg.GID}}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("envoy: start: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		done <- err
		if cfg.OnExit != nil {
			cfg.OnExit(err)
		}
	}()
	log.Infof("[envoy] started (pid %d, listen 127.0.0.1:%s, admin 127.0.0.1:%s)", cmd.Process.Pid, strconv.Itoa(cfg.ListenPort), strconv.Itoa(cfg.AdminPort))
	return &Running{Cmd: cmd, done: done}, nil
}

func GracefulShutdown(r *Running, timeout time.Duration) {
	if r == nil || r.Cmd == nil || r.Cmd.Process == nil {
		return
	}
	_ = r.Cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-r.done:
	case <-time.After(timeout):
		_ = r.Cmd.Process.Kill()
		<-r.done
	}
}
