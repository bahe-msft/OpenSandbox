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

package envoyextproc

import (
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/alibaba/opensandbox/egress/pkg/credentialvault"
	"github.com/alibaba/opensandbox/egress/pkg/mitmproxy"
	"github.com/alibaba/opensandbox/egress/pkg/policy"
)

func TestHandleRequestHeadersInjectsCredential(t *testing.T) {
	store := credentialvault.NewStore(mitmproxy.NewHealthGate(), func() bool { return true })
	pol, err := policy.ParsePolicy(`{"defaultAction":"deny","egress":[{"action":"allow","target":"api.example.com"}]}`)
	require.NoError(t, err)
	_, err = store.Create(credentialvault.CreateRequest{
		Credentials: []credentialvault.Credential{{Name: "token", Source: credentialvault.InlineCredentialSource{Value: "secret"}}},
		Bindings: []credentialvault.Binding{{
			Name:  "api",
			Match: credentialvault.Match{Schemes: []string{"https"}, Ports: []int{443}, Hosts: []string{"api.example.com"}, Methods: []string{"POST"}, Paths: []string{"/v1/*"}},
			Auth:  credentialvault.Auth{Type: "bearer", Credential: "token"},
		}},
	}, pol)
	require.NoError(t, err)

	server := New(store)
	resp := server.handleRequestHeaders(&extprocv3.HttpHeaders{Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
		{Key: ":scheme", Value: "https"},
		{Key: ":authority", Value: "api.example.com"},
		{Key: ":method", Value: "POST"},
		{Key: ":path", Value: "/v1/projects"},
	}}})

	mutation := resp.GetRequestHeaders().GetResponse().GetHeaderMutation()
	require.Empty(t, mutation.RemoveHeaders)
	require.Len(t, mutation.SetHeaders, 1)
	require.Equal(t, "authorization", mutation.SetHeaders[0].Header.Key)
	require.Equal(t, []byte("Bearer secret"), mutation.SetHeaders[0].Header.RawValue)
	require.Equal(t, corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD, mutation.SetHeaders[0].AppendAction)
}

func TestHandleRequestHeadersDeniesDisallowedHost(t *testing.T) {
	store := credentialvault.NewStore(mitmproxy.NewHealthGate(), func() bool { return true })
	pol, err := policy.ParsePolicy(`{"defaultAction":"deny","egress":[{"action":"allow","target":"api.example.com"}]}`)
	require.NoError(t, err)
	_, err = store.Create(credentialvault.CreateRequest{
		Credentials: []credentialvault.Credential{{Name: "token", Source: credentialvault.InlineCredentialSource{Value: "secret"}}},
		Bindings: []credentialvault.Binding{{
			Name:  "api",
			Match: credentialvault.Match{Hosts: []string{"api.example.com"}},
			Auth:  credentialvault.Auth{Type: "bearer", Credential: "token"},
		}},
	}, pol)
	require.NoError(t, err)

	server := New(store, func(host string) bool { return host == "api.example.com" })
	resp := server.handleRequestHeaders(&extprocv3.HttpHeaders{Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
		{Key: ":scheme", Value: "https"},
		{Key: ":authority", Value: "blocked.example.com"},
		{Key: ":method", Value: "GET"},
		{Key: ":path", Value: "/"},
	}}})

	require.NotNil(t, resp.GetImmediateResponse())
	require.Contains(t, string(resp.GetImmediateResponse().Body), "egress denied")
}
