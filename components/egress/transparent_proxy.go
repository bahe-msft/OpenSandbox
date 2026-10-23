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

func (s *policyServer) handleTransparentPolicy(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.effectivePolicy()
	origins := []string{}
	if current.TransparentProxy != nil {
		for _, origin := range current.TransparentProxy.Origins {
			host := strings.TrimPrefix(origin, "https://")
			if current.Evaluate(host) == policy.ActionAllow {
				origins = append(origins, origin)
			}
		}
	}
	writeJSON(w, http.StatusOK, policy.TransparentProxyPolicy{Origins: origins})
}
