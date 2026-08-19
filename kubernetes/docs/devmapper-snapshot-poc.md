# Devmapper block snapshot POC

> [!CAUTION]
> This experiment creates dm-thin devices outside containerd's metadata. It has
> no restore implementation, durable allocator, garbage collector, or node-loss
> handling. Run it only on a dedicated replaceable test node.

`cmd/devmapper-snapshot-poc` replaces OCI filesystem diff and registry push with
an immutable local dm-thin clone. It exists only to measure the potential pause
latency improvement before implementing a snapshotter-managed artifact format.

The command accepts the image-committer argument shape, but ignores target image
references and returns `devmapper-poc://` artifact references. OpenSandbox cannot
resume those references.

## Required access

The POC container requires:

- the host containerd socket at `/run/containerd/containerd.sock`;
- the host containerd FIFO directory at `/run/containerd/fifo`;
- the host `/dev/mapper` directory;
- UID 0 and a privileged security context; and
- `DEVMAPPER_POC_ACKNOWLEDGE_UNTRACKED_DEVICE=true`.

The normal image-committer Job intentionally does not provide dmsetup access.
Run this image in a manually reviewed privileged Job pinned to the source Pod's
node. Do not change the default image-committer security context for this POC.

## Operation

For each selected source container, the POC:

1. resolves its containerd writable snapshot;
2. runs `sync` in the container;
3. pauses the container task;
4. resolves the active snapshot's `/dev/mapper` device and thin ID;
5. allocates an ID from the reserved POC range;
6. suspends the source device, issues `create_snap`, and resumes the source;
7. resumes the container task; and
8. reports per-stage timings and a `devmapper-poc://` reference.

The default untracked ID range is `[16000000,16500000)`. Override it with
`DEVMAPPER_POC_DEVICE_ID_MIN` and `DEVMAPPER_POC_DEVICE_ID_MAX`. The range must
not overlap IDs managed by containerd.

Successful artifacts intentionally remain in the thinpool. Delete each reported
ID after collecting results:

```bash
DEVMAPPER_POC_ACKNOWLEDGE_UNTRACKED_DEVICE=true \
  image-committer cleanup containerd-thinpool <device-id> [...]
```

## Changed-block artifact experiment

The image also includes two experimental helpers:

- `block-artifact` packs `thin_delta` XML plus data read from the immutable
  clone into `opensandbox.devmapper.block.v1`, and applies that payload to a
  fresh clone of the recorded base snapshot;
- `acr-oras-login` exchanges AKS Workload Identity for an ACR refresh token and
  authenticates ORAS without persisting the federated or registry token.

The payload can be stored as an OCI artifact with these media types:

```text
artifact type: application/vnd.opensandbox.devmapper.snapshot.v1
block layer:   application/vnd.opensandbox.devmapper.blocks.v1+gzip
```

This is an OCI Distribution artifact, not an OCI container image. Restore still
requires a matching base snapshot and an explicit block-application step before
containerd can use the reconstructed snapshot.

A test-cluster round trip using `python:3.12-slim` produced a 1.75 MiB raw delta
compressed to about 111 KiB. ACR push, pull, block application, and a read-only
mount check all succeeded; a marker written before cloning was present in the
reconstructed filesystem. The snapshot-committer identity has `AcrPush` but not
delete permission, so test artifact deletion requires separate retention or
cleanup authorization.

The warmed `aks-rp-md` rootfs had 187,217 changed 64 KiB blocks relative to its
image parent, approximately 11.4 GiB before compression. Do not run that export
without reviewing temporary disk, ACR storage, and retention impact.

## Build

```bash
cd kubernetes
docker build -f Dockerfile.devmapper-snapshot-poc \
  -t devmapper-snapshot-poc:dev .
```

The `Publish AKS Images` workflow also exposes the explicitly selected
`devmapper-snapshot-poc` component. It is excluded from the workflow's `all`
selection.
