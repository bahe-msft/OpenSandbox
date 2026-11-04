#!/usr/bin/env bash
# Copyright 2026 The OpenSandbox Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EXAMPLE="$ROOT/examples/authn-exec"
CLUSTER=osb-authn-exec
CONTEXT="kind-$CLUSTER"
for tool in docker kind kubectl timeout; do
    command -v "$tool" >/dev/null || { printf 'Missing prerequisite: %s\n' "$tool" >&2; exit 1; }
done
timeout 10s docker info >/dev/null 2>&1 || { printf 'Docker daemon access is required.\n' >&2; exit 1; }
WORKDIR="$(mktemp -d)"
chmod 700 "$WORKDIR"
KUBECONFIG_FILE="$WORKDIR/kubeconfig"
reviewer_removed=0
k() { kubectl --kubeconfig "$KUBECONFIG_FILE" --context "$CONTEXT" --request-timeout=15s "$@"; }
restore_reviewer() {
    k create clusterrolebinding osb-authn-token-reviewer --clusterrole=osb-authn-token-reviewer \
        --serviceaccount=osb-authn:server --dry-run=client -o yaml | k apply -f -
}
finish() {
    result=$?
    trap - EXIT
    if (( reviewer_removed )); then
        restore_reviewer || printf 'Could not restore checker TokenReview RBAC.\n' >&2
    fi
    if (( result != 0 )) && [[ -s "$KUBECONFIG_FILE" ]]; then
        k -n osb-authn get pods -o wide || true
        k -n osb-authn get events --sort-by=.lastTimestamp || true
        for deployment in opensandbox-server; do
            k -n osb-authn logs "deployment/$deployment" --tail=80 || true
            k -n osb-authn logs "deployment/$deployment" --previous --tail=80 || true
        done
    fi
    rm -rf "$WORKDIR"
    exit "$result"
}
trap finish EXIT
clusters="$(timeout 15s kind get clusters)"
if grep -Fxq "$CLUSTER" <<< "$clusters"; then
    timeout 15s kind get kubeconfig --name "$CLUSTER" > "$KUBECONFIG_FILE"
else
    timeout 180s kind create cluster --name "$CLUSTER" --image kindest/node:v1.34.0 \
        --kubeconfig "$KUBECONFIG_FILE" --wait 60s
fi
chmod 600 "$KUBECONFIG_FILE"
timeout 600s docker build -t opensandbox-authn:dev "$ROOT/server"
timeout 120s kind load docker-image opensandbox-authn:dev --name "$CLUSTER"
k apply -f "$ROOT/kubernetes/config/crd/bases"
k wait --for=condition=Established --timeout=60s crd/pools.sandbox.opensandbox.io
k apply -f "$EXAMPLE/manifests.yaml"
k -n osb-authn create configmap client-code --from-file="$EXAMPLE/client.py" \
    --dry-run=client -o yaml | k apply -f -
k -n osb-authn create configmap authn-checker --from-file="$EXAMPLE/checker.py" \
    --dry-run=client -o yaml | k apply -f -
k -n osb-authn rollout restart deployment/opensandbox-server deployment/client deployment/denied
k -n osb-authn get pods -o wide
for deployment in opensandbox-server client denied; do
    k -n osb-authn rollout status "deployment/$deployment" --timeout=60s
done
probe() {
    timeout 75s kubectl --kubeconfig "$KUBECONFIG_FILE" --context "$CONTEXT" --request-timeout=15s \
        -n osb-authn exec "deployment/$1" -- python /poc/client.py "$2"
}
probe client smoke
probe denied denied
reviewer_removed=1
k delete clusterrolebinding osb-authn-token-reviewer
probe client unavailable
restore_reviewer
reviewer_removed=0
probe client recovered
printf '\nExec authentication passed on %s.\nCleanup: kind delete cluster --name %s\n' "$CONTEXT" "$CLUSTER"
