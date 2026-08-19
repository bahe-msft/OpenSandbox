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

// devmapper-snapshot-poc benchmarks an intentionally untracked dm-thin clone.
// It is not a production image committer and cannot restore its artifacts.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	containerd "github.com/containerd/containerd"

	"github.com/alibaba/OpenSandbox/sandbox-k8s/pkg/imagecommitter"
)

const (
	acknowledgementEnv     = "DEVMAPPER_POC_ACKNOWLEDGE_UNTRACKED_DEVICE"
	defaultContainerdSock  = "/run/containerd/containerd.sock"
	defaultContainerdNS    = "k8s.io"
	defaultPoolName        = "containerd-thinpool"
	defaultDeviceIDMin     = uint32(16_000_000)
	defaultDeviceIDMax     = uint32(16_500_000)
	terminationMessagePath = "/dev/termination-log"
)

type artifact struct {
	ContainerName string        `json:"containerName"`
	PoolName      string        `json:"poolName"`
	DeviceID      uint32        `json:"deviceID"`
	SourceDevice  string        `json:"sourceDevice"`
	SourceID      uint32        `json:"sourceID"`
	Suspend       time.Duration `json:"-"`
	Clone         time.Duration `json:"-"`
	Resume        time.Duration `json:"-"`
}

type outputContainer struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Digest string `json:"digest"`
}

