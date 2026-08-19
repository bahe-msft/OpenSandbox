#!/usr/bin/env bash
# Copyright 2026 Alibaba Group Holding Ltd.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Test-cluster-only concurrent dm-thin + Azure Blob round-trip stress harness.
set -euo pipefail
: "${KUBECONFIG:?KUBECONFIG is required}"
: "${DEVMAPPER_STRESS_BLOB_BASE_URL:?DEVMAPPER_STRESS_BLOB_BASE_URL is required}"
run=${1:-$(date +%s)}; count=${2:-8}; size_mib=${3:-64}
node=${DEVMAPPER_STRESS_NODE:-aks-nvkata-59440688-vms5}
image=${DEVMAPPER_STRESS_IMAGE:-ghcr.io/bahe-msft/opensandbox/devmapper-snapshot-poc:poc-devmapper-c700451}
hp=${DEVMAPPER_STRESS_HOST_POD:-self-hosted-kata-devmapper-installer-bkwp2}
source_image=${DEVMAPPER_STRESS_SOURCE_IMAGE:-python:3.12-slim}
state=/tmp/dm-stress-$run.tsv; : >$state
host(){ kubectl -n aks-sandbox-system exec "$hp" -- nsenter -t 1 -m -u -i -n -p -- "$@"; }
cleanup(){ rc=$?;set +e;while IFS=$'\t' read -r slot pod clone restore _;do [[ -n "$restore" ]]&&host ctr -n k8s.io snapshots --snapshotter devmapper rm "$restore" >/dev/null 2>&1;[[ -n "$clone" ]]&&host dmsetup message containerd-thinpool 0 "delete $clone" >/dev/null 2>&1;done <$state;kubectl -n opensandbox delete pod,job -l stress-run=$run --wait=false >/dev/null 2>&1;kubectl -n aks-sandbox-system delete job -l stress-run=$run --wait=false >/dev/null 2>&1;host rm -rf /tmp/dm-stress-$run >/dev/null 2>&1;exit $rc;};trap cleanup EXIT
before=$(host dmsetup status containerd-thinpool);echo run=$run concurrency=$count payload_mib=$size_mib source_image=$source_image;echo pool_before="$before"
# Download AzCopy once into the host tmpfs.
kubectl -n aks-sandbox-system delete pod azcopy-setup-$run --ignore-not-found >/dev/null 2>&1||true
cat <<YAML | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata: {name: azcopy-setup-$run, namespace: aks-sandbox-system, labels: {stress-run: "$run"}}
spec:
 nodeName: $node
 restartPolicy: Never
 containers:
 - name: setup
   image: $image
   imagePullPolicy: Never
   command: ["sh","-ceu"]
   args: ["mkdir -p /host-tmp/dm-stress-$run/tools; curl --fail --location --silent --show-error https://aka.ms/downloadazcopy-v10-linux | tar -xz -C /host-tmp/dm-stress-$run/tools; cp \$(find /host-tmp/dm-stress-$run/tools -type f -name azcopy | head -1) /host-tmp/dm-stress-$run/azcopy; chmod +x /host-tmp/dm-stress-$run/azcopy"]
   volumeMounts: [{name: tmp, mountPath: /host-tmp}]
 volumes:
 - name: tmp
   hostPath: {path: /tmp, type: Directory}
YAML
kubectl -n aks-sandbox-system wait --for=jsonpath='{.status.phase}'=Succeeded pod/azcopy-setup-$run --timeout=120s >/dev/null
# Start all source Kata pods.
for i in $(seq 1 $count);do pod=dm-src-$run-$i;cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: {name: $pod, namespace: opensandbox, labels: {stress-run: "$run", stress-slot: "$i"}}
spec:
 nodeName: $node
 runtimeClassName: self-kata-clh
 restartPolicy: Never
 containers:
 - name: sandbox
   image: $source_image
   imagePullPolicy: IfNotPresent
   command: ["sh","-c","dd if=/dev/urandom of=/root/stress-payload bs=1M count=$size_mib status=none; sha256sum /root/stress-payload | cut -d' ' -f1 > /root/stress-payload.sha256; touch /tmp/ready; sleep 1800"]
   readinessProbe: {exec: {command: ["sh","-c","test -f /tmp/ready"]}, periodSeconds: 1, failureThreshold: 300}
   resources: {requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: "1", memory: 512Mi}}
