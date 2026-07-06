# Handoff: egress auth token and Credential Vault with Kubernetes Pool/poolRef

## Summary

OpenSandbox Kubernetes `Pool` / `poolRef` mode can prewarm pods and later allocate
one to a `BatchSandbox`. This works for basic exec and can work for egress policy
updates when the Pool template includes an `egress` sidecar.

Credential Vault does not currently work correctly with prewarmed Pool pods. The
root cause is that Credential Vault requires the egress sidecar to have an egress
API auth token configured at runtime, but in Pool mode the sidecar is already
running before OpenSandbox generates the per-sandbox token for the allocated
`BatchSandbox`.

## Relevant files

Server token creation and endpoint header propagation:

- `server/opensandbox_server/services/constants.py`
  - `SANDBOX_EGRESS_AUTH_TOKEN_METADATA_KEY = "opensandbox.io/egress-auth-token"`
  - `OPEN_SANDBOX_EGRESS_AUTH_HEADER = "OPENSANDBOX-EGRESS-AUTH"`
- `server/opensandbox_server/services/k8s/create_helpers.py`
  - creates an egress token when `request.network_policy` is present
  - stores it as `metadata.annotations["opensandbox.io/egress-auth-token"]`
- `server/opensandbox_server/services/k8s/endpoint_resolver.py`
  - reads `opensandbox.io/egress-auth-token`
  - adds `OPENSANDBOX-EGRESS-AUTH: <token>` to endpoint headers for port `18080`
- `server/opensandbox_server/services/k8s/batchsandbox_provider.py`
  - non-pool create builds a pod template and calls `apply_egress_to_spec(...)`
  - pool create only creates a `BatchSandbox` with `spec.poolRef`
- `server/opensandbox_server/services/k8s/egress_helper.py`
  - when `egress_auth_token` is available, injects `OPENSANDBOX_EGRESS_TOKEN`
    into the egress sidecar env and readiness probe headers

Egress sidecar auth and Credential Vault readiness:

- `components/egress/policy_server.go`
  - `policyServer.authorize` compares request header `OPENSANDBOX-EGRESS-AUTH`
    against the server-side token captured at sidecar startup
  - if the token is empty, policy API auth is effectively disabled
  - `credentialvault.NewStore(..., func() bool { return strings.TrimSpace(token) != "" })`
    makes Credential Vault require a non-empty startup token
- `components/egress/pkg/credentialvault/vault.go`
  - `Store.Ready` fails with `credential vault requires egress API auth token`
    when the egress sidecar has no configured token

## Normal image-backed create flow

For a non-pool Kubernetes sandbox create with `networkPolicy`:

1. OpenSandbox server generates a random egress auth token.
2. It writes the token to the workload annotation:

   ```yaml
   metadata:
     annotations:
       opensandbox.io/egress-auth-token: <token>
   ```

3. It dynamically builds the sandbox pod template and injects the same token into
   the egress sidecar:

   ```yaml
   containers:
     - name: egress
       env:
         - name: OPENSANDBOX_EGRESS_TOKEN
           value: <token>
   ```

4. When a client asks for endpoint `18080`, endpoint resolver returns:

   ```http
   OPENSANDBOX-EGRESS-AUTH: <token>
   ```

5. The SDK/provider can call the egress control API:

   - `PATCH /policy`
   - `POST /credential-vault`

   The request header token matches `OPENSANDBOX_EGRESS_TOKEN`, so auth succeeds.

6. Credential Vault readiness passes because the egress sidecar has a non-empty
   configured token and is in `dns+nft` mode with transparent MITM enabled.

## Pool/poolRef create flow today

For `poolRef` mode:

1. A Pool controller creates prewarmed pods from `Pool.spec.template`.
2. The egress sidecar, if present in the Pool template, starts before any sandbox
   is allocated.
3. Later, OpenSandbox creates a `BatchSandbox` with:

   ```yaml
   spec:
     poolRef: <pool-name>
   ```

4. OpenSandbox still creates `metadata.annotations["opensandbox.io/egress-auth-token"]`
   when `networkPolicy` is present.
5. Endpoint resolver still returns `OPENSANDBOX-EGRESS-AUTH: <token>` for port
   `18080`.
