# Envoy Egress Proxy Test Plan

This plan validates the new Envoy-based transparent HTTP(S) proxy backend against the existing mitmproxy backend, with emphasis on feature parity, safety/fail-closed behavior, and performance.

## Goals

- Verify Envoy can replace or safely coexist with the existing transparent mitmproxy path.
- Confirm Credential Vault injection works through Envoy with the same user-visible semantics.
- Catch parity gaps before relying on performance numbers.
- Produce repeatable local, Kubernetes, and performance test evidence for the PR.

## Current Branch Context

- Branch: `feature/envoy-egress-mitm`
- PR: <https://github.com/bahe-msft/OpenSandbox/pull/2>
- Main affected areas:
  - `components/egress/pkg/envoyproxy/`
  - `components/egress/pkg/envoyextproc/`
  - `components/egress/pkg/envoysds/`
  - `components/egress/pkg/mitmcert/`
  - `components/egress/mitmproxy_transparent.go`
  - `server/opensandbox_server/services/k8s/*`

## High-Risk Parity Items

Validate these first; they determine whether Envoy is replacement-ready.

1. **Child process supervision**
   - mitmproxy path has restart/backoff and readiness-gate integration.
   - Envoy path must either restart Envoy or make `/healthz` fail when Envoy dies.

2. **Response-header redaction**
   - mitmproxy system addon redacts credential values from response headers.
   - Envoy `ext_proc` currently skips response headers; validate whether this is a blocker or accepted gap.

3. **`ignore_hosts` / SNI pass-through**
   - mitmproxy supports SNI-aware pass-through through `system.py`.
   - Envoy parity needs explicit validation or documentation as unsupported.

4. **Runtime wiring**
   - Kubernetes path passes `OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND=envoy`.
   - Verify Docker runtime support if Envoy is expected to work outside Kubernetes.

5. **Transparent redirect scope**
   - Confirm broad `80/443` redirect is policy-gated safely and fails closed.

6. **HTTP/2, upstream SNI, and TLS validation**
   - Validate HTTP/2/gRPC/SSE behavior, upstream SNI, and upstream certificate validation.

---

## 1. Fast Local Correctness Tests

Run these before any Docker/Kubernetes test.

```bash
cd components/egress
go test ./...
python3 -m unittest tests/test_mitmscripts_system.py
```

Server/Kubernetes env wiring tests:

```bash
cd ../../server
uv sync --all-groups

uv run pytest \
  tests/k8s/test_egress_helper.py \
  tests/k8s/test_batchsandbox_provider.py \
  tests/k8s/test_agent_sandbox_provider.py \
  tests/k8s/test_kubernetes_service.py \
  -k "egress or mitm or credential or envoy"
```

Broader affected server pass:

```bash
cd server
uv run pytest tests/k8s tests/test_docker_service.py -k "egress or mitm or credential"
```

Expected result: all focused unit tests pass before black-box validation.

---

## 2. Build the Local Egress Image

From repository root:

```bash
docker build -t opensandbox/egress:envoy-local -f components/egress/Dockerfile .
```

This matters because Envoy is only present in the built runtime image.

---

## 3. Local Black-Box Envoy Smoke Test

This validates the real Linux iptables + Envoy + SDS + ext_proc path.

### Start Envoy backend

```bash
docker rm -f egress-envoy-test >/dev/null 2>&1 || true

docker run -d --name egress-envoy-test \
  --cap-add=NET_ADMIN \
  --sysctl net.ipv6.conf.all.disable_ipv6=1 \
  --sysctl net.ipv6.conf.default.disable_ipv6=1 \
  -e OPENSANDBOX_EGRESS_MODE=dns+nft \
  -e OPENSANDBOX_EGRESS_MITMPROXY_TRANSPARENT=true \
  -e OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND=envoy \
  -e OPENSANDBOX_EGRESS_ENVOY_MITM_HOSTS=example.com \
  -e OPENSANDBOX_EGRESS_TOKEN=test-token \
  -p 18080:18080 \
  opensandbox/egress:envoy-local
```

### Wait for readiness

```bash
for i in $(seq 1 60); do
  if curl -sf -H 'OPENSANDBOX-EGRESS-AUTH: test-token' \
    http://127.0.0.1:18080/healthz; then
    break
  fi
  sleep 1
done
```

### Apply a default-deny allowlist policy

```bash
curl -sf -XPOST http://127.0.0.1:18080/policy \
  -H 'OPENSANDBOX-EGRESS-AUTH: test-token' \
  -d '{"defaultAction":"deny","egress":[{"action":"allow","target":"example.com"}]}'
```

### Verify allowed HTTPS

```bash
docker exec egress-envoy-test curl -sfI https://example.com
```

### Verify denied HTTPS fails

