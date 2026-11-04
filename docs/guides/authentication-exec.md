---
title: Management Authentication Checker
description: Validate management credentials with an independently packaged executable and a server-enforced timeout.
---

# Management Authentication Checker

An exec authentication checker replaces the static management-key check with a decision from an operator-configured executable. It runs in the trusted server environment, not inside a sandbox. The binary can implement OIDC verification, Kubernetes TokenReview, or enterprise IAM integration without an HTTP validation service or OpenSandbox imports.

## Choose exactly one authentication option

Static-key authentication is unchanged:

```toml
[server]
api_key = "existing-management-key"
```

Alternatively, remove `api_key` and configure the checker file:

```toml
[server]
authn_config = "/etc/osb/authn.toml"
```

`server.api_key` and `server.authn_config` are mutually exclusive, including an API key supplied through `OPENSANDBOX_SERVER_API_KEY`. Even an explicitly empty API-key value must be removed when selecting exec authentication. Exec authentication also cannot be combined with the existing key-based `[tenants]` mode. Both Docker and Kubernetes runtimes are supported. Configuring neither authentication option retains the existing explicit insecure-development startup acknowledgment.

## `authn.toml`

```toml
[exec]
command = ["/opt/osb/authn-check", "--config", "/etc/osb/authn.toml"]
timeout_seconds = 3

[provider]
# Settings interpreted by your checker, not by OSB.
```

OSB reads only `[exec]`; other sections belong to the independently packaged checker. The executable path must be absolute. Arguments come only from trusted operator configuration. OSB launches the argv directly without shell interpolation. The configuration and executable must not be writable by sandbox workloads or untrusted users. OSB loads `[exec]` at startup, so launcher changes require a server restart. The checker controls how it reloads its own settings.

`timeout_seconds` is the total deadline, including waiting for an available execution slot. It defaults to 3 seconds and must be greater than zero and at most 30 seconds. OSB enforces this deadline outside the checker. A checker that hangs cannot disable it. Up to eight checks execute concurrently. Each decision launches a fresh process; results are not cached.

## Checker protocol

In exec mode, clients send `Authorization: Bearer <credential>`. OSB writes one JSON document to the checker's stdin and closes it:

```json
{"token": "<incoming bearer credential>"}
```

The checker must determine whether the credential permits **OSB management access**, not merely whether it authenticates a user somewhere else. For OIDC, validate issuer, audience, signature, expiry, and applicable IAM permissions in the checker.

On exit code `0`, stdout must contain a single JSON decision:

```json
{"allowed": true, "subject": "optional-verified-id"}
```

`allowed` must be a JSON boolean. `subject` is optional, at most 256 characters, and is available as `request.state.auth_subject`; it does not select a namespace or resource policy. Unknown or duplicate result fields are rejected. The result schema is https://github.com/opensandbox-group/OpenSandbox/blob/main/specs/authentication-exec.schema.json.

| Checker outcome | OSB behavior |
|-----------------|--------------|
| Exit `0`, `allowed: true` | Continue to the existing management handler |
| Exit `0`, `allowed: false` | `401` with a Bearer challenge |
| Nonzero exit, timeout, malformed result, or stdout larger than 4 KiB | `503`, fail closed |

Credentials are sent only on stdin, never through argv or a credential environment variable. Stderr is discarded; stdout and exception details are not copied into errors or logs. On POSIX, the checker has a dedicated process group that OSB terminates on completion, cancellation, or timeout, including descendants. On Windows, OSB terminates the direct process; checkers must not daemonize or leave descendants running.

## Existing access paths

The existing public paths (`/health`, `/version`, `/docs`, `/redoc`, `/openapi.json`) remain public. Other management endpoints, including root and `/v1` aliases, require an allow decision. A checker failure never falls back to the static key or anonymous access.

Sandbox endpoint resolution, ingress, execd, and the deprecated server proxy keep their existing behavior. As in existing single-tenant mode, server-proxy paths are exempt from the management credential check and retain their sandbox endpoint authentication. Network isolation and sandbox endpoint credentials remain separate concerns.

Existing SDK custom headers can carry a bearer credential acquired outside OSB. Use existing request hooks or custom transports for renewal, restricted to the configured lifecycle origin and API path. Do not copy management credentials to direct sandbox endpoints or add them again after cross-origin redirects. SDK/CLI runtime behavior is unchanged by this option.

## Kind example

```bash
bash examples/authn-exec/run-kind.sh
```

The isolated `osb-authn-exec` cluster mounts a standalone Python checker into the server container. It uses TokenReview with an explicit OSB audience and an exact ServiceAccount allowlist; replace that executable with your OIDC/IAM checker without modifying OSB. The server ServiceAccount receives reviewer permission because the child process runs under the server's identity.

The smoke test checks successful management access, missing/invalid/wrong-audience credentials, a denied ServiceAccount, and fail-closed behavior when reviewer permission is removed. The client rereads a short-lived projected token file before each request. The example uses read-only runtime RBAC, does not create sandbox workloads, and never deploys to or changes your current Kubernetes context.
