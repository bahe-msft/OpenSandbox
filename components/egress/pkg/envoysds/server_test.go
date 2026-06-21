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

package envoysds

import (
	"context"
	"io"
	"testing"

	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/stretchr/testify/require"
)

func TestUpdateChangesSecretResponse(t *testing.T) {
	srv, err := New("downstream", []byte("cert-1"), []byte("key-1"))
	require.NoError(t, err)
	first, err := srv.FetchSecrets(context.Background(), &discoveryv3.DiscoveryRequest{})
	require.NoError(t, err)
	require.Equal(t, "1", first.VersionInfo)

	require.NoError(t, srv.Update([]byte("cert-2"), []byte("key-2")))
	second, err := srv.FetchSecrets(context.Background(), &discoveryv3.DiscoveryRequest{})
	require.NoError(t, err)
	require.Equal(t, "2", second.VersionInfo)
	require.Len(t, second.Resources, 1)

	secret := &tlsv3.Secret{}
	require.NoError(t, second.Resources[0].UnmarshalTo(secret))
	require.Equal(t, "downstream", secret.Name)
	require.Equal(t, []byte("cert-2"), secret.GetTlsCertificate().GetCertificateChain().GetInlineBytes())
	require.Equal(t, []byte("key-2"), secret.GetTlsCertificate().GetPrivateKey().GetInlineBytes())
}

func TestDeltaResourceMintsRequestedSecretName(t *testing.T) {
	srv, err := New("default", []byte("cert-default"), []byte("key-default"))
	require.NoError(t, err)
	srv.SetMintFunc(func(name string) ([]byte, []byte, error) {
		return []byte("cert-" + name), []byte("key-" + name), nil
	})

	res, err := srv.deltaResource("dev.azure.com")
	require.NoError(t, err)
	require.Equal(t, "dev.azure.com", res.Name)
	secret := &tlsv3.Secret{}
	require.NoError(t, res.Resource.UnmarshalTo(secret))
	require.Equal(t, "dev.azure.com", secret.Name)
	require.Equal(t, []byte("cert-dev.azure.com"), secret.GetTlsCertificate().GetCertificateChain().GetInlineBytes())
	require.Equal(t, []byte("key-dev.azure.com"), secret.GetTlsCertificate().GetPrivateKey().GetInlineBytes())
}

func TestDeltaResourceRejectsDisallowedSecretName(t *testing.T) {
	srv, err := New("default", []byte("cert-default"), []byte("key-default"))
	require.NoError(t, err)
	srv.SetAllowFunc(func(name string) bool { return name == "allowed.example.com" })
	srv.SetMintFunc(func(name string) ([]byte, []byte, error) {
		return []byte("cert-" + name), []byte("key-" + name), nil
	})

	_, err = srv.deltaResource("blocked.example.com")
	require.ErrorContains(t, err, "not allowed")
}

var _ secretDeltaStream = (*fakeDeltaStream)(nil)

type secretDeltaStream interface {
	Send(*discoveryv3.DeltaDiscoveryResponse) error
	Recv() (*discoveryv3.DeltaDiscoveryRequest, error)
}

type fakeDeltaStream struct {
	reqs []*discoveryv3.DeltaDiscoveryRequest
	sent []*discoveryv3.DeltaDiscoveryResponse
}

func (f *fakeDeltaStream) Send(resp *discoveryv3.DeltaDiscoveryResponse) error {
	f.sent = append(f.sent, resp)
	return nil
}

func (f *fakeDeltaStream) Recv() (*discoveryv3.DeltaDiscoveryRequest, error) {
	if len(f.reqs) == 0 {
		return nil, io.EOF
	}
	req := f.reqs[0]
	f.reqs = f.reqs[1:]
	return req, nil
}