```bash
docker exec egress-envoy-test sh -c '! curl -sfI --max-time 5 https://github.com'
```

### Inspect logs and process state

```bash
docker logs --tail=200 egress-envoy-test
docker exec egress-envoy-test ps aux
docker exec egress-envoy-test iptables -t nat -S
docker exec egress-envoy-test nft list ruleset || true
```

### Cleanup

```bash
docker rm -f egress-envoy-test
```

---

## 4. Mitmproxy Baseline Smoke Test

Run the same black-box smoke against the existing backend.

Change the run command to either omit `OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND` or set:

```bash
-e OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND=mitmproxy
```

Keep everything else the same. Results should match unless a behavior difference is explicitly documented.

---

## 5. Credential Vault Smoke Test

Credential Vault requires transparent MITM and an egress API auth token.

Use an echo endpoint such as `httpbin.org` for manual smoke. For CI, replace this with a local HTTPS echo server.

### Allow echo host

```bash
curl -sf -XPOST http://127.0.0.1:18080/policy \
  -H 'OPENSANDBOX-EGRESS-AUTH: test-token' \
  -d '{"defaultAction":"deny","egress":[{"action":"allow","target":"httpbin.org"}]}'
```

### Create vault

```bash
curl -sf -XPOST http://127.0.0.1:18080/credential-vault \
  -H 'OPENSANDBOX-EGRESS-AUTH: test-token' \
  -H 'content-type: application/json' \
  -d '{
    "credentials": [
      {"name": "fake-token", "source": {"type": "inline", "value": "not-a-real-secret"}}
    ],
    "bindings": [
      {
        "name": "httpbin-test",
        "match": {
          "hosts": ["httpbin.org"],
          "methods": ["GET"],
          "paths": ["/headers"]
        },
        "auth": {
          "type": "apiKey",
          "name": "X-OpenSandbox-Test",
          "credential": "fake-token"
        }
      }
    ]
  }'
```

### Verify injection

```bash
docker exec egress-envoy-test curl -s https://httpbin.org/headers
```

Expected:

- Request contains `X-OpenSandbox-Test: not-a-real-secret` upstream.
- Egress logs include injected header names, not secret values.
- Sanitized Credential Vault API does not expose secret values.

Also verify these auth types:

- `bearer`
- `basic`
- `apiKey`
- `customHeaders`

And these matching cases:

- exact host
- wildcard host
- exact-over-wildcard precedence
- path wildcard
- method filter
- scheme filter
- port filter
- existing header overwrite without duplication

---

## 6. Functional Parity Matrix

Track results as a table while testing.

| Area | mitmproxy | Envoy | Result | Notes |
|---|---:|---:|---|---|
| Transparent HTTP allow | TBD | TBD | TBD | |
| Transparent HTTPS allow | TBD | TBD | TBD | |
| Default-deny blocked host | TBD | TBD | TBD | |
| `dns+nft` resolved-IP allow | TBD | TBD | TBD | |
| Policy POST/PATCH/DELETE | TBD | TBD | TBD | |
| Credential bearer injection | TBD | TBD | TBD | |
| Credential basic injection | TBD | TBD | TBD | |
| Credential API-key injection | TBD | TBD | TBD | |
| Credential custom headers | TBD | TBD | TBD | |
| Secret-free logs | TBD | TBD | TBD | |
| Response header redaction | pass | TBD | TBD | Known risk |
| SSE/chunked streaming | TBD | TBD | TBD | |
| Large download | TBD | TBD | TBD | |
| HTTP/2 | TBD | TBD | TBD | |
| Upstream SNI | TBD | TBD | TBD | |
| CA export path compatibility | TBD | TBD | TBD | `/opt/opensandbox/mitmproxy-ca-cert.pem` |
| Sandbox CA trust before entrypoint | TBD | TBD | TBD | K8s/server path |
| `ignore_hosts` pass-through | pass | TBD | TBD | Known risk |
| Child process crash behavior | pass | TBD | TBD | Known risk |
| Graceful shutdown cleanup | TBD | TBD | TBD | iptables/nft cleanup |

---

## 7. Kubernetes / Kind Validation

Run server-side K8s tests first:

```bash
cd server
uv run pytest tests/k8s -k "egress or envoy or credential"
```

Run Kubernetes operator tests if relevant:

```bash
cd kubernetes
make test
make test-e2e-main
```

For manual Kind validation, configure server with Envoy backend:

```toml
[egress]
image = "opensandbox/egress:envoy-local"
mode = "dns+nft"
http_proxy_backend = "envoy"
envoy_mitm_hosts = ["example.com"]
```

Create a sandbox with:

- `networkPolicy`
- `credentialProxy.enabled=true`

Verify pod spec:

