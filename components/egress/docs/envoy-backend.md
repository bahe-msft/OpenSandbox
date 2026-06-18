# Envoy HTTP Proxy Backend

This document tracks the experimental Envoy backend for the egress sidecar's
HTTP(S) interception path.

Current status:

- The existing DNS proxy, nftables enforcement, policy API, and Credential Vault
  store remain unchanged.
- `OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND=envoy` selects Envoy instead of
  mitmproxy for the transparent `80/443` interception listener.
- The Go egress process starts an Envoy `ext_proc` gRPC server. It evaluates the
  existing Credential Vault active snapshot and returns request-header mutations.
- The Docker image includes Envoy.

Known gaps before this can replace mitmproxy:

- Dynamic TLS MITM certificate generation is not implemented.
- SDS/xDS for per-SNI certificates is not implemented.
- CA export/install compatibility with the existing mitmproxy CA path is not
  implemented.
- Envoy original-destination routing needs validation under Kubernetes/Kata.
- Response-header redaction is not implemented in the Envoy path yet.
- `ignore_hosts`/SNI pass-through compatibility is not implemented.

The intended final design is:

```text
sandbox process
  -> iptables redirect tcp/80,443
  -> Envoy transparent listener
  -> Envoy request headers ext_proc
  -> Go egress Credential Vault store
  -> Envoy upstream forwarding
```

Non-HTTP traffic remains governed by DNS+nftables and is not sent through Envoy.
