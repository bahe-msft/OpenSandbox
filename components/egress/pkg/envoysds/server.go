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
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	secretv3 "github.com/envoyproxy/go-control-plane/envoy/service/secret/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
)

const TypeURLSecret = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret"

type Server struct {
	secretName string
	version    atomic.Uint64

	mu      sync.RWMutex
	secret  *tlsv3.Secret
	streams map[chan *discoveryv3.DiscoveryResponse]struct{}
}

func New(secretName string, certPEM, keyPEM []byte) (*Server, error) {
	if secretName == "" {
		return nil, fmt.Errorf("secret name is required")
	}
	s := &Server{secretName: secretName, streams: map[chan *discoveryv3.DiscoveryResponse]struct{}{}}
	if err := s.Update(certPEM, keyPEM); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	grpcServer := grpc.NewServer()
	secretv3.RegisterSecretDiscoveryServiceServer(grpcServer, s)
	errCh := make(chan error, 1)
	go func() { errCh <- grpcServer.Serve(lis) }()
	select {
	case <-ctx.Done():
		grpcServer.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) Update(certPEM, keyPEM []byte) error {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return fmt.Errorf("certificate and private key are required")
	}
	secret := &tlsv3.Secret{
		Name: s.secretName,
		Type: &tlsv3.Secret_TlsCertificate{TlsCertificate: &tlsv3.TlsCertificate{
			CertificateChain: &corev3.DataSource{Specifier: &corev3.DataSource_InlineBytes{InlineBytes: certPEM}},
			PrivateKey:       &corev3.DataSource{Specifier: &corev3.DataSource_InlineBytes{InlineBytes: keyPEM}},
		}},
	}
	version := s.version.Add(1)
	s.mu.Lock()
	s.secret = secret
	streams := make([]chan *discoveryv3.DiscoveryResponse, 0, len(s.streams))
	for ch := range s.streams {
		streams = append(streams, ch)
	}
	s.mu.Unlock()
	resp, err := s.response(version)
	if err != nil {
		return err
	}
	for _, ch := range streams {
		select {
		case ch <- resp:
		default:
		}
	}
	return nil
}

func (s *Server) StreamSecrets(stream secretv3.SecretDiscoveryService_StreamSecretsServer) error {
	updates := make(chan *discoveryv3.DiscoveryResponse, 4)
	errCh := make(chan error, 1)
	s.mu.Lock()
	s.streams[updates] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.streams, updates)
		s.mu.Unlock()
	}()

	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				errCh <- err
				return
			}
			resp, err := s.response(s.version.Load())
			if err == nil {
				select {
				case updates <- resp:
				default:
				}
			}
		}
	}()

	resp, err := s.response(s.version.Load())
	if err != nil {
		return err
	}
	if err := stream.Send(resp); err != nil {
		return err
	}
	for {
		select {
		case resp := <-updates:
			if err := stream.Send(resp); err != nil {
				return err
			}
		case err := <-errCh:
			return err
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (s *Server) FetchSecrets(context.Context, *discoveryv3.DiscoveryRequest) (*discoveryv3.DiscoveryResponse, error) {
	return s.response(s.version.Load())
}

func (s *Server) DeltaSecrets(secretv3.SecretDiscoveryService_DeltaSecretsServer) error {
	return fmt.Errorf("delta SDS is not supported")
}

func (s *Server) response(version uint64) (*discoveryv3.DiscoveryResponse, error) {
	s.mu.RLock()
	secret := s.secret
	s.mu.RUnlock()
	if secret == nil {
		return nil, fmt.Errorf("secret is not initialized")
	}
	resource, err := anypb.New(secret)
	if err != nil {
		return nil, err
	}
	return &discoveryv3.DiscoveryResponse{
		VersionInfo: fmt.Sprintf("%d", version),
		Resources:   []*anypb.Any{resource},
		TypeUrl:     TypeURLSecret,
		Nonce:       fmt.Sprintf("%d", version),
	}, nil
}
