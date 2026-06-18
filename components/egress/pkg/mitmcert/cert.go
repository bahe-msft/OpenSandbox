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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alibaba/opensandbox/egress/pkg/constants"
	"github.com/alibaba/opensandbox/egress/pkg/log"
)

const CACertName = "mitmproxy-ca-cert.pem"

type Authority struct {
	CertPEM []byte
	KeyPEM  []byte
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
}

func LoadOrCreateAuthority(dir string) (*Authority, error) {
	if dir == "" {
		dir = filepath.Join(constants.OpenSandboxRootDir, "envoy-mitm")
	}
	certPath := filepath.Join(dir, CACertName)
	keyPath := filepath.Join(dir, "mitmproxy-ca-key.pem")
	if certPEM, certErr := os.ReadFile(certPath); certErr == nil {
		keyPEM, keyErr := os.ReadFile(keyPath)
		if keyErr == nil {
			return parseAuthority(certPEM, keyPEM)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	auth, err := createAuthority()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, auth.CertPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, auth.KeyPEM, 0o600); err != nil {
		return nil, err
	}
	return auth, nil
}

func createAuthority() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "OpenSandbox Egress MITM CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &Authority{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		cert:    cert,
		key:     key,
	}, nil
}

func parseAuthority(certPEM, keyPEM []byte) (*Authority, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("missing CA certificate PEM block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("missing CA key PEM block")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &Authority{CertPEM: certPEM, KeyPEM: keyPEM, cert: cert, key: key}, nil
}

func (a *Authority) MintLeaf(host string) (certPEM, keyPEM []byte, err error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, nil, fmt.Errorf("host is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func ExportCA(auth *Authority) error {
	if err := os.MkdirAll(constants.OpenSandboxRootDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(constants.OpenSandboxRootDir, CACertName)
	if err := os.WriteFile(dst, auth.CertPEM, 0o644); err != nil {
		return err
	}
	if err := installSystemTrust(dst); err != nil {
		return err
	}
	log.Infof("[envoy] copied root CA to %s", dst)
	return nil
}

func installSystemTrust(pemPath string) error {
	if _, err := exec.LookPath("update-ca-certificates"); err != nil {
		return fmt.Errorf("update-ca-certificates not found: %w", err)
	}
	dir := "/usr/local/share/ca-certificates"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(pemPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "opensandbox-envoy-mitm-ca.crt"), data, 0o644); err != nil {
		return err
	}
	out, err := exec.Command("update-ca-certificates").CombinedOutput()
	if err != nil {
		return fmt.Errorf("update-ca-certificates: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}
