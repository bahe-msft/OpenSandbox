package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alibaba/opensandbox/egress/pkg/constants"
	"github.com/alibaba/opensandbox/egress/pkg/policy"
)

func TestTransparentPolicyRuntimeUpdateAndRemoval(t *testing.T) {
	t.Setenv(constants.EnvMitmproxyTransparent, "true")
	t.Setenv(constants.EnvTransparentProxyGateway, "http://gateway.system.svc.cluster.local")
	t.Setenv(constants.EnvTransparentProxyTokenFile, "/var/run/identity/token")
	proxy := &stubProxy{updated: policy.DefaultDenyPolicy()}
	server := &policyServer{proxy: proxy, token: "management"}
	body := `{"defaultAction":"deny","egress":[{"action":"allow","target":"gateway.system.svc.cluster.local"}],"transparentProxy":{"origins":["https://github.com"]}}`
	request := httptest.NewRequest(http.MethodPost, "/policy", strings.NewReader(body))
	request.Header.Set(constants.EgressAuthTokenHeader, "management")
	response := httptest.NewRecorder()
	server.handlePolicy(response, request)
	if response.Code != http.StatusOK || !proxy.updated.TransparentHost("github.com") {
		t.Fatalf("runtime origins were not applied: %d", response.Code)
	}
	merged, err := patchMergedPolicy(proxy.updated, []policy.EgressRule{{Action: "allow", Target: "other.system.svc.cluster.local"}})
	if err != nil || !merged.TransparentHost("github.com") {
		t.Fatal("network-only patch removed transparent origins", err)
	}
	private := httptest.NewRecorder()
	server.handleTransparentPolicy(private, httptest.NewRequest(http.MethodGet, "/policy/_active?host=github.com", nil))
	var view policy.TransparentProxyPolicy
	if err := json.Unmarshal(private.Body.Bytes(), &view); err != nil || len(view.Origins) != 1 {
		t.Fatal("accepted private policy unavailable", err)
	}
	request = httptest.NewRequest(http.MethodPost, "/policy", strings.NewReader(`{"defaultAction":"deny","transparentProxy":{"origins":[]}}`))
	request.Header.Set(constants.EgressAuthTokenHeader, "management")
	response = httptest.NewRecorder()
	server.handlePolicy(response, request)
	if response.Code != http.StatusOK || proxy.updated.TransparentHost("github.com") {
		t.Fatal("origin removal was not applied")
	}
}

func TestTransparentPrivateViewEvaluatesConcreteWildcardHost(t *testing.T) {
	t.Parallel()
	pol, err := policy.ParsePolicy(`{"defaultAction":"deny","transparentProxy":{"origins":["https://*.kusto.windows.net"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	server := &policyServer{proxy: &stubProxy{updated: pol}}
	var denies []policy.EgressRule
	for _, target := range []string{"blocked.westus2.kusto.windows.net", "*.eastus2.kusto.windows.net"} {
		rule, err := policy.ParseValidatedEgressRule(policy.ActionDeny, target)
		if err != nil {
			t.Fatal(err)
		}
		denies = append(denies, rule)
	}
	server.setAlwaysRules(denies, nil)

	for _, test := range []struct {
		host    string
		allowed bool
	}{
		{host: "cluster.westus2.kusto.windows.net", allowed: true},
		{host: "blocked.westus2.kusto.windows.net"},
		{host: "cluster.eastus2.kusto.windows.net"},
		{host: "kusto.windows.net"},
		{host: "*.kusto.windows.net"},
	} {
		response := httptest.NewRecorder()
		server.handleTransparentPolicy(response, httptest.NewRequest(http.MethodGet, "/policy/_active?host="+test.host, nil))
		var view policy.TransparentProxyPolicy
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if (len(view.Origins) > 0) != test.allowed {
			t.Fatalf("concrete-host policy differs for %s", test.host)
		}
	}

	response := httptest.NewRecorder()
	server.handleTransparentPolicy(response, httptest.NewRequest(http.MethodGet, "/policy/_active", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatal("missing concrete hostname accepted")
	}
}

func TestTransparentPolicyRequiresTrustedRuntimeConfiguration(t *testing.T) {
	t.Setenv(constants.EnvMitmproxyTransparent, "true")
	pol, err := policy.ParsePolicy(`{"defaultAction":"deny","transparentProxy":{"origins":["https://github.com"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	server := &policyServer{proxy: &stubProxy{}, token: "management"}
	if err := server.validateTransparentPolicy(pol); err == nil {
		t.Fatal("forwarding without a fixed Gateway was accepted")
	}
}
