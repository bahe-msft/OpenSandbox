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
	"context"
	"io"
	"net"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/grpc"

	"github.com/alibaba/opensandbox/egress/pkg/credentialvault"
	"github.com/alibaba/opensandbox/egress/pkg/log"
)

type Server struct {
	store     *credentialvault.Store
	allowHost func(string) bool
}

func New(store *credentialvault.Store, allowHost ...func(string) bool) *Server {
	s := &Server{store: store}
	if len(allowHost) > 0 {
		s.allowHost = allowHost[0]
	}
	return s
}

func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	grpcServer := grpc.NewServer()
	extprocv3.RegisterExternalProcessorServer(grpcServer, s)
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

func (s *Server) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		resp := &extprocv3.ProcessingResponse{}
		switch typed := req.Request.(type) {
		case *extprocv3.ProcessingRequest_RequestHeaders:
			resp = s.handleRequestHeaders(typed.RequestHeaders)
		default:
			resp.Response = &extprocv3.ProcessingResponse_RequestHeaders{
				RequestHeaders: &extprocv3.HeadersResponse{},
			}
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

func (s *Server) handleRequestHeaders(req *extprocv3.HttpHeaders) *extprocv3.ProcessingResponse {
	mutation := &extprocv3.HeaderMutation{}
	empty := func() *extprocv3.ProcessingResponse {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{RequestHeaders: &extprocv3.HeadersResponse{Response: &extprocv3.CommonResponse{HeaderMutation: mutation}}}}
	}
	if s.store == nil {
		return empty()
	}
	snapshot, err := s.store.ActiveSnapshot()
	if err != nil {
		return empty()
	}
	headers := headersToMap(req.GetHeaders().GetHeaders())
	info := parseRequestInfo(headers)
	binding := selectBinding(info, snapshot)
	if binding == nil {
		if s.allowHost != nil && !s.allowHost(info.host) {
			return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ImmediateResponse{ImmediateResponse: &extprocv3.ImmediateResponse{
				Status:  &typev3.HttpStatus{Code: typev3.StatusCode_Forbidden},
				Body:    []byte("egress denied\n"),
				Details: "opensandbox_egress_denied",
			}}}
		}
		return empty()
	}
	for _, h := range binding.Headers {
		name := strings.ToLower(h.Name)
		mutation.SetHeaders = append(mutation.SetHeaders, &corev3.HeaderValueOption{
			Header:       &corev3.HeaderValue{Key: name, RawValue: []byte(h.Value)},
			AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
		})
	}
	if len(binding.Headers) > 0 {
		log.Infof("envoy credential proxy: injected binding=%s revision=%d host=%s method=%s headers=%s",
			binding.Name, snapshot.Revision, info.host, info.method, headerNames(binding.Headers))
	}
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{RequestHeaders: &extprocv3.HeadersResponse{Response: &extprocv3.CommonResponse{HeaderMutation: mutation}}}}
}

func headersToMap(headers []*corev3.HeaderValue) map[string]string {
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		value := h.Value
		if value == "" && len(h.RawValue) > 0 {
			value = string(h.RawValue)
		}
		out[strings.ToLower(h.Key)] = value
	}
	return out
}

func headerNames(headers []credentialvault.InjectionHeader) string {
	names := make([]string, 0, len(headers))
	for _, h := range headers {
		names = append(names, h.Name)
	}
	return strings.Join(names, ",")
}