YAML
done
kubectl -n opensandbox wait --for=condition=Ready pod -l stress-run=$run --timeout=600s >/dev/null;echo sources_ready=$(date -u +%FT%TZ)
# Start all clone jobs concurrently.
for i in $(seq 1 $count);do pod=dm-src-$run-$i;uid=$(kubectl -n opensandbox get pod $pod -o jsonpath='{.metadata.uid}');job=dm-clone-$run-$i;cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata: {name: $job, namespace: opensandbox, labels: {stress-run: "$run", stress-slot: "$i"}}
spec:
 backoffLimit: 0
 template:
  metadata: {labels: {stress-run: "$run", stress-slot: "$i"}}
  spec:
   nodeName: $node
   restartPolicy: Never
   containers:
   - name: clone
     image: $image
     imagePullPolicy: Never
     args: ["$pod","opensandbox","sandbox:ignored"]
     env: [{name: CONTAINERD_SOCKET, value: /run/containerd/containerd.sock},{name: SOURCE_POD_UID, value: "$uid"},{name: DEVMAPPER_POC_ACKNOWLEDGE_UNTRACKED_DEVICE, value: "true"}]
     securityContext: {privileged: true, runAsUser: 0}
     volumeMounts: [{name: sock, mountPath: /run/containerd/containerd.sock},{name: fifo, mountPath: /run/containerd/fifo},{name: dev, mountPath: /dev/mapper}]
   volumes:
   - name: sock
     hostPath: {path: /run/containerd/containerd.sock, type: Socket}
   - name: fifo
     hostPath: {path: /run/containerd/fifo, type: DirectoryOrCreate}
   - name: dev
     hostPath: {path: /dev/mapper, type: Directory}
