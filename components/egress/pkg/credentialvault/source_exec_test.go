//go:build linux

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

package credentialvault

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	credentialproviderv1 "github.com/alibaba/opensandbox/egress/pkg/credentialprovider/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func writeExecProvider(t *testing.T, dir, name string, response *credentialproviderv1.CredentialResponse) (string, string) {
	t.Helper()
	responsePath := filepath.Join(dir, name+".response")
	requestPath := filepath.Join(dir, name+".request")
	response.ApiVersion = credentialproviderv1.APIVersion
	data, err := proto.Marshal(response)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(responsePath, data, 0o600))

	// Keep response/request files outside the provider directory: every regular
	// file in that directory intentionally represents an executable provider.
	providerDir := filepath.Join(dir, "providers")
	require.NoError(t, os.Mkdir(providerDir, 0o700))
	argsPath := filepath.Join(dir, name+".args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsPath + "'\ncat > '" + requestPath + "'\ncat '" + responsePath + "'\n"
	providerPath := filepath.Join(providerDir, name)
	require.NoError(t, os.WriteFile(providerPath, []byte(script), 0o500))
	return providerDir, responsePath
}

func TestExecCredentialProviderTypedProtocolAndBinaryEncoding(t *testing.T) {
	root := t.TempDir()
	providerDir, _ := writeExecProvider(t, root, "opensandbox-psat", &credentialproviderv1.CredentialResponse{
		Status:     credentialproviderv1.CredentialResponse_SUCCESS,
		Credential: []byte{0x00, 0xff, 0x42},
		Encoding:   credentialproviderv1.CredentialResponse_BASE64URL,
	})
	registry := NewSourceRegistry()
	require.NoError(t, RegisterExecCredentialProviders(
		registry,
		providerDir,
		`[{"name":"opensandbox-psat","args":["--token-file","/custom/token"]}]`,
	))
	source, err := registry.Create([]byte(`{"type":"plugin","value":"opensandbox-psat"}`))
	require.NoError(t, err)
	require.Equal(t, "plugin", source.Type())

	value, err := source.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "AP9C", value)

	requestData, err := os.ReadFile(filepath.Join(root, "opensandbox-psat.request"))
	require.NoError(t, err)
	var request credentialproviderv1.CredentialRequest
	require.NoError(t, proto.Unmarshal(requestData, &request))
	require.Equal(t, credentialproviderv1.APIVersion, request.ApiVersion)
	require.Equal(t, "opensandbox-psat", request.PluginName)
	argsData, err := os.ReadFile(filepath.Join(root, "opensandbox-psat.args"))
	require.NoError(t, err)
	require.Equal(t, "--token-file\n/custom/token\n", string(argsData))

	store := NewStoreWithRegistry(nil, func() bool { return true }, registry)
	policy := testCredentialPolicy(t, `{"defaultAction":"deny","egress":[{"action":"allow","target":"gateway.example.com"}]}`)
	_, err = store.Create(CreateRequest{
		Credentials: []Credential{{Name: "identity", Source: []byte(`{"type":"plugin","value":"opensandbox-psat"}`)}},
		Bindings: []Binding{{
			Name:  "gateway",
			Match: Match{Hosts: []string{"gateway.example.com"}},
			Auth:  Auth{Type: "apiKey", Name: "X-Identity", Credential: "identity"},
		}},
	}, policy)
	require.NoError(t, err)
	snapshot, err := store.ActiveSnapshot()
	require.NoError(t, err)
	require.True(t, snapshot.Cacheable)
	require.True(t, snapshot.Bindings[0].Dynamic)
	require.Empty(t, snapshot.Bindings[0].Headers)

	resolved, err := store.ResolveBindingWithContext(context.Background(), "gateway", snapshot.Revision)
	require.NoError(t, err)
	require.False(t, resolved.Bindings[0].Dynamic)
	require.Equal(t, "AP9C", resolved.Bindings[0].Headers[0].Value)
}

func TestExecCredentialProviderDoesNotCacheDynamicResponse(t *testing.T) {
	root := t.TempDir()
	providerDir, responsePath := writeExecProvider(t, root, "rotating", &credentialproviderv1.CredentialResponse{
		Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("one"),
		Encoding: credentialproviderv1.CredentialResponse_UTF8,
	})
	registry := NewSourceRegistry()
	require.NoError(t, RegisterExecCredentialProviders(registry, providerDir, ""))
	source, err := registry.Create([]byte(`{"type":"plugin","value":"rotating"}`))
	require.NoError(t, err)
	value, err := source.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "one", value)

	data, err := proto.Marshal(&credentialproviderv1.CredentialResponse{
		ApiVersion: credentialproviderv1.APIVersion,
		Status:     credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("two"),
		Encoding: credentialproviderv1.CredentialResponse_UTF8,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(responsePath, data, 0o600))
	value, err = source.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "two", value)
}

