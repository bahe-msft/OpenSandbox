# Exec Management Authentication

```bash
bash examples/authn-exec/run-kind.sh
```

Configuration, protocol, and access boundaries: [Management authentication checker](../../docs/guides/authentication-exec.md).

`checker.py` is an independently deployable executable with no OpenSandbox imports. It reads a credential from stdin, validates it with Kubernetes TokenReview, and writes an allow/deny JSON result. Install `kubernetes` plus `tomli` on Python 3.10 when running it outside the server image; substitute an OIDC/IAM executable using the same stdin/stdout contract as needed.
