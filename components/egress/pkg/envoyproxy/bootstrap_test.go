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

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBootstrapYAMLUsesSDSForDownstreamCertificate(t *testing.T) {
	yaml := BootstrapYAML(BootstrapConfig{
		ListenPort:  18082,
		AdminPort:   19000,
		ExtProcAddr: "127.0.0.1:19001",
		SDSAddr:     "127.0.0.1:19002",
		SDSSecret:   "opensandbox_downstream_mitm",
	})

	require.Contains(t, yaml, "tls_certificate_sds_secret_configs:")
	require.Contains(t, yaml, "name: \"opensandbox_downstream_mitm\"")
	require.Contains(t, yaml, "cluster_name: sds_cluster")
	require.Contains(t, yaml, "port_value: 19002")
}

func TestBootstrapYAMLCanEnableOnDemandSDS(t *testing.T) {
	yaml := BootstrapYAML(BootstrapConfig{
		ListenPort:  18082,
		AdminPort:   19000,
		ExtProcAddr: "127.0.0.1:19001",
		SDSAddr:     "127.0.0.1:19002",
		SDSSecret:   "default_host",
		OnDemandSDS: true,
	})

	require.Contains(t, yaml, "custom_tls_certificate_selector:")
	require.Contains(t, yaml, "cert_selectors.on_demand_secret.v3.Config")
	require.Contains(t, yaml, "cert_mappers.sni.v3.SNI")
	require.Contains(t, yaml, "api_type: DELTA_GRPC")
}
