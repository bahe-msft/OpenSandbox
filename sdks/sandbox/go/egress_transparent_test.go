package opensandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetPolicyCarriesTransparentOrigins(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/policy" || request.Header.Get(egressAuthHeader) != "management" {
			t.Error("incorrect policy transport")
		}
		var policy NetworkPolicy
		if err := json.NewDecoder(request.Body).Decode(&policy); err != nil {
			t.Error(err)
		}
		if policy.TransparentProxy == nil || len(policy.TransparentProxy.Origins) != 1 || policy.TransparentProxy.Origins[0] != "https://github.com" {
			t.Error("transparent origin lost in serialization")
		}
		_ = json.NewEncoder(w).Encode(PolicyStatusResponse{Policy: &policy})
	}))
	defer server.Close()
	client := NewEgressClient(server.URL, "management")
	state, err := client.SetPolicy(context.Background(), NetworkPolicy{
		DefaultAction:    "deny",
		TransparentProxy: &TransparentProxyPolicy{Origins: []string{"https://github.com"}},
	})
	if err != nil || state.Policy.TransparentProxy == nil {
		t.Fatalf("transparent policy unavailable: %v", err)
	}
}
