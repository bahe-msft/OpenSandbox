// Copyright 2026 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sandboxv1alpha1 "github.com/alibaba/OpenSandbox/sandbox-k8s/apis/sandbox/v1alpha1"
)

func newEgressTokenTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, sandboxv1alpha1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func testPool() *sandboxv1alpha1.Pool {
	return &sandboxv1alpha1.Pool{ObjectMeta: metav1.ObjectMeta{Name: "pool1", Namespace: "default", UID: "pool-uid"}}
}

func TestEnsurePoolPodEgressTokenCreatesSecretVolume(t *testing.T) {
	pool := testPool()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sandbox"}, {Name: ContainerNameEgress}}}}
	c := newEgressTokenTestClient(t, pool)

	err := ensurePoolPodEgressToken(context.Background(), c, pool, pod)
	require.NoError(t, err)

	tokenFile := ""
	for _, env := range pod.Spec.Containers[1].Env {
		if env.Name == EnvOpenSandboxEgressTokenFile {
			tokenFile = env.Value
		}
	}
	assert.Equal(t, "/var/run/opensandbox/egress-token/token", tokenFile)
	assert.Len(t, pod.Spec.Volumes, 1)
	assert.Equal(t, egressTokenVolumeName, pod.Spec.Volumes[0].Name)
	assert.NotEmpty(t, pod.Spec.Volumes[0].Secret.SecretName)
	assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 1)
	assert.Equal(t, egressTokenMountPath, pod.Spec.Containers[1].VolumeMounts[0].MountPath)

	secret := &corev1.Secret{}
	err = c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: pod.Spec.Volumes[0].Secret.SecretName}, secret)
	require.NoError(t, err)
	assert.NotEmpty(t, secret.Data[egressTokenSecretKey])
}

func TestEnsurePoolPodEgressTokenPreservesExistingToken(t *testing.T) {
	pool := testPool()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: ContainerNameEgress,
		Env:  []corev1.EnvVar{{Name: EnvOpenSandboxEgressToken, Value: "existing-token"}},
	}}}}
	c := newEgressTokenTestClient(t, pool)

	err := ensurePoolPodEgressToken(context.Background(), c, pool, pod)
	require.NoError(t, err)

	assert.Empty(t, pod.Spec.Volumes)
	assert.Equal(t, "existing-token", literalEgressTokenFromPod(pod))
}

func TestEnsurePoolPodEgressTokenPreservesExistingTokenValueFrom(t *testing.T) {
	pool := testPool()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: ContainerNameEgress,
		Env: []corev1.EnvVar{{
			Name: EnvOpenSandboxEgressToken,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "egress-token"},
				Key:                  "token",
			}},
		}},
	}}}}
	c := newEgressTokenTestClient(t, pool)

	err := ensurePoolPodEgressToken(context.Background(), c, pool, pod)
	require.NoError(t, err)

	assert.Empty(t, pod.Spec.Volumes)
	assert.NotNil(t, pod.Spec.Containers[0].Env[0].ValueFrom)
	assert.Empty(t, pod.Spec.Containers[0].Env[0].Value)
}

func TestEnsurePoolPodEgressTokenNoopWithoutEgressContainer(t *testing.T) {
	pool := testPool()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sandbox"}}}}
	c := newEgressTokenTestClient(t, pool)

	err := ensurePoolPodEgressToken(context.Background(), c, pool, pod)
	require.NoError(t, err)

	assert.Empty(t, pod.Spec.Volumes)
	assert.Empty(t, pod.Spec.Containers[0].Env)
}

func TestEgressTokenForAllocatedPodsReadsSecret(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token-secret", Namespace: "default"}, Data: map[string][]byte{egressTokenSecretKey: []byte("pod-token")}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: ContainerNameEgress}},
			Volumes: []corev1.Volume{{Name: egressTokenVolumeName, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: "token-secret",
			}}}},
		},
	}
	c := newEgressTokenTestClient(t, secret, pod)

	token, err := egressTokenForAllocatedPods(context.Background(), c, "default", []string{"pod1"})
	require.NoError(t, err)
	assert.Equal(t, "pod-token", token)
}

func TestRotateEgressTokenForPodsUpdatesSecret(t *testing.T) {
	oldWait := waitEgressTokenPropagationFunc
	waitEgressTokenPropagationFunc = func(context.Context, *corev1.Pod, string, string) bool { return true }
	t.Cleanup(func() { waitEgressTokenPropagationFunc = oldWait })

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token-secret", Namespace: "default"}, Data: map[string][]byte{egressTokenSecretKey: []byte("old-token")}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: egressTokenVolumeName, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: "token-secret",
		}}}}},
	}
	c := newEgressTokenTestClient(t, secret)

	timedOut, err := rotateEgressTokenForPods(context.Background(), c, "default", []*corev1.Pod{pod}, map[string][]string{"sbx": {"pod1"}})
	require.NoError(t, err)
	assert.Empty(t, timedOut)

	updated := &corev1.Secret{}
	err = c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "token-secret"}, updated)
	require.NoError(t, err)
	assert.NotEqual(t, "old-token", string(updated.Data[egressTokenSecretKey]))
	assert.NotEmpty(t, updated.Data[egressTokenSecretKey])
}

func TestRotateEgressTokenForPodsReturnsTimedOutPods(t *testing.T) {
	oldWait := waitEgressTokenPropagationFunc
	waitEgressTokenPropagationFunc = func(context.Context, *corev1.Pod, string, string) bool { return false }
	t.Cleanup(func() { waitEgressTokenPropagationFunc = oldWait })

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token-secret", Namespace: "default"}, Data: map[string][]byte{egressTokenSecretKey: []byte("old-token")}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: egressTokenVolumeName, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: "token-secret",
		}}}}},
	}
	c := newEgressTokenTestClient(t, secret)

	timedOut, err := rotateEgressTokenForPods(context.Background(), c, "default", []*corev1.Pod{pod}, map[string][]string{"sbx": {"pod1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"pod1"}, timedOut)
}

func TestRemovePodsFromRecycleMap(t *testing.T) {
	recycled := map[string][]string{
		"sbx1": {"pod1", "pod2"},
		"sbx2": {"pod3"},
	}

	got := removePodsFromRecycleMap(recycled, []string{"pod2", "pod3"})

	assert.Equal(t, map[string][]string{"sbx1": {"pod1"}}, got)
}
