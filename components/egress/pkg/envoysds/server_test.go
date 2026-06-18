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
