# OpenSandbox Idle Controller

Standalone Go controller that watches `BatchSandbox` resources and pauses opted-in
sandboxes after execd reports they have been idle for a configured threshold.

The controller intentionally remains outside the lifecycle server and the
OpenSandbox Kubernetes operator. It combines:

- a controller-runtime watch for candidate and lifecycle state changes;
- lifecycle API endpoint resolution and pause requests;
- execd `GET /v1/activity` snapshots;
- timed reconcile requeues for idle thresholds and the final grace check.

It does **not** patch `BatchSandbox.spec.pause` directly.

## Flow

1. Watch `BatchSandbox` resources with the opt-in label (default:
   `opensandbox.ai/auto-pause=true`).
2. Select resources in phase `Succeed` with at least one ready replica and no
   pause intent.
3. Resolve the sandbox's execd endpoint on port `44772` using lifecycle server
   proxy mode.
4. Read `GET /v1/activity`.
5. Requeue at the next idle-policy boundary. Reconcile does not sleep.
6. Once idle, retain the first revision in memory and requeue for the grace
   period.
7. Read activity again and require the same revision, `busy=false`, zero active
   operations, and no active keep-awake deadline.
8. Call lifecycle `POST /sandboxes/{id}/pause` unless dry-run is enabled.
9. Observe `Pausing`/`Paused` through subsequent `BatchSandbox` watch events.

A controller restart discards pending first observations and restarts the grace
check, which is safe and only delays a pause.

## Configuration

Environment variables and matching flags:

| Environment | Flag | Default | Description |
| --- | --- | --- | --- |
| `OPENSANDBOX_BASE_URL` | `--opensandbox-url` | required | Lifecycle API base URL. |
| `OPENSANDBOX_API_KEY` | `--api-key` | empty | Lifecycle API key. |
| `PAUSE_AFTER` | `--pause-after` | `1h` | Idle duration before eligibility. |
| `GRACE_PERIOD` | `--grace-period` | `30s` | Delay between matching activity observations. |
| `CHECK_INTERVAL` | `--check-interval` | `5m` | Maximum interval between checks. |
| `REQUEST_TIMEOUT` | `--request-timeout` | `30s` | Lifecycle and execd HTTP timeout. |
| `DRY_RUN` | `--dry-run` | `true` | Log eligible sandboxes without pausing. |
| `OPT_IN_LABEL` | `--opt-in-label` | `opensandbox.ai/auto-pause` | Required BatchSandbox label. |
| `OPT_IN_VALUE` | `--opt-in-value` | `true` | Required label value. |
| `LEADER_ELECTION` | `--leader-elect` | `true` | Enable leader election. |
| `LEADER_ELECTION_ID` | `--leader-election-id` | `opensandbox-idle-controller` | Lease name. |
| `METRICS_ADDRESS` | `--metrics-bind-address` | `:8080` | Metrics listener. |
| `PROBE_ADDRESS` | `--health-probe-bind-address` | `:8081` | Health probe listener. |

## Build and test

```bash
cd components/idle-controller
go test -race ./...
go build ./cmd/idle-controller
```

Build the container from the repository root:

```bash
docker build -f components/idle-controller/Dockerfile -t idle-controller:dev .
```

## Deploy

Review the image and policy settings, then apply the example manifests:

```bash
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
```

The example deployment starts in dry-run mode.

## RBAC

The controller requires read-only access to `BatchSandbox` resources and access
to a leader-election Lease when leader election is enabled. It does not require
permission to patch BatchSandbox because pause requests go through the lifecycle
API.

## Safety notes

The controller does not yet have an atomic idle claim. Two observations and an
unchanged revision reduce—but do not eliminate—activity that starts after the
final check and before the pause request. Start with dry-run, opt-in labels, and
a conservative threshold.
