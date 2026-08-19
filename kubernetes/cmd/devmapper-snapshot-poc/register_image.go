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

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"time"

	containerd "github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func registerLocalImage(ctx context.Context, args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: image-committer register-local-image <base-image> <active-snapshot-key> <target-image>")
	}
	baseReference, activeKey, targetReference := args[0], args[1], args[2]
	client, err := containerd.New(envOr("CONTAINERD_SOCKET", defaultContainerdSock), containerd.WithDefaultNamespace(envOr("CONTAINERD_NAMESPACE", defaultContainerdNS)))
	if err != nil {
		return err
	}
	defer client.Close()
	baseImage, err := client.GetImage(ctx, baseReference)
	if err != nil {
		return fmt.Errorf("load base image %s: %w", baseReference, err)
	}
	store := client.ContentStore()
	manifest, err := images.Manifest(ctx, store, baseImage.Target(), platforms.Default())
	if err != nil {
		return fmt.Errorf("load base manifest: %w", err)
	}
	configData, err := content.ReadBlob(ctx, store, manifest.Config)
	if err != nil {
		return err
	}
	var config ocispec.Image
	if err := json.Unmarshal(configData, &config); err != nil {
		return err
	}

	uncompressed, compressed, err := emptyLayer()
	if err != nil {
		return err
	}
	diffID := digest.FromBytes(uncompressed)
	layerDescriptor := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(compressed), Size: int64(len(compressed))}
	if err := content.WriteBlob(ctx, store, "opensandbox-block-empty-"+layerDescriptor.Digest.String(), bytes.NewReader(compressed), layerDescriptor, content.WithLabels(map[string]string{"containerd.io/uncompressed": diffID.String()})); err != nil {
		return fmt.Errorf("write synthetic layer: %w", err)
	}

	now := time.Now().UTC()
	config.Created = &now
	config.RootFS.DiffIDs = append(config.RootFS.DiffIDs, diffID)
	config.History = append(config.History, ocispec.History{Created: &now, CreatedBy: "OpenSandbox devmapper block restore POC"})
	newConfig, err := json.Marshal(config)
	if err != nil {
		return err
	}
	configDescriptor := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(newConfig), Size: int64(len(newConfig))}
	if err := content.WriteBlob(ctx, store, "opensandbox-block-config-"+configDescriptor.Digest.String(), bytes.NewReader(newConfig), configDescriptor); err != nil {
		return err
	}

	layers := append([]ocispec.Descriptor(nil), manifest.Layers...)
	layers = append(layers, layerDescriptor)
	newManifest := ocispec.Manifest{Versioned: manifest.Versioned, MediaType: ocispec.MediaTypeImageManifest, Config: configDescriptor, Layers: layers, Annotations: manifest.Annotations}
	manifestData, err := json.Marshal(newManifest)
	if err != nil {
		return err
	}
	manifestDescriptor := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromBytes(manifestData), Size: int64(len(manifestData))}
	labels := map[string]string{"containerd.io/gc.ref.content.0": configDescriptor.Digest.String()}
	for index, layer := range layers {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.%d", index+1)] = layer.Digest.String()
	}
	if err := content.WriteBlob(ctx, store, "opensandbox-block-manifest-"+manifestDescriptor.Digest.String(), bytes.NewReader(manifestData), manifestDescriptor, content.WithLabels(labels)); err != nil {
		return err
	}

	chainID := identity.ChainID(config.RootFS.DiffIDs).String()
	if err := client.SnapshotService("devmapper").Commit(ctx, chainID, activeKey); err != nil {
		return fmt.Errorf("commit restored snapshot as %s: %w", chainID, err)
	}
	imageRecord := images.Image{Name: targetReference, Target: manifestDescriptor, CreatedAt: now, UpdatedAt: now}
	if _, err := client.ImageService().Create(ctx, imageRecord); err != nil {
		return fmt.Errorf("create local restored image: %w", err)
	}
	fmt.Printf("registered target=%s chainID=%s manifest=%s\n", targetReference, chainID, manifestDescriptor.Digest)
	return nil
}

func emptyLayer() ([]byte, []byte, error) {
	var raw bytes.Buffer
	tarWriter := tar.NewWriter(&raw)
	if err := tarWriter.Close(); err != nil {
		return nil, nil, err
	}
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write(raw.Bytes()); err != nil {
		return nil, nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, nil, err
	}
	return raw.Bytes(), compressed.Bytes(), nil
}
