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
	crand "crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sandboxv1alpha1 "github.com/alibaba/OpenSandbox/sandbox-k8s/apis/sandbox/v1alpha1"
)

const (
	ContainerNameEgress               = "egress"
	EnvOpenSandboxEgressToken         = "OPENSANDBOX_EGRESS_TOKEN"
	EnvOpenSandboxEgressTokenFile     = "OPENSANDBOX_EGRESS_TOKEN_FILE"
	AnnoOpenSandboxEgressAuthTokenKey = "opensandbox.io/egress-auth-token"

	egressTokenVolumeName = "opensandbox-egress-token"
	egressTokenMountPath  = "/var/run/opensandbox/egress-token"
	egressTokenFileName   = "token"
	egressTokenSecretKey  = "token"
	egressAuthHeader      = "OPENSANDBOX-EGRESS-AUTH"
	egressPolicyAPIPath   = "/policy"
)

var (
	egressTokenPropagationTimeout  = 60 * time.Second
	egressTokenPropagationInterval = time.Second
	egressTokenHTTPClient          = &http.Client{Timeout: 2 * time.Second}
	waitEgressTokenPropagationFunc = waitEgressTokenPropagation
)

func generateEgressAuthToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := crand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func ensurePoolPodEgressToken(ctx context.Context, c client.Client, pool *sandboxv1alpha1.Pool, pod *corev1.Pod) error {
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		if container.Name != ContainerNameEgress {
			continue
		}
		if hasConfiguredEgressToken(*container) {
			return nil
		}

		token, err := generateEgressAuthToken()
		if err != nil {
			return err
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: pool.Name + "-egress-token-",
				Namespace:    pod.Namespace,
				Labels: map[string]string{
					LabelPoolName: pool.Name,
				},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pool, sandboxv1alpha1.SchemeBuilder.GroupVersion.WithKind("Pool"))},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				egressTokenSecretKey: []byte(token),
			},
		}
		if err := c.Create(ctx, secret); err != nil {
			return err
		}

		ensureEgressTokenSecretVolume(pod, secret.Name)
		ensureEgressTokenSecretMount(container)
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  EnvOpenSandboxEgressTokenFile,
			Value: filepath.Join(egressTokenMountPath, egressTokenFileName),
		})
		return nil
	}
	return nil
}

func hasConfiguredEgressToken(container corev1.Container) bool {
	for _, env := range container.Env {
		switch env.Name {
		case EnvOpenSandboxEgressToken:
			if strings.TrimSpace(env.Value) != "" || env.ValueFrom != nil {
				return true
			}
		case EnvOpenSandboxEgressTokenFile:
			if strings.TrimSpace(env.Value) != "" {
				return true
			}
		}
	}
	return false
}

func ensureEgressTokenSecretVolume(pod *corev1.Pod, secretName string) {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == egressTokenVolumeName {
			pod.Spec.Volumes[i].VolumeSource = corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: secretName,
				Items:      []corev1.KeyToPath{{Key: egressTokenSecretKey, Path: egressTokenFileName}},
			}}
			return
		}
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: egressTokenVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: secretName,
			Items:      []corev1.KeyToPath{{Key: egressTokenSecretKey, Path: egressTokenFileName}},
		}},
	})
}

func ensureEgressTokenSecretMount(container *corev1.Container) {
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == egressTokenVolumeName {
			container.VolumeMounts[i].MountPath = egressTokenMountPath
			container.VolumeMounts[i].ReadOnly = true
			container.VolumeMounts[i].SubPath = ""
			return
		}
	}
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      egressTokenVolumeName,
		MountPath: egressTokenMountPath,
		ReadOnly:  true,
	})
}

func literalEgressTokenFromPod(pod *corev1.Pod) string {
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		if container.Name != ContainerNameEgress {
			continue
		}
		for _, env := range container.Env {
			if env.Name == EnvOpenSandboxEgressToken && strings.TrimSpace(env.Value) != "" {
				return env.Value
			}
		}
		return ""
	}
	return ""
}

func egressTokenSecretNameFromPod(pod *corev1.Pod) string {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == egressTokenVolumeName && volume.Secret != nil {
			return volume.Secret.SecretName
		}
	}
	return ""
}

