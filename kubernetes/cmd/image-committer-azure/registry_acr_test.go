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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/alibaba/OpenSandbox/sandbox-k8s/pkg/imagecommitter"
)

type fakeAzureCredential struct {
	token azcore.AccessToken
	err   error
}

func (f fakeAzureCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if len(options.Scopes) != 1 || options.Scopes[0] != azureContainerRegistryScope {
		return azcore.AccessToken{}, fmt.Errorf("unexpected scopes: %v", options.Scopes)
	}
	return f.token, f.err
}

func TestACRCredentialProviderExchangesAzureToken(t *testing.T) {
	accessToken := testJWT(t, `{"tid":"tenant-id"}`)
	var received url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/exchange" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		received = r.Form
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"refresh_token":"acr-refresh-token"}`)
	}))
	defer server.Close()

	provider := &acrCredentialProvider{
		credential: fakeAzureCredential{token: azcore.AccessToken{Token: accessToken, ExpiresOn: time.Now().Add(time.Hour)}},
		client:     server.Client(),
		exchangeEndpoint: func(string) string {
			return server.URL + "/oauth2/exchange"
		},
	}
	credential, err := provider.Credential(context.Background(), "registry.azurecr.io")
	if err != nil {
		t.Fatalf("Credential failed: %v", err)
	}
	if credential.RefreshToken != "acr-refresh-token" {
		t.Fatalf("refresh token = %q", credential.RefreshToken)
	}
	if received.Get("grant_type") != "access_token" || received.Get("tenant") != "tenant-id" || received.Get("access_token") != accessToken {
		t.Fatalf("unexpected exchange form: %v", received)
	}
}

func TestACRCredentialProviderRejectsNonACRHost(t *testing.T) {
	provider := &acrCredentialProvider{credential: fakeAzureCredential{}}
	if _, err := provider.Credential(context.Background(), "attacker.example.com"); err == nil {
		t.Fatal("non-ACR host should be rejected before obtaining or sending a token")
	}
}

type recordingCredentialProvider struct {
	hosts []string
}

func (p *recordingCredentialProvider) Credential(_ context.Context, host string) (imagecommitter.RegistryCredential, error) {
	p.hosts = append(p.hosts, host)
	return imagecommitter.RegistryCredential{RefreshToken: "token"}, nil
}

func TestACRSourceCredentialProviderUsesIdentityOnlyForACR(t *testing.T) {
	delegate := &recordingCredentialProvider{}
	provider := acrSourceCredentialProvider{provider: delegate}

	credential, err := provider.Credential(context.Background(), "registry.azurecr.io")
	if err != nil {
		t.Fatalf("ACR credential failed: %v", err)
	}
	if credential.RefreshToken != "token" || len(delegate.hosts) != 1 || delegate.hosts[0] != "registry.azurecr.io" {
		t.Fatalf("ACR credential was not delegated: credential=%#v hosts=%v", credential, delegate.hosts)
	}

	credential, err = provider.Credential(context.Background(), "mcr.microsoft.com")
	if err != nil {
		t.Fatalf("public source credential failed: %v", err)
	}
	if credential != (imagecommitter.RegistryCredential{}) {
		t.Fatalf("public source credential = %#v, want anonymous", credential)
	}
	if len(delegate.hosts) != 1 {
		t.Fatalf("non-ACR source was delegated: hosts=%v", delegate.hosts)
	}
}

func TestAzureContainerRegistryHosts(t *testing.T) {
	for _, host := range []string{"example.azurecr.io", "example.azurecr.cn", "example.azurecr.us", "example.azurecr.de"} {
		if !isAzureContainerRegistryHost(host) {
			t.Fatalf("expected %q to be accepted", host)
		}
	}
	for _, host := range []string{"azurecr.io", "example.com", "azurecr.io.attacker.example"} {
		if isAzureContainerRegistryHost(host) {
			t.Fatalf("expected %q to be rejected", host)
		}
	}
}

func TestACRCredentialProviderIntegration(t *testing.T) {
	registryHost := os.Getenv("ACR_INTEGRATION_REGISTRY")
	if registryHost == "" {
		t.Skip("ACR_INTEGRATION_REGISTRY is not set")
	}
	azureCredential, err := azidentity.NewAzureCLICredential(nil)
	if err != nil {
		t.Fatalf("NewAzureCLICredential failed: %v", err)
	}
	provider := &acrCredentialProvider{credential: azureCredential}
	credential, err := provider.Credential(context.Background(), registryHost)
	if err != nil {
		t.Fatalf("Credential failed: %v", err)
	}
	if credential.RefreshToken == "" {
		t.Fatal("ACR exchange returned no refresh token")
	}
}

func TestTenantIDFromJWT(t *testing.T) {
	token := testJWT(t, `{"tid":"tenant-id"}`)
	tenantID, err := tenantIDFromJWT(token)
	if err != nil {
		t.Fatalf("tenantIDFromJWT failed: %v", err)
	}
	if tenantID != "tenant-id" {
		t.Fatalf("tenant ID = %q", tenantID)
	}
	if _, err := tenantIDFromJWT("not-a-token"); err == nil {
		t.Fatal("invalid token should fail")
	}
}

func testJWT(t *testing.T, claims string) string {
	t.Helper()
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"none"}`)) + "." + encode([]byte(claims)) + ".signature"
}
