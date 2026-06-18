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

	"github.com/stretchr/testify/require"

	"github.com/alibaba/opensandbox/egress/pkg/credentialvault"
)

func TestSelectBinding(t *testing.T) {
	snapshot := credentialvault.ActiveSnapshot{
		Revision: 1,
		Bindings: []credentialvault.ActiveBinding{
			{
				Name: "wildcard",
				Match: credentialvault.Match{
					Schemes: []string{"https"},
					Ports:   []int{443},
					Hosts:   []string{"*.example.com"},
					Methods: []string{"GET"},
					Paths:   []string{"/*"},
				},
			},
			{
				Name: "exact",
				Match: credentialvault.Match{
					Schemes: []string{"https"},
					Ports:   []int{443},
					Hosts:   []string{"api.example.com"},
					Methods: []string{"GET"},
					Paths:   []string{"/v1/*"},
				},
			},
		},
	}

	req := parseRequestInfo(map[string]string{
		":scheme":    "https",
		":authority": "api.example.com",
		":method":    "GET",
		":path":      "/v1/projects?x=1",
	})

	binding := selectBinding(req, snapshot)
	require.NotNil(t, binding)
	require.Equal(t, "exact", binding.Name)
}