func egressTokenFromPod(ctx context.Context, c client.Client, pod *corev1.Pod) (string, error) {
	if token := literalEgressTokenFromPod(pod); token != "" {
		return token, nil
	}
	secretName := egressTokenSecretNameFromPod(pod)
	if secretName == "" {
		return "", nil
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: secretName}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(secret.Data[egressTokenSecretKey])), nil
}

func egressTokenForAllocatedPods(ctx context.Context, c client.Client, namespace string, pods []string) (string, error) {
	for _, podName := range pods {
		if podName == "" {
			continue
		}
		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return "", err
		}
		token, err := egressTokenFromPod(ctx, c, pod)
		if err != nil {
			return "", err
		}
		if token != "" {
			return token, nil
		}
	}
	return "", nil
}

func rotateEgressTokenForPods(ctx context.Context, c client.Client, namespace string, pods []*corev1.Pod, recycled map[string][]string) ([]string, error) {
	if len(recycled) == 0 {
		return nil, nil
	}
	podByName := make(map[string]*corev1.Pod, len(pods))
	for _, pod := range pods {
		if pod != nil {
			podByName[pod.Name] = pod
		}
	}
	rotated := make(map[string]struct{})
	timedOutPods := make([]string, 0)
	for _, podNames := range recycled {
		for _, podName := range podNames {
			if _, ok := rotated[podName]; ok {
				continue
			}
			pod := podByName[podName]
			if pod == nil {
				continue
			}
			secretName := egressTokenSecretNameFromPod(pod)
			if secretName == "" {
				continue
			}
			secret := &corev1.Secret{}
			if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, secret); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			oldToken := strings.TrimSpace(string(secret.Data[egressTokenSecretKey]))
			newToken, err := generateEgressAuthToken()
			if err != nil {
				return nil, err
			}
			if secret.Data == nil {
				secret.Data = map[string][]byte{}
			}
			secret.Data[egressTokenSecretKey] = []byte(newToken)
			if err := c.Update(ctx, secret); err != nil {
				return nil, fmt.Errorf("rotate egress token secret %s/%s: %w", namespace, secretName, err)
			}
			rotated[podName] = struct{}{}
			if !waitEgressTokenPropagationFunc(ctx, pod, oldToken, newToken) {
				timedOutPods = append(timedOutPods, podName)
			}
		}
	}
	return timedOutPods, nil
}

func waitEgressTokenPropagation(ctx context.Context, pod *corev1.Pod, oldToken, newToken string) bool {
	if pod == nil || strings.TrimSpace(pod.Status.PodIP) == "" || strings.TrimSpace(newToken) == "" {
		return false
	}
	waitCtx, cancel := context.WithTimeout(ctx, egressTokenPropagationTimeout)
	defer cancel()

	for {
		oldRejected := oldToken == "" || egressPolicyStatus(waitCtx, pod.Status.PodIP, oldToken) == http.StatusUnauthorized
		newStatus := egressPolicyStatus(waitCtx, pod.Status.PodIP, newToken)
		newAccepted := newStatus != 0 && newStatus != http.StatusUnauthorized
		if oldRejected && newAccepted {
			return true
		}

		timer := time.NewTimer(egressTokenPropagationInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false
		case <-timer.C:
		}
	}
}

func egressPolicyStatus(ctx context.Context, podIP string, token string) int {
	url := fmt.Sprintf("http://%s:18080%s", podIP, egressPolicyAPIPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set(egressAuthHeader, token)
	resp, err := egressTokenHTTPClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func removePodsFromRecycleMap(recycled map[string][]string, podsToRemove []string) map[string][]string {
	if len(podsToRemove) == 0 {
		return recycled
	}
	removeSet := make(map[string]struct{}, len(podsToRemove))
	for _, podName := range podsToRemove {
		removeSet[podName] = struct{}{}
	}
	for sandboxName, podNames := range recycled {
		kept := podNames[:0]
		for _, podName := range podNames {
			if _, ok := removeSet[podName]; !ok {
				kept = append(kept, podName)
			}
		}
		if len(kept) == 0 {
			delete(recycled, sandboxName)
			continue
		}
		recycled[sandboxName] = kept
	}
	return recycled
}