- egress sidecar has `OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND=envoy`
- egress sidecar has `OPENSANDBOX_EGRESS_MITMPROXY_TRANSPARENT=true`
- optional `OPENSANDBOX_EGRESS_ENVOY_MITM_HOSTS` is passed
- egress sidecar has `NET_ADMIN`
- sandbox container drops `NET_ADMIN`
- readiness probe includes egress auth header when token is set
- sandbox entrypoint waits for/trusts MITM CA

Verify from inside sandbox:

```bash
curl -I https://example.com        # should succeed when allowed
curl -I https://github.com         # should fail when not allowed
```

Then verify Credential Vault injection through the lifecycle/egress APIs.

---

## 8. Failure and Recovery Tests

Run these for both backends.

### Envoy process killed

```bash
docker exec egress-envoy-test pkill envoy
sleep 2
curl -i -H 'OPENSANDBOX-EGRESS-AUTH: test-token' http://127.0.0.1:18080/healthz
```

Expected outcome must be one of:

- Envoy restarts and health returns 200, or
- health becomes 503 and traffic fails closed.

If health stays 200 while Envoy is dead, this is a blocker.

### SDS unavailable at startup

Use an occupied SDS port or invalid SDS address and verify startup fails closed.

### ext_proc unavailable

Kill or block the ext_proc listener and verify traffic does not leak credentials or bypass policy.

### Shutdown cleanup

```bash
docker stop egress-envoy-test
docker logs --tail=100 egress-envoy-test
```

Expected:

- iptables transparent redirect removed
- DNS redirect removed
- nft enforcement removed or cleaned up as designed
- Envoy receives graceful shutdown

---

## 9. Performance Validation

Existing benchmark:

```bash
cd components/egress
./tests/bench-mitm-overhead.sh
```

Current comparison is:

```text
dns+nft
dns+nft + mitmproxy
```

Extend it to include:

```text
dns+nft + envoy
```

Envoy phase should add:

```bash
-e OPENSANDBOX_EGRESS_MITMPROXY_TRANSPARENT=true
-e OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND=envoy
-e OPENSANDBOX_EGRESS_ENVOY_MITM_HOSTS=<benchmark-hosts>
```

### Recommended workloads

1. **Short HTTPS request storm**
   - HEAD/GET to many hosts
   - concurrency: 1, 8, 32, 128
   - cold SNI and warm SNI variants

2. **Large download**
   - 20 MiB and 100 MiB objects
   - parallel streams: 1, 4, 16

3. **SSE/chunked streaming**
   - small chunks every 100-250 ms
   - measure first-byte and inter-chunk delay

4. **Credential injection overhead**
   - no active vault
   - 1 binding
   - 100 bindings
   - 1000 bindings
   - exact match, wildcard match, and no binding

5. **Policy scale**
   - 10 rules
   - 100 rules
   - 1000 rules
   - 4096 rules

### Metrics to collect

- req/s
- avg / p50 / p95 / p99 / p99.9 latency
- error rate
- aggregate MiB/s for downloads
- first-byte latency
- SSE inter-chunk delay
- CPU percentage and CPU seconds/request
- RSS / peak memory
- Envoy admin `/stats`
- SDS request count / cert mint count
- egress logs
- Docker stats

### Methodology

- Prefer a stable Linux host/VM; avoid using macOS Docker Desktop as the source of truth.
- Warm up before measuring.
- Run each scenario at least 5 times.
- Use local deterministic upstreams for primary numbers.
- Keep raw artifacts under `/tmp` or a named results directory.
- Record exact git commit, image tag, host type, CPU/memory limits, Docker version, and kernel.

### Suggested performance gates

If Envoy is intended as a mitmproxy replacement:

- unexpected error rate: `0`
- Envoy req/s: `>= mitmproxy`
- Envoy p99 latency: no worse than `mitmproxy + 10%`
- Envoy CPU seconds/request: `<= mitmproxy`
- Envoy RSS: `<= mitmproxy`, or documented justification
- SSE inter-chunk delay: no worse than `mitmproxy + 50 ms`
- startup readiness: no worse than `mitmproxy + 5 s`

---

## 10. Recommended Daily Test Loop

Use this during development:

```bash
# Fast egress correctness
(cd components/egress && go test ./...)
(cd components/egress && python3 -m unittest tests/test_mitmscripts_system.py)

# Server/K8s wiring
(cd server && uv run pytest tests/k8s -k "egress or envoy or credential")

# Build runtime image
docker build -t opensandbox/egress:envoy-local -f components/egress/Dockerfile .

# Then run local black-box Envoy smoke from this document.
```

Before marking the PR ready:

1. Complete the parity matrix.
2. Run Kind validation.
3. Run performance comparison on Linux.
4. Add results to the PR testing section.
5. Document any accepted Envoy gaps in `components/egress/docs/envoy-backend.md` or user-facing docs if behavior is visible.