6. However, the already-running egress sidecar in the selected Pool pod does not
   receive the newly generated token as `OPENSANDBOX_EGRESS_TOKEN`.

Result:

- Runtime egress policy updates can work if the sidecar has no startup token,
  because `policyServer.authorize` allows requests when `s.token == ""`.
- Credential Vault fails because `Store.Ready` requires the egress sidecar to
  have a configured auth token.

Observed failure:

```text
Precondition Failed: credential vault requires egress API auth token
```

## Live validation notes

In AKS test, a pool-backed sandbox showed:

```yaml
BatchSandbox metadata.annotations:
  opensandbox.io/egress-auth-token: <generated-token>
  sandbox.opensandbox.io/alloc-status: '{"pods":["aks-kata-sm-..."]}'

allocated Pool pod containers:
  - sandbox
  - egress

egress env:
  - OPENSANDBOX_EGRESS_RULES=...
  - OPENSANDBOX_EGRESS_MODE=dns+nft
  # no OPENSANDBOX_EGRESS_TOKEN
```

A non-pool/image-backed sandbox showed:

```yaml
BatchSandbox metadata.annotations:
  opensandbox.io/egress-auth-token: <token>

egress env:
  - OPENSANDBOX_EGRESS_RULES=...
  - OPENSANDBOX_EGRESS_MODE=dns+nft
  - OPENSANDBOX_EGRESS_MITMPROXY_TRANSPARENT=true
  - OPENSANDBOX_EGRESS_TOKEN=<same-token>
```

This is the key behavioral difference.

## What currently works

### Pool + execd

Works if the Pool template includes `execd` install/bootstrap setup and the
sandbox container is started in the expected OpenSandbox shape.

### Pool + egress policy

Works if the Pool template includes the egress sidecar and the provider/SDK
updates policy after acquisition.

Caveat: with no `OPENSANDBOX_EGRESS_TOKEN`, the egress policy API is not
protected by egress-sidecar token auth. This is weaker than non-pool mode and
should be reviewed before production use.

### Pool + Credential Vault

Does not work correctly today with standard Pool mode because the prewarmed
sidecar does not receive the per-sandbox egress token.

## Fix options

### Option A: Dynamic egress token file

Add egress-sidecar support for a token file, for example:

```text
OPENSANDBOX_EGRESS_TOKEN_FILE=/var/run/opensandbox/egress-token
```

Implementation details:

1. Replace `policyServer.token string` with a token source abstraction.
2. `authorize` reads the current token from the source instead of only using the
   startup string.
3. `credentialvault.NewStore(... requireToken ...)` uses the same token source so
   Credential Vault readiness checks the dynamic token.
4. Pool allocation path writes the generated per-sandbox token into a file
   mounted only into the egress container.

Challenge: Kubernetes cannot directly write an arbitrary file into an already
running pod. This option needs an additional mechanism, such as a sidecar/agent,
projected volume, or pod annotation + Downward API file.

### Option B: Pod annotation + Downward API token file

Use Kubernetes Downward API to expose a pod annotation as a file mounted only
into the egress container:

```yaml
volumes:
  - name: egress-token
    downwardAPI:
      items:
        - path: token
          fieldRef:
            fieldPath: metadata.annotations['opensandbox.io/egress-auth-token']
```

Then Pool allocation would patch the selected pod annotation:

```yaml
metadata:
  annotations:
    opensandbox.io/egress-auth-token: <token>
```

The egress sidecar reads/reloads `/var/run/.../token`.

Pros:

- Keeps per-sandbox generated token model.
- Avoids static shared pool tokens.
- Can keep the token out of the sandbox container by mounting the volume only in
  the egress container.

Questions/risks:

- Need to validate Downward API file update latency/reliability after pod
  annotation patches.
- Need controller changes to patch the allocated pod annotation when assigning it
  to a `BatchSandbox`.
- Need egress changes to read/reload the token file.

### Option C: Per-pod token generated at Pool pod creation

Generate a unique token for each Pool pod when the pod is created. Start egress
with that token. When the pod is allocated, copy the selected pod's token into
`BatchSandbox.metadata.annotations["opensandbox.io/egress-auth-token"]` so
endpoint resolver returns the token the sidecar already knows.

Pros:

- Egress sidecar can keep startup token env model.
- Avoids reload-after-allocation.
- Still per-pod, not shared globally.