func TestExecCredentialProviderCachesOnlyUntilExpiry(t *testing.T) {
	root := t.TempDir()
	providerDir, responsePath := writeExecProvider(t, root, "cached", &credentialproviderv1.CredentialResponse{
		Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("one"),
		Encoding: credentialproviderv1.CredentialResponse_UTF8, Cacheable: true,
		ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
	})
	registry := NewSourceRegistry()
	require.NoError(t, RegisterExecCredentialProviders(registry, providerDir, ""))
	source, err := registry.Create([]byte(`{"type":"plugin","value":"cached"}`))
	require.NoError(t, err)
	value, err := source.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "one", value)

	data, err := proto.Marshal(&credentialproviderv1.CredentialResponse{
		ApiVersion: credentialproviderv1.APIVersion,
		Status:     credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("two"),
		Encoding: credentialproviderv1.CredentialResponse_UTF8,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(responsePath, data, 0o600))
	value, err = source.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "one", value)
}

func TestExecCredentialProviderRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name     string
		response *credentialproviderv1.CredentialResponse
		want     string
	}{
		{
			name: "wrong version",
			response: &credentialproviderv1.CredentialResponse{ApiVersion: "v2", Status: credentialproviderv1.CredentialResponse_SUCCESS,
				Credential: []byte("token"), Encoding: credentialproviderv1.CredentialResponse_UTF8},
			want: "unsupported API version",
		},
		{
			name: "denied",
			response: &credentialproviderv1.CredentialResponse{ApiVersion: credentialproviderv1.APIVersion,
				Status: credentialproviderv1.CredentialResponse_DENIED},
			want: "did not return",
		},
		{
			name: "expired",
			response: &credentialproviderv1.CredentialResponse{ApiVersion: credentialproviderv1.APIVersion,
				Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("token"),
				Encoding: credentialproviderv1.CredentialResponse_UTF8, ExpiresAt: timestamppb.New(time.Now().Add(-time.Minute))},
			want: "expired",
		},
		{
			name: "unsupported encoding",
			response: &credentialproviderv1.CredentialResponse{ApiVersion: credentialproviderv1.APIVersion,
				Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("token")},
			want: "unsupported encoding",
		},
		{
			name: "invalid UTF-8",
			response: &credentialproviderv1.CredentialResponse{ApiVersion: credentialproviderv1.APIVersion,
				Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte{0xff},
				Encoding: credentialproviderv1.CredentialResponse_UTF8},
			want: "invalid UTF-8",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			providerDir, responsePath := writeExecProvider(t, root, "provider", &credentialproviderv1.CredentialResponse{
				Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("placeholder"),
				Encoding: credentialproviderv1.CredentialResponse_UTF8,
			})
			data, err := proto.Marshal(test.response)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(responsePath, data, 0o600))
			registry := NewSourceRegistry()
			require.NoError(t, RegisterExecCredentialProviders(registry, providerDir, ""))
			source, err := registry.Create([]byte(`{"type":"plugin","value":"provider"}`))
			require.NoError(t, err)
			_, err = source.Resolve(context.Background())
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestExecCredentialProviderRejectsMalformedAndOversizedOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "malformed", data: []byte{0xff, 0xff}},
		{name: "oversized", data: make([]byte, maxProviderOutputBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			providerDir, responsePath := writeExecProvider(t, root, "provider", &credentialproviderv1.CredentialResponse{
				Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("placeholder"),
				Encoding: credentialproviderv1.CredentialResponse_UTF8,
			})
			require.NoError(t, os.WriteFile(responsePath, test.data, 0o600))
			registry := NewSourceRegistry()
			require.NoError(t, RegisterExecCredentialProviders(registry, providerDir, ""))
			source, err := registry.Create([]byte(`{"type":"plugin","value":"provider"}`))
			require.NoError(t, err)
			_, err = source.Resolve(context.Background())
			require.Error(t, err)
		})
	}
}

func TestExecCredentialProviderHonorsCancellation(t *testing.T) {
	providerDir := filepath.Join(t.TempDir(), "providers")
	require.NoError(t, os.Mkdir(providerDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(providerDir, "slow"),
		[]byte("#!/bin/sh\nsleep 10\n"),
		0o500,
	))
	registry := NewSourceRegistry()
	require.NoError(t, RegisterExecCredentialProviders(registry, providerDir, ""))
	source, err := registry.Create([]byte(`{"type":"plugin","value":"slow"}`))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = source.Resolve(ctx)
	require.ErrorContains(t, err, "unavailable")
}

func TestExecCredentialProviderRejectsCallerFieldsAndUnsafeExecutable(t *testing.T) {
	root := t.TempDir()
	providerDir, _ := writeExecProvider(t, root, "provider", &credentialproviderv1.CredentialResponse{
		Status: credentialproviderv1.CredentialResponse_SUCCESS, Credential: []byte("token"),
		Encoding: credentialproviderv1.CredentialResponse_UTF8,
	})
	registry := NewSourceRegistry()
	require.NoError(t, RegisterExecCredentialProviders(registry, providerDir, ""))
	_, err := registry.Create([]byte(`{"type":"plugin","value":"provider","path":"/bin/sh"}`))
	require.ErrorContains(t, err, "unknown field")
	_, err = registry.Create([]byte(`{"type":"plugin","value":"missing"}`))
	require.ErrorContains(t, err, "unsupported credential plugin")

	require.NoError(t, os.Chmod(filepath.Join(providerDir, "provider"), 0o522))
	registry = NewSourceRegistry()
	require.ErrorContains(t, RegisterExecCredentialProviders(registry, providerDir, ""), "not group/world-writable")
}
