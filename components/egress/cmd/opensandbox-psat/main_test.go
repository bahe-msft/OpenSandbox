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
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	credentialproviderv1 "github.com/alibaba/opensandbox/egress/pkg/credentialprovider/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func testJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return header + "." + claims + ".signature"
}

func providerRequest(t *testing.T) []byte {
	t.Helper()
	data, err := proto.Marshal(&credentialproviderv1.CredentialRequest{
		ApiVersion: credentialproviderv1.APIVersion,
		PluginName: "opensandbox-psat",
	})
	require.NoError(t, err)
	return data
}

func TestProviderReadsConfiguredTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token := testJWT(time.Now().Add(time.Hour))
	require.NoError(t, os.WriteFile(path, []byte(token), 0o600))
	var output bytes.Buffer
	require.NoError(t, run([]string{"--token-file", path}, bytes.NewReader(providerRequest(t)), &output))

	var response credentialproviderv1.CredentialResponse
	require.NoError(t, proto.Unmarshal(output.Bytes(), &response))
	require.Equal(t, credentialproviderv1.APIVersion, response.ApiVersion)
	require.Equal(t, credentialproviderv1.CredentialResponse_SUCCESS, response.Status)
	require.Equal(t, token, string(response.Credential))
	require.False(t, response.Cacheable)
	require.NotNil(t, response.ExpiresAt)
}

func TestProviderRejectsExpiredAndMalformedTokens(t *testing.T) {
	for name, token := range map[string]string{
		"expired":   testJWT(time.Now().Add(-time.Minute)),
		"malformed": "not-a-jwt",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			require.NoError(t, os.WriteFile(path, []byte(token), 0o600))
			var output bytes.Buffer
			require.NoError(t, run([]string{"--token-file", path}, bytes.NewReader(providerRequest(t)), &output))
			var response credentialproviderv1.CredentialResponse
			require.NoError(t, proto.Unmarshal(output.Bytes(), &response))
			require.Equal(t, credentialproviderv1.CredentialResponse_UNAVAILABLE, response.Status)
			require.Empty(t, response.Credential)
			require.Equal(t, "token_unavailable", response.ErrorCode)
		})
	}
}

func TestProviderRejectsWrongProtocolRequest(t *testing.T) {
	request, err := proto.Marshal(&credentialproviderv1.CredentialRequest{
		ApiVersion: "v2",
		PluginName: "opensandbox-psat",
	})
	require.NoError(t, err)
	var output bytes.Buffer
	require.NoError(t, run(nil, bytes.NewReader(request), &output))
	var response credentialproviderv1.CredentialResponse
	require.NoError(t, proto.Unmarshal(output.Bytes(), &response))
	require.Equal(t, credentialproviderv1.CredentialResponse_DENIED, response.Status)
}