Cons:

- Pool controller must generate/store/manage per-pod tokens.
- Token must be stored somewhere retrievable by the controller at allocation time.
- Careful access controls needed so the sandbox container cannot read the token.

### Option D: Static per-pool token

Put a static token in the Pool template:

```yaml
- name: OPENSANDBOX_EGRESS_TOKEN
  valueFrom: ...
```

Then make endpoint resolution/provider use that token for every allocation from
the Pool.

Pros:

- Simple operationally.

Cons:

- Weakest isolation.
- Shared token across many sandboxes/pods.
- Requires a way for clients to discover the pool token.
- Does not match current per-sandbox token model.
- Not recommended for production.

### Option E: Recreate/restart egress sidecar on allocation

On allocation, restart or recreate the egress sidecar with the generated
per-sandbox token.

Pros:

- Preserves per-sandbox token model.

Cons:

- Kubernetes cannot restart just one regular container with a new env var without
  pod restart/recreation.
- Reduces warm-pool benefit.
- Operationally complex.

## Recommended direction

Recommended upstream design: **Option B** or **Option C**.

- Option B is attractive if Downward API annotation updates are reliable enough
  and egress can reload a token file.
- Option C is attractive if the Pool controller can safely own per-pod token
  generation and copy the selected pod token to the `BatchSandbox` annotation.

Avoid Option D for production because it creates shared egress API credentials.

## Option C validation plan on current Kubernetes cluster

Use this section to validate a per-pod-token implementation end-to-end on the
current AKS cluster. The flow is intentionally split into baseline reproduction,
controller build/deploy, and post-fix validation.

### 0. Common variables

Set these once per shell. Adjust names to match the target ACR and Helm release.

```bash
export OSB_NS=opensandbox-system
export TEST_NS=default
export RELEASE=opensandbox-controller
export ACR_NAME=<acr-name>
export ACR_LOGIN_SERVER=$(az acr show -n "$ACR_NAME" --query loginServer -o tsv)
export TAG=egress-token-option-c-$(git rev-parse --short HEAD)-$(date +%Y%m%d%H%M%S)
export CONTROLLER_IMAGE="$ACR_LOGIN_SERVER/opensandbox/controller:$TAG"
```

Confirm the cluster context before mutating it:

```bash
kubectl config current-context
kubectl get nodes
```

### 1. Install or verify OpenSandbox controller/CRDs

If the CRDs are absent:

```bash
kubectl get crd | grep -E 'batchsandboxes|pools' || true

cd kubernetes
helm upgrade --install "$RELEASE" ./charts/opensandbox-controller \
  -n "$OSB_NS" \
  --create-namespace
```

Verify:

```bash
kubectl get crd | grep -E 'batchsandboxes|pools'
kubectl get pods -n "$OSB_NS"
```

### 2. Baseline reproduction before the fix

Create a Pool with an egress sidecar but no `OPENSANDBOX_EGRESS_TOKEN`:

```bash
cat >/tmp/egress-token-repro-pool.yaml <<'YAML'
apiVersion: sandbox.opensandbox.io/v1alpha1
kind: Pool
metadata:
  name: egress-token-repro
spec:
  template:
    metadata:
      labels:
        app: egress-token-repro
    spec:
      containers:
        - name: sandbox
          image: registry.k8s.io/pause:3.9
        - name: egress
          image: sandbox-registry.cn-zhangjiakou.cr.aliyuncs.com/opensandbox/egress:v1.1.3
          env:
            - name: OPENSANDBOX_EGRESS_RULES
              value: '{"defaultAction":"deny","egress":[]}'
            - name: OPENSANDBOX_EGRESS_MODE
              value: dns+nft
            - name: OPENSANDBOX_EGRESS_MITMPROXY_TRANSPARENT
              value: "true"
          securityContext:
            capabilities:
              add: ["NET_ADMIN"]
          ports:
            - name: egress-api
              containerPort: 18080
      tolerations:
        - operator: Exists
  capacitySpec:
    bufferMax: 1
    bufferMin: 1
    poolMax: 1
    poolMin: 0
YAML

kubectl apply -n "$TEST_NS" -f /tmp/egress-token-repro-pool.yaml
kubectl wait -n "$TEST_NS" --for=condition=Ready pod \
  -l sandbox.opensandbox.io/pool-name=egress-token-repro \
  --timeout=180s
```

