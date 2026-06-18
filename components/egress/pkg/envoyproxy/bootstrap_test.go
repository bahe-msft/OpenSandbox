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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBootstrapYAMLIncludesMultipleDownstreamCertificates(t *testing.T) {
	yaml := BootstrapYAML(BootstrapConfig{
		ListenPort:  18082,
		AdminPort:   19000,
		ExtProcAddr: "127.0.0.1:19001",
		Certificates: []CertificateConfig{
			{CertPath: "/tmp/dev.azure.com.crt", KeyPath: "/tmp/dev.azure.com.key"},
			{CertPath: "/tmp/example.com.crt", KeyPath: "/tmp/example.com.key"},
		},
	})

	require.Equal(t, 2, strings.Count(yaml, "certificate_chain:"))
	require.Contains(t, yaml, `filename: "/tmp/dev.azure.com.crt"`)
	require.Contains(t, yaml, `filename: "/tmp/dev.azure.com.key"`)
	require.Contains(t, yaml, `filename: "/tmp/example.com.crt"`)
	require.Contains(t, yaml, `filename: "/tmp/example.com.key"`)
}
