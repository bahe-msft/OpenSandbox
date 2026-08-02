---
title: Containerd-Native Image Committer Plugin
authors:
  - "@bcho"
creation-date: 2026-07-20
last-updated: 2026-07-20
status: provisional
---

# Containerd-Native Image Committer Plugin

## Table of Contents

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Commit Interface](#commit-interface)
  - [Unpause Interface](#unpause-interface)
  - [Implementation Interfaces](#implementation-interfaces)
  - [Registry Credential Providers](#registry-credential-providers)
  - [API Changes](#api-changes)
  - [Annotation / Label Contract Changes](#annotation--label-contract-changes)
- [Upgrade Strategy](#upgrade-strategy)
- [Test Plan](#test-plan)
- [Implementation History](#implementation-history)

## Summary

Replace the current `nerdctl`-based image committer with a containerd-native
implementation. Preserve the executable contract used by the Kubernetes
controller so different image-committer implementations can be selected through
the existing controller configuration.

The reference implementation separates container operations, image creation,
registry push, and credential lookup behind provider interfaces. This allows
cloud-specific authentication, such as Azure Workload Identity, AWS IRSA or Pod
Identity, and Google Workload Identity, without changing the snapshot flow or
requiring a registry login file.

## Motivation

The current image committer uses `nerdctl` for container lookup, pause,
unpause, image commit, registry login, push, and image inspection. This couples
snapshot behavior to a command-line tool, its output, and its credential store.

The current registry flow also assumes a mounted Docker config. That works for
static credentials but is not a clean extension point for short-lived cloud
credentials.

A defined executable contract allows other implementations to provide the same
commit and unpause behavior. Provider interfaces inside the reference
implementation keep container runtime and registry-specific behavior
replaceable, similar to how CRI and CNI define stable boundaries between a
caller and different providers.

### Goals

- Define commit and unpause inputs, outputs, and required behavior.
- Remove the reference implementation's dependency on `nerdctl`.
- Use containerd APIs for container lifecycle and local image creation.
- Make unpause idempotent and safe to retry.
- Support static, token-based, and cloud-specific registry credential
  providers.
- Allow operators to select the ServiceAccount used by commit Jobs so cloud
  workload identity can be configured without changing a sandbox request.

### Non-Goals

- Dynamic Go plugins or shared objects.
- Per-sandbox image-committer selection through `osb` or the lifecycle API.
- Process or memory checkpoint/restore.
- Guest filesystem quiescing or flushing. Implementations may perform
  runtime-specific preparation before pause, but it is not part of the
  interoperable image-committer contract.
- Application-level quiescing or transaction consistency.
- Reducing the trust level of the commit Job; the containerd socket still gives
  it node-level runtime access.
- Implementing every cloud credential provider in the first version.

## Proposal

An image-committer implementation is a trusted executable in the image selected
by:

```text
--image-committer-image
```

or the Helm value:

```text
controller.snapshot.imageCommitterImage
```

The controller executes the binary at:

```text
/usr/local/bin/image-committer
```

The controller sets `IMAGE_COMMITTER_API_VERSION=v1` for both operations. An
implementation may reject an unsupported version. For compatibility with the
current unversioned command, an absent value is interpreted as `v1`.

Any implementation may be used if it satisfies the commit and unpause
interfaces below.

### User Stories

#### Use a different image-committer implementation

An operator supplies an image containing a compatible
`/usr/local/bin/image-committer`. The Kubernetes controller invokes it without
requiring changes to `BatchSandbox`, `SandboxSnapshot`, SDKs, or `osb`.

#### Use workload identity for registry push

An implementation obtains short-lived registry credentials from the Pod's
cloud workload identity and pushes snapshot images without a login command or
Docker login file.

### Commit Interface

The commit interface creates and pushes snapshot images for one or more
containers in a source Pod.

#### Invocation

```text
image-committer <pod-name> <namespace> <container-name>:<target-image> [...]
```

Example:

```text
image-committer sandbox-0 default \
  sandbox:registry.example.com/snapshots/sandbox:snap-gen2
```

#### Inputs

##### Arguments

| Position | Name | Required | Description |
|---|---|---:|---|
| 1 | `pod-name` | yes | Source Kubernetes Pod name |
| 2 | `namespace` | yes | Source Kubernetes Pod namespace |
| 3...N | `container-name:target-image` | yes | Container name and destination image; at least one is required |

The first colon in a container specification separates the container name from
the full image reference. The image reference may contain additional colons,
including a registry port and image tag.

##### Environment

| Variable | Default | Required | Description |
|---|---|---:|---|
| `IMAGE_COMMITTER_API_VERSION` | `v1` | no | Image-committer executable contract version |
| `CONTAINERD_SOCKET` | `/run/containerd/containerd.sock` | no | Containerd socket address |
| `CONTAINERD_NAMESPACE` | `k8s.io` | no | Containerd namespace |
| `SOURCE_POD_UID` | empty | no | Pod UID used to avoid stale container matches |
| `SNAPSHOT_REGISTRY_INSECURE` | unset | no | Explicit boolean override for insecure registry transport |

`IMAGE_COMMITTER_API_VERSION` and `SOURCE_POD_UID` are proposed additions. When
`SNAPSHOT_REGISTRY_INSECURE` is unset, implementations preserve the current
compatibility heuristic: local and private-address registry hosts use insecure
transport and other hosts use secure transport.

Credential providers may use projected ServiceAccount tokens and
provider-injected environment variables. Provider selection is owned by the
image-committer implementation and is not part of the executable contract.
Implementations must continue to support anonymous registries. The reference
implementation also supports the existing Docker config mount at:

```text
/var/run/opensandbox/registry/config.json
```

##### Controller configuration

| Controller flag | Helm value | Default | Description |
|---|---|---|---|
| `--image-committer-service-account` | `controller.snapshot.imageCommitterServiceAccount` | `""` | ServiceAccount name assigned to commit Jobs |
| `--image-committer-pod-labels` | `controller.snapshot.imageCommitterPodLabels` | `""` / `{}` | Labels assigned to commit Job Pods |

An empty ServiceAccount setting preserves Kubernetes defaulting. When set, the
ServiceAccount must exist in every sandbox namespace where a commit Job may be
created. The ServiceAccount's cloud annotations or associations select the
specific workload identity, role, or managed identity available to the plugin.
Pod labels let operators trigger provider admission webhooks without hard-coding
provider-specific metadata in the controller.

##### Runtime resources

The commit Job provides:

- the host containerd socket mounted at the path in `CONTAINERD_SOCKET`,
- the host containerd FIFO directory mounted at `/run/containerd/fifo` so an
  implementation may use containerd task exec,
- the source Pod name, namespace, and container names,
- target image references,
- the configured image-committer ServiceAccount and its projected workload
  identity inputs,
- optional registry credential inputs.

The Job runs on the same node as the source Pod. OpenSandbox sets the configured
commit Job ServiceAccount and Pod labels. Cluster admission webhooks remain
responsible for projected tokens and environment variables. The plugin must
document its ServiceAccount, admission, and identity prerequisites.

The initial extension is intentionally limited to ServiceAccount and Pod label
selection. A follow-up may define a generic commit Job template override for
annotations, environment, volumes, and other provider-specific settings.

#### Expected behavior

An implementation must:

1. Validate all inputs.
2. Resolve all requested containers before changing task state.
3. Match containers by Pod name, namespace, container name, and Pod UID when
   supplied.
4. Support both running and stopped containers.
5. Pause running containers and track which tasks were paused by this process.
6. Create a local OCI image from each container's writable snapshot.
7. Resume every task paused by this process before starting registry push.
8. Push every image to its requested target.
9. Write the pushed image results to the termination message.

Stopped containers skip pause but remain eligible for image creation.

On failure, the implementation must make a best-effort attempt to resume all
tasks it paused. Signal and cancellation handling must perform the same cleanup.

Whether one pause failure aborts all image creation or remains best effort must
be resolved before this proposal moves to `implementable` status.

#### Output

On success, the implementation writes JSON to `/dev/termination-log`:

```json
{
  "containers": [
    {
      "name": "sandbox",
      "image": "registry.example.com/snapshots/sandbox:snap-gen2",
      "digest": "sha256:0123456789abcdef"
    }
  ]
}
```

| Field | Required | Description |
|---|---:|---|
| `containers` | yes | One result for every requested container, in request order |
| `name` | yes | Source container name |
| `image` | yes | Requested target image reference |
| `digest` | yes | Digest of the pushed OCI manifest, or OCI index when the target is an index; config digests and placeholders are not allowed |

The command exits:

- `0` only after all images are created, pushed, and reported,
- nonzero when validation, lookup, image creation, push, or result writing
  fails.

### Unpause Interface

The unpause interface restores source containers that may have remained paused
when a commit process exited unexpectedly.

#### Invocation

```text
image-committer unpause <pod-name> <namespace> <container-name> [...]
```

Example:

```text
image-committer unpause sandbox-0 default sandbox
```

#### Inputs

##### Arguments

| Position | Name | Required | Description |
|---|---|---:|---|
| 1 | `unpause` | yes | Selects the unpause operation |
| 2 | `pod-name` | yes | Source Kubernetes Pod name |
| 3 | `namespace` | yes | Source Kubernetes Pod namespace |
| 4...N | `container-name` | yes | Container names to inspect and unpause |

##### Environment

| Variable | Default | Required | Description |
|---|---|---:|---|
| `IMAGE_COMMITTER_API_VERSION` | `v1` | no | Image-committer executable contract version |
| `CONTAINERD_SOCKET` | `/run/containerd/containerd.sock` | no | Containerd socket address |
| `CONTAINERD_NAMESPACE` | `k8s.io` | no | Containerd namespace |
| `SOURCE_POD_UID` | empty | no | Pod UID used to avoid stale container matches |

`SOURCE_POD_UID` is optional for compatibility. The controller obtains it from
the source Pod for the commit Job and copies it from the commit Job template to
a later unpause Job. An unpause Job created for an older commit Job may omit it.

Unpause does not require target images, registry configuration, registry
credentials, or the FIFO mount. The Job runs on the source Pod's node and
provides the host containerd socket at the path in `CONTAINERD_SOCKET`.

#### Expected behavior

An implementation must inspect every requested container and apply these
idempotent rules:

| Current state | Required behavior |
|---|---|
| Paused | Resume the task and verify that it is running |
| Running | Successful no-op |
| Stopped or no task | Successful no-op with a diagnostic message |
| Missing or ambiguous | Record an error and continue with other containers |
| Unknown state | Record an error and continue with other containers |

A failure for one container must not prevent attempts for the remaining
containers.

#### Output

Unpause does not produce a snapshot termination payload. It reports diagnostics
to standard output or standard error and exits:

- `0` when every requested container is running, was resumed, or is an accepted
  stopped/no-task no-op,
- nonzero when any requested container cannot be resolved, inspected, or
  resumed.

### Implementation Interfaces

The executable contract above is the boundary between the controller and any
image-committer implementation. The following Go interfaces define provider
boundaries within the reference implementation. Alternative implementations
may use another language or internal design as long as they satisfy the
executable contract.

Provider implementations are registered and selected by configuration or build
composition. They are not loaded as Go shared objects.

#### Container runtime provider

```go
type ContainerSelector struct {
    PodName       string
    PodNamespace  string
    PodUID        string
    ContainerName string
}

type TaskState string

const (
    TaskStateUnknown TaskState = "unknown"
    TaskStateRunning TaskState = "running"
    TaskStatePaused  TaskState = "paused"
    TaskStateStopped TaskState = "stopped"
)

type ResolvedContainer struct {
    ID          string
    Name        string
    State       TaskState
    Snapshotter string
    SnapshotKey string
    SourceImage string
}

type PauseHandle struct {
    ContainerID string
    PausedByUs  bool
}

type ContainerRuntime interface {
    Resolve(ctx context.Context, selector ContainerSelector) (ResolvedContainer, error)
    Status(ctx context.Context, container ResolvedContainer) (TaskState, error)
    Pause(ctx context.Context, container ResolvedContainer) (PauseHandle, error)
    Resume(ctx context.Context, container ResolvedContainer) error
}
```

The initial provider uses containerd metadata and standard Kubernetes container
labels.

#### Container execution provider

```go
type ExecRequest struct {
    Args []string
}

type ExecResult struct {
    ExitCode uint32
    Output   []byte
}

type ContainerExecutor interface {
    Exec(ctx context.Context, container ResolvedContainer, request ExecRequest) (ExecResult, error)
}
```

The execution provider is optional. It allows an implementation to run
runtime-specific preparation or maintenance inside a source container without
making that behavior part of the commit contract. The commit Job keeps the
containerd FIFO mount so containerd task exec implementations can configure
process I/O.

#### Image provider

```go
type LocalImage struct {
    Reference string
    Target    ocispec.Descriptor
}

type ImageBuilder interface {
    Commit(ctx context.Context, container ResolvedContainer, target string) (LocalImage, error)
}

type ImagePusher interface {
    Push(ctx context.Context, image LocalImage) (ocispec.Descriptor, error)
}
```

The initial image builder creates an OCI image from the containerd writable
snapshot. The image pusher uploads local image content after paused tasks have
been resumed and returns the pushed manifest or index descriptor. Its descriptor
digest is written to the commit result.

#### Credential provider

```go
type RegistryCredential struct {
    Username     string
    Password     string
    AccessToken  string
    RefreshToken string
}

type CredentialProvider interface {
    Credential(ctx context.Context, registryHost string) (RegistryCredential, error)
}
```

A credential provider must return credentials only for the requested registry
host.

### Registry Credential Providers

Credential-provider selection is an implementation detail of the plugin. The
controller does not define provider names or pass a provider-selection
environment variable. An implementation may select credentials through its
build configuration, its own configuration, the registry host, or a cloud
SDK's default credential chain.

The controller only provides generic credential inputs:

- the existing Docker config mount when `--snapshot-push-secret` is configured,
- the configured commit Job ServiceAccount and Pod labels, plus any projected
  or admission-injected workload identity inputs.

For workload identity, the operator sets
`--image-committer-service-account` to the ServiceAccount carrying the desired
cloud identity annotation or association and `--image-committer-pod-labels`
when the provider webhook requires an opt-in label. A plugin can then implement flows
such as:

- AKS Workload Identity to ACR credentials,
- IRSA or EKS Pod Identity to an ECR authorization token,
- GKE Workload Identity to an Artifact Registry access token.

The plugin returns standard username/password, access-token, or refresh-token
credentials to its registry client. Credentials are not passed in command-line
arguments.

The unpause Job does not push images and does not receive the image-committer
workload identity ServiceAccount. ServiceAccount configuration remains a
cluster-level operator setting and cannot be selected by a sandbox API caller.

### API Changes

There are no changes to `BatchSandbox`, `SandboxSnapshot`, the lifecycle API,
SDKs, or `osb`. `make manifests generate` is not required.

This proposal adds these controller and Helm settings:

- `--image-committer-service-account` /
  `controller.snapshot.imageCommitterServiceAccount`
- `--image-committer-pod-labels` /
  `controller.snapshot.imageCommitterPodLabels`

It also adds `IMAGE_COMMITTER_API_VERSION` and `SOURCE_POD_UID` to the internal
commit Job environment. These are not CRD or lifecycle API fields.

The setting is additive and operator-controlled. Controller configuration,
Helm values, generated deployment manifests, and user documentation must be
updated together.

### Annotation / Label Contract Changes

None.

The containerd runtime provider reads these standard Kubernetes container
labels:

```text
io.kubernetes.pod.name
io.kubernetes.pod.namespace
io.kubernetes.pod.uid
io.kubernetes.container.name
```

## Upgrade Strategy

- Preserve the existing executable path, arguments, and termination result.
- Keep the Docker config credential provider during migration.
- Leave the new ServiceAccount setting unset to preserve current behavior.
- Create and annotate or associate the selected ServiceAccount in each sandbox
  namespace before enabling a workload identity provider.
- Deploy another implementation through the existing image-committer image
  configuration.
- Keep the previous image available for rollback until containerd and Kata e2e
  tests pass.
- No CRD conversion or object migration is required.

## Test Plan

### Executable contract tests

- Validate commit and unpause arguments, including registry references with
  ports.
- Verify commit result ordering and termination-message JSON.
- Verify exit status and best-effort resume on each failure stage.
- Verify unpause is idempotent and attempts every requested container.

### Provider tests

- Resolve running and stopped containers by exact Kubernetes labels.
- Match Pod UID and reject ambiguous containers.
- Execute an optional container command with bounded output and cleanup.
- Track pause ownership and resume only owned tasks.
- Build a pullable OCI image from a writable snapshot.
- Resolve anonymous, static, token, and compatibility credentials without
  leaking them across registry hosts.
- For each implemented cloud provider, resolve credentials from a mocked
  workload identity source and refresh short-lived credentials.

### Controller tests

- Preserve binary path, arguments, mounts, and environment.
- Pass `IMAGE_COMMITTER_API_VERSION=v1` to commit and unpause Jobs.
- Pass the source Pod UID to the commit Job and copy it to the unpause Job.
- Set the configured ServiceAccount only on commit Jobs.
- Preserve the default ServiceAccount behavior when the setting is empty.
- Persist digests from a successful commit result.
- Create an unpause Job after commit failure.

### Integration and e2e tests

- Run commit, unpause, push, pull, and restore against real containerd.
- Cover running and stopped containers.
- Cover anonymous, authenticated, and insecure registries.
- Verify optional container task execution with supported runc and Kata
  runtimes.
- Verify unpause after terminating a commit Job while containers are paused.
- Run the existing Kind pause/resume suite without `nerdctl` in the committer
  image.

## Implementation History

- [x] 2026-07-20: Drafted proposal
- [ ] 2026-07-20: Draft proposal PR opened
- [ ] 2026-07-20: Community feedback incorporated
- [ ] 2026-07-20: Proposal marked as implementable
- [ ] 2026-07-20: Implementation PR opened