Confirm the prewarmed egress sidecar has no token:

```bash
POD=$(kubectl get pods -n "$TEST_NS" \
  -l sandbox.opensandbox.io/pool-name=egress-token-repro \
  -o jsonpath='{.items[0].metadata.name}')

kubectl get pod "$POD" -n "$TEST_NS" \
  -o jsonpath='{range .spec.containers[?(@.name=="egress")].env[*]}{.name}={.value}{"\n"}{end}' \
  | grep OPENSANDBOX_EGRESS_TOKEN || echo "missing OPENSANDBOX_EGRESS_TOKEN"
```

Create a pool-backed `BatchSandbox` with the annotation that the lifecycle server
would normally generate:

```bash
cat >/tmp/egress-token-repro-batchsandbox.yaml <<'YAML'
apiVersion: sandbox.opensandbox.io/v1alpha1
kind: BatchSandbox
metadata:
  name: egress-token-repro-sbx
  annotations:
    opensandbox.io/egress-auth-token: repro-generated-token
spec:
  replicas: 1
  poolRef: egress-token-repro
YAML

kubectl apply -n "$TEST_NS" -f /tmp/egress-token-repro-batchsandbox.yaml
```

Wait for allocation and verify the mismatch:

```bash
until kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.sandbox\.opensandbox\.io/alloc-status}' | grep -q pods; do
  sleep 2
done

ALLOC_POD=$(kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.sandbox\.opensandbox\.io/alloc-status}' | jq -r '.pods[0]')

kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.opensandbox\.io/egress-auth-token}{"\n"}'

kubectl get pod "$ALLOC_POD" -n "$TEST_NS" \
  -o jsonpath='{range .spec.containers[?(@.name=="egress")].env[*]}{.name}={.value}{"\n"}{end}' \
  | grep OPENSANDBOX_EGRESS_TOKEN || echo "missing OPENSANDBOX_EGRESS_TOKEN"
```

Reproduce the Credential Vault failure:

```bash
kubectl port-forward -n "$TEST_NS" pod/"$ALLOC_POD" 18080:18080
```

In another shell:

```bash
curl -i \
  -X POST http://127.0.0.1:18080/credential-vault \
  -H 'Content-Type: application/json' \
  -H 'OPENSANDBOX-EGRESS-AUTH: repro-generated-token' \
  --data '{}'
```

Expected baseline result:

```text
HTTP/1.1 412 Precondition Failed
credential vault requires egress API auth token
```

Clean up the baseline resources before deploying the fix:

```bash
kubectl delete batchsandbox egress-token-repro-sbx -n "$TEST_NS" --ignore-not-found
kubectl delete pool egress-token-repro -n "$TEST_NS" --ignore-not-found
```

### 3. Build and push the patched controller image to ACR

After implementing Option C in the controller:

```bash
az acr login -n "$ACR_NAME"

docker buildx build \
  --platform linux/amd64 \
  -f kubernetes/Dockerfile \
  -t "$CONTROLLER_IMAGE" \
  --push \
  kubernetes
```

If the AKS node architecture is not amd64, change `--platform` accordingly.

### 4. Update the controller deployment with kubectl

For this cluster validation, patch the live Deployment directly. This is enough
for an experiment and avoids changing Helm values. Be aware that a later Helm
upgrade can revert the image unless the same image is also passed to Helm.

```bash
kubectl set image deployment/opensandbox-controller-manager \
  -n "$OSB_NS" \
  manager="$CONTROLLER_IMAGE"
```

If the Deployment name differs, discover it first:

```bash
kubectl get deploy -n "$OSB_NS"
kubectl get deploy -n "$OSB_NS" -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .spec.template.spec.containers[*]}{.name}{"="}{.image}{" "}{end}{"\n"}{end}'
```

Verify rollout:

```bash
kubectl rollout status deployment/opensandbox-controller-manager -n "$OSB_NS" --timeout=180s
kubectl get pods -n "$OSB_NS" -o wide
kubectl logs -n "$OSB_NS" deployment/opensandbox-controller-manager -c manager --tail=100 || true
```

### 5. Post-fix validation

