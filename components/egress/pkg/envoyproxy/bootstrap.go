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

package envoyproxy

import "fmt"

type BootstrapConfig struct {
	ListenPort  int
	AdminPort   int
	ExtProcAddr string
	SDSAddr     string
	SDSSecret   string
	OnDemandSDS bool
}

func BootstrapYAML(cfg BootstrapConfig) string {
	return fmt.Sprintf(`admin:
  address:
    socket_address:
      address: 127.0.0.1
      port_value: %d

static_resources:
  listeners:
  - name: opensandbox_transparent_http
    address:
      socket_address:
        address: 127.0.0.1
        port_value: %d
    listener_filters:
    - name: envoy.filters.listener.tls_inspector
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
    filter_chains:
    - filter_chain_match:
        transport_protocol: tls
      transport_socket:
        name: envoy.transport_sockets.tls
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
          common_tls_context:
%s
            tls_certificate_sds_secret_configs:
            - name: %q
              sds_config:
                resource_api_version: V3
                api_config_source:
                  api_type: GRPC
                  transport_api_version: V3
                  grpc_services:
                  - envoy_grpc:
                      cluster_name: sds_cluster
      filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: opensandbox_egress_tls
          route_config:
            name: opensandbox_egress_tls_routes
            virtual_hosts:
            - name: all_tls
              domains: ["*"]
              routes:
              - match: { prefix: "/" }
                route:
                  cluster: dynamic_forward_proxy_cluster
                  timeout: 0s
          http_filters:
          - name: envoy.filters.http.ext_proc
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
              grpc_service:
                envoy_grpc:
                  cluster_name: ext_proc_cluster
                timeout: 5s
              message_timeout: 5s
              processing_mode:
                request_header_mode: SEND
                response_header_mode: SKIP
                request_body_mode: NONE
                response_body_mode: NONE
                request_trailer_mode: SKIP
                response_trailer_mode: SKIP
          - name: envoy.filters.http.dynamic_forward_proxy
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig
              dns_cache_config:
                name: opensandbox_dynamic_forward_proxy_cache
                dns_lookup_family: V4_ONLY
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: opensandbox_egress
          route_config:
            name: opensandbox_egress_routes
            virtual_hosts:
            - name: all
              domains: ["*"]
              routes:
              - match: { prefix: "/" }
                route:
                  cluster: dynamic_forward_proxy_cluster
                  timeout: 0s
          http_filters:
          - name: envoy.filters.http.ext_proc
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
              grpc_service:
                envoy_grpc:
                  cluster_name: ext_proc_cluster
                timeout: 5s
              message_timeout: 5s
              processing_mode:
                request_header_mode: SEND
                response_header_mode: SKIP
                request_body_mode: NONE
                response_body_mode: NONE
                request_trailer_mode: SKIP
                response_trailer_mode: SKIP
          - name: envoy.filters.http.dynamic_forward_proxy
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig
              dns_cache_config:
                name: opensandbox_dynamic_forward_proxy_cache
                dns_lookup_family: V4_ONLY
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  clusters:
  - name: sds_cluster
    connect_timeout: 0.25s
    type: STATIC
    http2_protocol_options: {}
    load_assignment:
      cluster_name: sds_cluster
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: %s
                port_value: %s
  - name: ext_proc_cluster
    connect_timeout: 0.25s
    type: STATIC
    http2_protocol_options: {}
    load_assignment:
      cluster_name: ext_proc_cluster
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: %s
                port_value: %s
  - name: dynamic_forward_proxy_cluster
    connect_timeout: 5s
    lb_policy: CLUSTER_PROVIDED
    transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        common_tls_context:
          validation_context:
            trusted_ca:
              filename: /etc/ssl/certs/ca-certificates.crt
    typed_extension_protocol_options:
      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
        "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
        upstream_http_protocol_options:
          auto_sni: true
          auto_san_validation: true
        explicit_http_config:
          http_protocol_options:
            header_key_format:
              proper_case_words: {}
    cluster_type:
      name: envoy.clusters.dynamic_forward_proxy
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig
        dns_cache_config:
          name: opensandbox_dynamic_forward_proxy_cache
          dns_lookup_family: V4_ONLY
`, cfg.AdminPort, cfg.ListenPort, onDemandSelectorYAML(cfg), cfg.SDSSecret, host(cfg.SDSAddr), port(cfg.SDSAddr), host(cfg.ExtProcAddr), port(cfg.ExtProcAddr))
}

func onDemandSelectorYAML(cfg BootstrapConfig) string {
	if !cfg.OnDemandSDS {
		return ""
	}
	return fmt.Sprintf(`            custom_tls_certificate_selector:
              name: on-demand
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.cert_selectors.on_demand_secret.v3.Config
                config_source:
                  api_config_source:
                    api_type: DELTA_GRPC
                    transport_api_version: V3
                    grpc_services:
                    - envoy_grpc:
                        cluster_name: sds_cluster
                certificate_mapper:
                  name: sni
                  typed_config:
                    "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.cert_mappers.sni.v3.SNI
                    default_value: %q
                prefetch_secret_names:
                - %q`, cfg.SDSSecret, cfg.SDSSecret)
}

func host(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

func port(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return "19001"
}
