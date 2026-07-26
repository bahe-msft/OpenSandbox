// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package opensandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientLifecycleFlow(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	pauseCalled := false
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "secret", r.Header.Get("OPEN-SANDBOX-API-KEY"))
		switch r.URL.Path {
		case "/sandboxes/s1/endpoints/44772":
			require.Equal(t, "true", r.URL.Query().Get("use_server_proxy"))
			fmt.Fprintf(w, `{"endpoint":%q,"headers":{"OpenSandbox-Ingress-To":"s1-44772"}}`, server.URL+"/execd")
		case "/execd/v1/activity":
			require.Equal(t, "s1-44772", r.Header.Get("OpenSandbox-Ingress-To"))
			fmt.Fprint(w, `{"last_activity_at":"2026-07-26T20:00:00Z","observed_at":"2026-07-26T21:00:00Z","busy":false,"active_operations":0,"revision":4}`)
		case "/sandboxes/s1/pause":
			require.Equal(t, http.MethodPost, r.Method)
			pauseCalled = true
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", time.Second)
	require.NoError(t, err)
	endpoint, err := client.ResolveExecdEndpoint(context.Background(), "s1")
	require.NoError(t, err)
	snapshot, err := client.Activity(context.Background(), endpoint)
	require.NoError(t, err)
	require.EqualValues(t, 4, snapshot.Revision)
	require.NoError(t, client.Pause(context.Background(), "s1"))
	require.True(t, pauseCalled)
}

func TestNormalizeEndpointUsesLifecycleScheme(t *testing.T) {
	t.Parallel()
	client, err := NewClient("http://opensandbox-server:80", "", time.Second)
	require.NoError(t, err)
	endpoint, err := client.normalizeEndpoint("opensandbox-server:80/sandboxes/s1/proxy/44772")
	require.NoError(t, err)
	require.Equal(t, "http://opensandbox-server:80/sandboxes/s1/proxy/44772", endpoint)
}
