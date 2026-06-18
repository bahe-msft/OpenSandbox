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

package mitmcert

import (
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMintLeafSignedByAuthority(t *testing.T) {
	auth, err := createAuthority()
	require.NoError(t, err)
	certPEM, _, err := auth.MintLeaf("dev.azure.com")
	require.NoError(t, err)
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Contains(t, leaf.DNSNames, "dev.azure.com")
	pool := x509.NewCertPool()
	pool.AddCert(auth.cert)
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: "dev.azure.com", Roots: pool})
	require.NoError(t, err)
}

func TestLoadOrCreateAuthorityPersists(t *testing.T) {
	dir := t.TempDir()
	one, err := LoadOrCreateAuthority(dir)
	require.NoError(t, err)
	two, err := LoadOrCreateAuthority(dir)
	require.NoError(t, err)
	require.Equal(t, one.CertPEM, two.CertPEM)
	require.Equal(t, one.KeyPEM, two.KeyPEM)
}