Recreate the same Pool manifest from the baseline step. With Option C, the
controller should inject a non-empty token into the egress sidecar at Pool pod
creation time.

```bash
kubectl apply -n "$TEST_NS" -f /tmp/egress-token-repro-pool.yaml
kubectl wait -n "$TEST_NS" --for=condition=Ready pod \
  -l sandbox.opensandbox.io/pool-name=egress-token-repro \
  --timeout=180s

POD=$(kubectl get pods -n "$TEST_NS" \
  -l sandbox.opensandbox.io/pool-name=egress-token-repro \
  -o jsonpath='{.items[0].metadata.name}')

POD_TOKEN=$(kubectl get pod "$POD" -n "$TEST_NS" \
  -o jsonpath='{range .spec.containers[?(@.name=="egress")].env[?(@.name=="OPENSANDBOX_EGRESS_TOKEN")]}{.value}{end}')

test -n "$POD_TOKEN" && echo "pool pod token present"
```

Create a pool-backed `BatchSandbox`. For controller-only validation, omit the
server-generated token annotation so the allocated pod token is clearly the
source of truth:

```bash
cat >/tmp/egress-token-repro-batchsandbox-fixed.yaml <<'YAML'
apiVersion: sandbox.opensandbox.io/v1alpha1
kind: BatchSandbox
metadata:
  name: egress-token-repro-sbx
spec:
  replicas: 1
  poolRef: egress-token-repro
YAML

kubectl apply -n "$TEST_NS" -f /tmp/egress-token-repro-batchsandbox-fixed.yaml

until kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.sandbox\.opensandbox\.io/alloc-status}' | grep -q pods; do
  sleep 2
done

ALLOC_POD=$(kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.sandbox\.opensandbox\.io/alloc-status}' | jq -r '.pods[0]')

SANDBOX_TOKEN=$(kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.opensandbox\.io/egress-auth-token}')

test "$SANDBOX_TOKEN" = "$POD_TOKEN" && echo "BatchSandbox token matches allocated pod token"
```

Validate egress API auth behavior:

```bash
kubectl port-forward -n "$TEST_NS" pod/"$ALLOC_POD" 18080:18080
```

In another shell:

```bash
# Missing/wrong token should fail.
curl -i -X PATCH http://127.0.0.1:18080/policy \
  -H 'Content-Type: application/json' \
  --data '{"defaultAction":"deny","egress":[]}'

curl -i -X PATCH http://127.0.0.1:18080/policy \
  -H 'Content-Type: application/json' \
  -H 'OPENSANDBOX-EGRESS-AUTH: wrong-token' \
  --data '{"defaultAction":"deny","egress":[]}'

# Correct token should pass auth. The exact policy response may depend on
# policy validation, but it must not be 401/403.
curl -i -X PATCH http://127.0.0.1:18080/policy \
  -H 'Content-Type: application/json' \
  -H "OPENSANDBOX-EGRESS-AUTH: $SANDBOX_TOKEN" \
  --data '{"defaultAction":"deny","egress":[]}'
```

Validate Credential Vault no longer fails for the token precondition:

```bash
curl -i \
  -X POST http://127.0.0.1:18080/credential-vault \
  -H 'Content-Type: application/json' \
  -H "OPENSANDBOX-EGRESS-AUTH: $SANDBOX_TOKEN" \
  --data '{}'
```

Expected post-fix result: the response is not `412 credential vault requires
egress API auth token`. Depending on MITM readiness and egress mode on the test
node, other preconditions may still need separate investigation.

### 6. Recycle validation

Option C is per-pod. If the same Pool pod is reused, the token is expected to be
stable for that pod and copied to each newly allocated `BatchSandbox`.

```bash
kubectl delete batchsandbox egress-token-repro-sbx -n "$TEST_NS" --ignore-not-found

kubectl apply -n "$TEST_NS" -f /tmp/egress-token-repro-batchsandbox-fixed.yaml

until kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.sandbox\.opensandbox\.io/alloc-status}' | grep -q pods; do
  sleep 2
done

RECYCLED_TOKEN=$(kubectl get batchsandbox egress-token-repro-sbx -n "$TEST_NS" \
  -o jsonpath='{.metadata.annotations.opensandbox\.io/egress-auth-token}')

test "$RECYCLED_TOKEN" = "$POD_TOKEN" && echo "recycled allocation received same per-pod token"
```