type output struct {
	Containers []outputContainer `json:"containers"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if strings.ToLower(strings.TrimSpace(os.Getenv(acknowledgementEnv))) != "true" {
		return fmt.Errorf("%s=true is required because this POC creates devices outside containerd metadata", acknowledgementEnv)
	}
	if len(args) > 0 && args[0] == "cleanup" {
		return cleanup(args[1:])
	}
	if len(args) < 3 {
		return errors.New("usage: devmapper-snapshot-poc <pod> <namespace> <container>:<ignored-target> [...]")
	}

	client, err := containerd.New(envOr("CONTAINERD_SOCKET", defaultContainerdSock), containerd.WithDefaultNamespace(envOr("CONTAINERD_NAMESPACE", defaultContainerdNS)))
	if err != nil {
		return fmt.Errorf("connect to containerd: %w", err)
	}
	defer client.Close()

	runtime := imagecommitter.NewContainerdRuntime(client)
	podName, namespace := args[0], args[1]
	podUID := strings.TrimSpace(os.Getenv("SOURCE_POD_UID"))
	containers := make([]imagecommitter.ResolvedContainer, 0, len(args)-2)
	for _, raw := range args[2:] {
		name, _, ok := strings.Cut(raw, ":")
		if !ok || name == "" {
			return fmt.Errorf("invalid container argument %q", raw)
		}
		resolved, err := runtime.Resolve(ctx, imagecommitter.ContainerSelector{PodName: podName, PodNamespace: namespace, PodUID: podUID, ContainerName: name})
		if err != nil {
			return fmt.Errorf("resolve %s: %w", name, err)
		}
		if resolved.Snapshotter != "devmapper" {
			return fmt.Errorf("container %s uses snapshotter %q, want devmapper", name, resolved.Snapshotter)
		}
		containers = append(containers, resolved)
		fmt.Printf("resolved container=%s id=%s snapshotKey=%s state=%s\n", name, resolved.ID, resolved.SnapshotKey, resolved.State)
	}

	for _, container := range containers {
		if container.State != imagecommitter.TaskStateRunning {
			continue
		}
		started := time.Now()
		result, err := runtime.Exec(ctx, container, imagecommitter.ExecRequest{Args: []string{"sync"}})
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("sync container %s: exit=%d: %w", container.Name, result.ExitCode, err)
		}
		fmt.Printf("phase=sync container=%s duration=%s\n", container.Name, time.Since(started))
	}

	handles := make([]imagecommitter.PauseHandle, 0, len(containers))
	artifacts := make([]artifact, 0, len(containers))
	success := false
	defer func() {
		if success {
			return
		}
		for _, a := range artifacts {
			_ = dmsetup("message", a.PoolName, "0", fmt.Sprintf("delete %d", a.DeviceID))
		}
		for i := len(handles) - 1; i >= 0; i-- {
			_ = runtime.Resume(context.Background(), handles[i].Container)
		}
	}()

	for _, container := range containers {
		started := time.Now()
		handle, err := runtime.Pause(ctx, container)
		if err != nil {
			return fmt.Errorf("pause container %s: %w", container.Name, err)
		}
		if handle.PausedByUs {
			handles = append(handles, handle)
		}
		fmt.Printf("phase=task-pause container=%s duration=%s\n", container.Name, time.Since(started))
	}

	pool := envOr("DEVMAPPER_POC_POOL", defaultPoolName)
	for _, container := range containers {
		a, err := cloneActiveSnapshot(ctx, client, pool, container)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, a)
		fmt.Printf("phase=block-clone container=%s source=%s sourceID=%d cloneID=%d suspend=%s clone=%s resume=%s critical=%s\n",
			a.ContainerName, a.SourceDevice, a.SourceID, a.DeviceID, a.Suspend, a.Clone, a.Resume, a.Suspend+a.Clone+a.Resume)
	}

	for i := len(handles) - 1; i >= 0; i-- {
		started := time.Now()
		if err := runtime.Resume(ctx, handles[i].Container); err != nil {
			return fmt.Errorf("resume container %s: %w", handles[i].Container.Name, err)
		}
		fmt.Printf("phase=task-resume container=%s duration=%s\n", handles[i].Container.Name, time.Since(started))
	}
	handles = nil

	result := output{Containers: make([]outputContainer, 0, len(artifacts))}
	for _, a := range artifacts {
		ref := fmt.Sprintf("devmapper-poc://%s/%d?source=%s&sourceID=%d", a.PoolName, a.DeviceID, a.SourceDevice, a.SourceID)
		digest := sha256.Sum256([]byte(ref))
		result.Containers = append(result.Containers, outputContainer{Name: a.ContainerName, Image: ref, Digest: "sha256:" + hex.EncodeToString(digest[:])})
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := os.WriteFile(terminationMessagePath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	fmt.Printf("%s\n", data)
	success = true
	return nil
}

func cloneActiveSnapshot(ctx context.Context, client *containerd.Client, pool string, container imagecommitter.ResolvedContainer) (artifact, error) {
	mounts, err := client.SnapshotService(container.Snapshotter).Mounts(ctx, container.SnapshotKey)
	if err != nil {
		return artifact{}, fmt.Errorf("get snapshot mounts for %s: %w", container.Name, err)
	}
	if len(mounts) != 1 || !strings.HasPrefix(mounts[0].Source, "/dev/mapper/") {
		return artifact{}, fmt.Errorf("container %s has unsupported snapshot mounts: %+v", container.Name, mounts)
	}
	sourceDevice := filepath.Base(mounts[0].Source)
	table, err := dmsetupOutput("table", sourceDevice)
	if err != nil {
		return artifact{}, err
	}
	sourceID, err := parseThinDeviceID(table)
	if err != nil {
		return artifact{}, fmt.Errorf("parse source device %s: %w", sourceDevice, err)
	}
	a := artifact{ContainerName: container.Name, PoolName: pool, SourceDevice: sourceDevice, SourceID: sourceID}
	started := time.Now()
	if err := dmsetup("suspend", sourceDevice); err != nil {
		return artifact{}, err
	}
	a.Suspend = time.Since(started)
	resumed := false
	defer func() {
		if !resumed {
			_ = dmsetup("resume", sourceDevice)
		}
	}()

	started = time.Now()
	cloneID, err := createClone(pool, sourceID)
	if err != nil {
		return artifact{}, err
	}
	a.DeviceID = cloneID
	a.Clone = time.Since(started)
	started = time.Now()
	if err := dmsetup("resume", sourceDevice); err != nil {
		_ = dmsetup("message", pool, "0", fmt.Sprintf("delete %d", cloneID))
		return artifact{}, err
	}
	resumed = true
	a.Resume = time.Since(started)
	return a, nil
}

func createClone(pool string, sourceID uint32) (uint32, error) {
	minID, err := envUint32("DEVMAPPER_POC_DEVICE_ID_MIN", defaultDeviceIDMin)
	if err != nil {
		return 0, err
	}
	maxID, err := envUint32("DEVMAPPER_POC_DEVICE_ID_MAX", defaultDeviceIDMax)
	if err != nil {
		return 0, err
	}
	if minID >= maxID || maxID > 0xFFFFFF {
		return 0, fmt.Errorf("invalid POC device ID range [%d,%d)", minID, maxID)
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, err
	}
	span := maxID - minID
	start := minID + binary.LittleEndian.Uint32(random[:])%span
	for i := uint32(0); i < 100; i++ {
		candidate := minID + (start-minID+i)%span
		err := dmsetup("message", pool, "0", fmt.Sprintf("create_snap %d %d", candidate, sourceID))
		if err == nil {
			return candidate, nil
		}
		if !strings.Contains(strings.ToLower(err.Error()), "file exists") {
			return 0, fmt.Errorf("create clone ID %d: %w", candidate, err)
		}
	}
	return 0, errors.New("could not allocate an untracked POC device ID")
}

func cleanup(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: devmapper-snapshot-poc cleanup <pool> <device-id> [...]")
	}
	for _, raw := range args[1:] {
		id, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return err
		}
		if err := dmsetup("message", args[0], "0", fmt.Sprintf("delete %d", id)); err != nil {
			return err
		}
		fmt.Printf("deleted pool=%s deviceID=%d\n", args[0], id)
	}
	return nil
}

func parseThinDeviceID(table string) (uint32, error) {
	fields := strings.Fields(table)
	if len(fields) < 5 || fields[2] != "thin" {
		return 0, fmt.Errorf("unexpected dm table %q", table)
	}
	value, err := strconv.ParseUint(fields[4], 10, 32)
	return uint32(value), err
}

func dmsetup(args ...string) error {
	_, err := dmsetupOutput(args...)
	return err
}

func dmsetupOutput(args ...string) (string, error) {
	data, err := exec.Command("dmsetup", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("dmsetup %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(data)), err)
	}
	return strings.TrimSpace(string(data)), nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envUint32(name string, fallback uint32) (uint32, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	return uint32(value), err
}