YAML
done
clone_start=$(date +%s%3N);kubectl -n opensandbox wait --for=condition=complete job -l stress-run=$run --timeout=600s >/dev/null;echo clone_wave_ms=$(($(date +%s%3N)-clone_start))
# Resolve clone/base IDs and prepare independent restore snapshots.
for i in $(seq 1 $count);do pod=dm-src-$run-$i;logs=$(kubectl -n opensandbox logs job/dm-clone-$run-$i);clone=$(sed -nE 's/.*cloneID=([0-9]+).*/\1/p'<<<"$logs"|head -1);srcdev=$(sed -nE 's/.*source=([^ ]+) sourceID=.*/\1/p'<<<"$logs"|head -1);rec=$(host strings /var/lib/containerd/io.containerd.snapshotter.v1.devmapper/containerd-thinpool.db|grep -o "${srcdev}{[^}]*}"|tail -1);pname=$(sed -nE 's/.*"parent_name":"([^"]+)".*/\1/p'<<<"$rec");prec=$(host strings /var/lib/containerd/io.containerd.snapshotter.v1.devmapper/containerd-thinpool.db|grep -o "${pname}{[^}]*}"|tail -1);pid=$(sed -nE 's/.*"device_id":([0-9]+).*/\1/p'<<<"$prec");cid=$(kubectl -n opensandbox get pod $pod -o jsonpath='{.status.containerStatuses[0].containerID}'|sed 's#containerd://##');info=$(host ctr -n k8s.io containers info $cid);skey=$(jq -r .SnapshotKey<<<"$info");parent=$(host ctr -n k8s.io snapshots --snapshotter devmapper info $skey|jq -r .Parent);stable=$(host dmsetup table $srcdev);sectors=$(awk '{print $2}'<<<"$stable");restore=dm-restore-$run-$i;host ctr -n k8s.io snapshots --snapshotter devmapper prepare $restore $parent >/dev/null;rmount=$(host ctr -n k8s.io snapshots --snapshotter devmapper mounts /tmp/r $restore);rdev=$(grep -o 'containerd-thinpool-snap-[0-9]\+'<<<"$rmount"|head -1);expected=$(kubectl -n opensandbox exec $pod -- cat /root/stress-payload.sha256|tr -d '\r\n');printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' $i $pod $clone $restore $pid $sectors $rdev $expected >>$state;done
# Export/upload/download/apply jobs concurrently.
while IFS=$'\t' read -r i pod clone restore pid sectors rdev expected;do job=dm-blob-$run-$i;url="${DEVMAPPER_STRESS_BLOB_BASE_URL%/}/$run/$i/artifact.gz";cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata: {name: $job, namespace: aks-sandbox-system, labels: {stress-run: "$run", stress-slot: "$i"}}
spec:
 backoffLimit: 0
 template:
  metadata: {labels: {stress-run: "$run", stress-slot: "$i", azure.workload.identity/use: "true"}}
  spec:
   serviceAccountName: aks-code-proxy
   nodeName: $node
   restartPolicy: Never
   containers:
   - name: transfer
     image: $image
     imagePullPolicy: Never
     command: ["sh","-ceu"]
     args:
     - |
       work=/host-tmp/dm-stress-$run/$i; mkdir -p \$work/pulled
       meta=\$(dmsetup table containerd-thinpool|awk '{print \$4}'); meta=\$(readlink -f /dev/block/\$meta)
       exec 9>/host-tmp/dm-stress-metadata.lock; flock -x 9
       dmsetup message containerd-thinpool 0 reserve_metadata_snap
       trap 'dmsetup message containerd-thinpool 0 release_metadata_snap >/dev/null 2>&1 || true' EXIT
       thin_delta --metadata-snap --snap1 $pid --snap2 $clone \$meta > \$work/delta.xml
       dmsetup message containerd-thinpool 0 release_metadata_snap; trap - EXIT; flock -u 9
       map=poc-stress-$clone; dmsetup create \$map --readonly --table "0 $sectors thin /dev/mapper/containerd-thinpool $clone"; trap 'dmsetup remove --retry poc-stress-$clone >/dev/null 2>&1 || true' EXIT
       ready=false; for attempt in \$(seq 1 100); do dmsetup mknodes \$map >/dev/null 2>&1 || true; if [ -b /dev/mapper/\$map ]; then ready=true; break; fi; sleep 0.05; done; \$ready
       block-artifact pack --delta \$work/delta.xml --source /dev/mapper/\$map --output \$work/artifact.gz --base stress --virtual-sectors $sectors
       dmsetup remove \$map; trap - EXIT
       before=\$(sha256sum \$work/artifact.gz|awk '{print \$1}'); az=/host-tmp/dm-stress-$run/azcopy
       t=\$(date +%s%N); \$az copy \$work/artifact.gz '$url' --overwrite=true --check-length=true --output-level=essential; upload=\$(( (\$(date +%s%N)-t)/1000000 ))
       rm \$work/artifact.gz; t=\$(date +%s%N); \$az copy '$url' \$work/pulled/artifact.gz --overwrite=true --check-length=true --output-level=essential; download=\$(( (\$(date +%s%N)-t)/1000000 ))
       after=\$(sha256sum \$work/pulled/artifact.gz|awk '{print \$1}'); [ \$before = \$after ]
       block-artifact apply --artifact \$work/pulled/artifact.gz --target /dev/mapper/$rdev
       set +e; e2fsck -fy /dev/mapper/$rdev >/dev/null; rc=\$?; set -e; [ \$rc -lt 4 ]
       echo slot=$i upload_ms=\$upload download_ms=\$download artifact_sha=\$after
     env: [{name: AZCOPY_AUTO_LOGIN_TYPE, value: WORKLOAD}]
     securityContext: {privileged: true, runAsUser: 0}
     volumeMounts: [{name: dev, mountPath: /dev},{name: tmp, mountPath: /host-tmp}]
   volumes:
   - name: dev
     hostPath: {path: /dev, type: Directory}
   - name: tmp
     hostPath: {path: /tmp, type: Directory}
YAML
done <$state
blob_start=$(date +%s%3N);if ! kubectl -n aks-sandbox-system wait --for=condition=complete job -l stress-run=$run --timeout=1800s;then echo FAILED_JOBS;for j in $(kubectl -n aks-sandbox-system get jobs -l stress-run=$run -o name);do kubectl -n aks-sandbox-system logs $j --tail=30||true;done;exit 1;fi;echo blob_wave_ms=$(($(date +%s%3N)-blob_start))
# Mount every reconstruction and verify payload checksum.
fail=0;while IFS=$'\t' read -r i pod clone restore pid sectors rdev expected;do m=/tmp/dm-verify-$run-$i;host mkdir -p $m;host mount -o ro,noload /dev/mapper/$rdev $m;actual=$(host sha256sum $m/root/stress-payload|awk '{print $1}');host umount $m;host rmdir $m;if [[ $actual != $expected ]];then echo slot=$i CHECKSUM_MISMATCH expected=$expected actual=$actual;fail=1;else echo slot=$i checksum=ok $(kubectl -n aks-sandbox-system logs job/dm-blob-$run-$i|tail -1);fi;done <$state
[[ $fail == 0 ]];after=$(host dmsetup status containerd-thinpool);echo pool_after="$after";echo WAVE_VERIFIED run=$run count=$count
