package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/alibaba/opensandbox/egress/pkg/constants"
	"github.com/alibaba/opensandbox/egress/pkg/policy"
)

func (s *policyServer) validateTransparentPolicy(pol *policy.NetworkPolicy) error {
	if pol == nil || pol.TransparentProxy == nil || len(pol.TransparentProxy.Origins) == 0 {
		return nil
	}
	if s.token == "" || !constants.IsTruthy(os.Getenv(constants.EnvMitmproxyTransparent)) {
		return fmt.Errorf("transparent forwarding requires authenticated policy management and mitmproxy")
	}
	if os.Getenv(constants.EnvEgressProfile) == constants.ProfileFastSandbox {
		return fmt.Errorf("transparent forwarding requires the sidecar profile")
	}
	gateway, err := url.Parse(os.Getenv(constants.EnvTransparentProxyGateway))
	if err != nil || gateway.Scheme != "http" || gateway.User != nil || !strings.HasSuffix(gateway.Hostname(), ".svc.cluster.local") {
		return fmt.Errorf("transparent forwarding requires a fixed internal HTTP Gateway")
	}
	if gateway.Port() != "" && gateway.Port() != "80" || gateway.Path != "" || gateway.RawQuery != "" || gateway.Fragment != "" {
		return fmt.Errorf("transparent Gateway must use port 80 without a path, query, or fragment")
	}
	if pol.TransparentHost(gateway.Hostname()) {
		return fmt.Errorf("transparent Gateway must not be an intercepted origin")
	}
	if !filepath.IsAbs(os.Getenv(constants.EnvTransparentProxyTokenFile)) {
		return fmt.Errorf("transparent forwarding requires a sidecar-only identity file")
	}
	deny, allow := s.currentAlwaysRules()
	if policy.MergeAlwaysOverlay(pol, deny, allow).Evaluate(gateway.Hostname()) != policy.ActionAllow {
		return fmt.Errorf("transparent Gateway must be allowed by the network policy")
	}
	return nil
}

func (s *policyServer) handleTransparentPolicy(w http.ResponseWriter, request *http.Request) {
	hosts := request.URL.Query()["host"]
	if len(hosts) != 1 || hosts[0] == "" {
		http.Error(w, "one concrete hostname is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.effectivePolicy()
	origins := []string{}
	// Evaluate the concrete hostname so exact and partial-subtree denies also
	// apply to cached-address requests covered by a broader origin wildcard.
	if current.TransparentHost(hosts[0]) && current.Evaluate(hosts[0]) == policy.ActionAllow {
		origins = current.TransparentProxy.Origins
	}
	writeJSON(w, http.StatusOK, policy.TransparentProxyPolicy{Origins: origins})
}