Cleanup:

```bash
kubectl delete batchsandbox egress-token-repro-sbx -n "$TEST_NS" --ignore-not-found
kubectl delete pool egress-token-repro -n "$TEST_NS" --ignore-not-found
```

## Token-file prototype notes

A stronger warm-pool-friendly variant was prototyped after the initial
per-pod-env-token implementation:

- egress supports `OPENSANDBOX_EGRESS_TOKEN_FILE` and reads the token file at
  request time;
- Pool pod creation creates a per-pod Secret containing `token`;
- the Secret is mounted only into the `egress` container at
  `/var/run/opensandbox/egress-token/token`;
- the egress container receives `OPENSANDBOX_EGRESS_TOKEN_FILE` instead of a
  literal token env var;
- allocation sync reads the Secret token and writes it to
  `BatchSandbox.metadata.annotations["opensandbox.io/egress-auth-token"]`;
- recycle success rotates the Secret for reusable pods, so `Noop`/`Restart`
  strategies can reuse the same pod with a new token.

Real-cluster validation used the existing egress image in AKS, then copied a
locally built patched egress binary into the running egress container and let the
supervisor restart it. With a Pool using `recycleStrategy.type: Noop`:

1. The Pool pod had `OPENSANDBOX_EGRESS_TOKEN_FILE` and a mounted Secret.
2. First `BatchSandbox` allocation copied the Secret token to the BatchSandbox
   annotation.
3. Wrong token returned `401 Unauthorized`.
4. Correct token returned `201 Created` for `POST /credential-vault`.
5. Deleting the first BatchSandbox returned the same pod to the Pool.
6. The controller rotated the Secret token.
7. A second BatchSandbox reused the same pod and received the new token in its
   annotation.
8. After the Secret volume propagated in the pod, the old token returned `401`
   and the new token passed egress auth.

Important caveat: Kubernetes Secret volume propagation is eventual. In the AKS
validation, the old mounted token remained active for a short window after the
Secret update while the new API annotation already contained the new token. The
observed sequence was old token still accepted/new token rejected, then after
several polling attempts old token rejected/new token accepted.

Follow-up gate: the controller now waits after Secret rotation before releasing a
reusable pod back to the allocator. It polls the pod egress API and requires both
conditions before the pod can be reused:

- old token returns `401 Unauthorized`;
- new token is accepted by `GET /policy`.

If the wait times out, the controller removes that pod from the successful
release set and adds it to `toDeletePods`. The pod is deleted and replaced rather
than returned to the warm pool with uncertain token state. This preserves the
secure fallback while allowing warm reuse when Secret propagation is confirmed.

AKS validation caveat: the local controller process used for validation could not
reach AKS Pod IPs directly (`curl http://<podIP>:18080/healthz` timed out), so the
success path of the gate could not be validated from outside the cluster. The
fallback path was validated: with `recycleStrategy.type: Noop`, release reconcile
waited for the 60s propagation timeout, then deleted the old pod and created a
replacement instead of reusing it. A controller running inside the cluster should
be able to exercise the success path because it can reach Pod IPs.

## Suggested upstream issue title

```text
Credential Vault does not work with Kubernetes Pool/poolRef because egress auth token is not injected into prewarmed pods
```

Suggested issue body summary:

```text
In non-pool Kubernetes create, OpenSandbox generates opensandbox.io/egress-auth-token and injects the same value into OPENSANDBOX_EGRESS_TOKEN on the egress sidecar. Credential Vault works because GetEndpoint(18080) returns OPENSANDBOX-EGRESS-AUTH matching the sidecar token.

In Pool/poolRef mode, OpenSandbox generates opensandbox.io/egress-auth-token on the BatchSandbox and GetEndpoint(18080) returns OPENSANDBOX-EGRESS-AUTH, but the selected prewarmed Pool pod's already-running egress sidecar does not receive OPENSANDBOX_EGRESS_TOKEN. Runtime /policy updates may work when the sidecar has no token, but /credential-vault fails with "credential vault requires egress API auth token".

Please add a supported token handoff path for pooled pods, such as dynamic token file reload, pod annotation + Downward API, or per-pod token propagation from Pool pod to BatchSandbox annotation.
```
