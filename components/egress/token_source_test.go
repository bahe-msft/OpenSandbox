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

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/alibaba/opensandbox/egress/pkg/constants"
	"github.com/stretchr/testify/require"
)

func TestFileEgressTokenSourceReadsCurrentToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0o600))

	source := fileEgressTokenSource{path: path}
	require.True(t, source.Configured())
	require.Equal(t, "first", source.Token())

	require.NoError(t, os.WriteFile(path, []byte("second\n"), 0o600))
	require.Equal(t, "second", source.Token())
}

func TestPolicyServerAuthorizeUsesTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o600))
	srv := &policyServer{tokenSource: fileEgressTokenSource{path: path}}

	req := httptest.NewRequest(http.MethodGet, "/policy", nil)
	req.Header.Set(constants.EgressAuthTokenHeader, "first")
	require.True(t, srv.authorize(req))

	require.NoError(t, os.WriteFile(path, []byte("second"), 0o600))
	require.False(t, srv.authorize(req))

	req.Header.Set(constants.EgressAuthTokenHeader, "second")
	require.True(t, srv.authorize(req))
}

func TestPolicyServerAuthorizeConfiguredMissingTokenFileFailsClosed(t *testing.T) {
	srv := &policyServer{tokenSource: fileEgressTokenSource{path: filepath.Join(t.TempDir(), "missing")}}
	req := httptest.NewRequest(http.MethodGet, "/policy", nil)
	req.Header.Set(constants.EgressAuthTokenHeader, "anything")

	require.False(t, srv.authorize(req))
}
