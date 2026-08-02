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

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/alibaba/OpenSandbox/sandbox-k8s/internal/imagecommitter"
)

// The double slash preserves ARM's trailing-slash resource identifier in the
// resulting token audience (https://management.azure.com/).
const azureContainerRegistryScope = "https://management.azure.com//.default"

// acrCredentialProvider exchanges an azidentity access token for an ACR
// refresh token. The image committer does not read identity tokens directly;
// azidentity selects and consumes the available credential source.
type acrCredentialProvider struct {
	credential       azcore.TokenCredential
	tenantID         string
	client           *http.Client
	exchangeEndpoint func(registryHost string) string
}

// newACRCredentialProvider creates a provider using azidentity's default
// credential chain. In Kubernetes, Workload Identity is preferred when its
// webhook-injected environment and projected token are available.
func newACRCredentialProvider() (*acrCredentialProvider, error) {
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure credential: %w", err)
	}
	return &acrCredentialProvider{
		credential: credential,
		tenantID:   strings.TrimSpace(os.Getenv("AZURE_TENANT_ID")),
		client:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (p *acrCredentialProvider) Credential(ctx context.Context, registryHost string) (imagecommitter.RegistryCredential, error) {
	if p == nil || p.credential == nil {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("Azure credential is not configured")
	}
	exchangeEndpoint, err := p.acrExchangeEndpoint(registryHost)
	if err != nil {
		return imagecommitter.RegistryCredential{}, err
	}
	accessToken, err := p.credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{azureContainerRegistryScope},
	})
	if err != nil {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("get Azure access token: %w", err)
	}
	tenantID := p.tenantID
	if tenantID == "" {
		tenantID, err = tenantIDFromJWT(accessToken.Token)
		if err != nil {
			return imagecommitter.RegistryCredential{}, fmt.Errorf("determine Azure tenant: %w", err)
		}
	}

	form := url.Values{
		"grant_type":   {"access_token"},
		"service":      {registryHost},
		"tenant":       {tenantID},
		"access_token": {accessToken.Token},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("create ACR token exchange request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := p.httpClient().Do(request)
	if err != nil {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("exchange Azure token with ACR %s: %w", registryHost, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("read ACR token exchange response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("ACR token exchange for %s returned %s: %s", registryHost, response.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("decode ACR token exchange response: %w", err)
	}
	if result.RefreshToken == "" {
		return imagecommitter.RegistryCredential{}, fmt.Errorf("ACR token exchange for %s returned an empty refresh token", registryHost)
	}
	return imagecommitter.RegistryCredential{RefreshToken: result.RefreshToken}, nil
}

func (p *acrCredentialProvider) acrExchangeEndpoint(registryHost string) (string, error) {
	if p.exchangeEndpoint != nil {
		return p.exchangeEndpoint(registryHost), nil
	}
	if !isAzureContainerRegistryHost(registryHost) {
		return "", fmt.Errorf("registry host %q is not an Azure Container Registry login server", registryHost)
	}
	endpoint := url.URL{Scheme: "https", Host: registryHost, Path: "/oauth2/exchange"}
	return endpoint.String(), nil
}

func isAzureContainerRegistryHost(registryHost string) bool {
	host := strings.ToLower(strings.Split(registryHost, ":")[0])
	for _, suffix := range []string{".azurecr.io", ".azurecr.cn", ".azurecr.us", ".azurecr.de"} {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return false
}

func (p *acrCredentialProvider) httpClient() *http.Client {
	if p.client != nil {
		return p.client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func tenantIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("Azure access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode Azure access token claims: %w", err)
	}
	var claims struct {
		TenantID string `json:"tid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode Azure access token claims: %w", err)
	}
	if claims.TenantID == "" {
		return "", fmt.Errorf("Azure access token has no tid claim")
	}
	return claims.TenantID, nil
}
