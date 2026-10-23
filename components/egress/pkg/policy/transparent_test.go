package policy

import "testing"

func TestTransparentOriginsDoNotBecomeNetworkRules(t *testing.T) {
	t.Parallel()
	p, err := ParsePolicy(`{"defaultAction":"deny","egress":[{"action":"allow","target":"gateway.system.svc.cluster.local"}],"transparentProxy":{"origins":["https://github.com"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.TransparentHost("GitHub.com.") || p.Evaluate("github.com") != ActionAllow {
		t.Fatal("admitted origin is not available for interception")
	}
	if len(p.Egress) != 1 || p.Egress[0].Target != "gateway.system.svc.cluster.local" {
		t.Fatal("public upstream became an ordinary network destination")
	}
	deny, err := ParseValidatedEgressRule(ActionDeny, "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if MergeAlwaysOverlay(p, []EgressRule{deny}, nil).Evaluate("github.com") != ActionDeny {
		t.Fatal("transparent origin bypassed an operator deny")
	}
}

func TestTransparentWildcardMatchesSubdomainsWithoutNetworkAdmission(t *testing.T) {
	t.Parallel()
	p, err := ParsePolicy(`{"defaultAction":"deny","transparentProxy":{"origins":["https://*.kusto.windows.net"]}}`)
	if err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"cluster.kusto.windows.net", "Cluster.Westus2.Kusto.Windows.Net."} {
		if !p.TransparentHost(host) {
			t.Fatalf("subdomain did not match: %s", host)
		}
	}
	for _, host := range []string{"kusto.windows.net", "notkusto.windows.net", "cluster.kusto.windows.net.evil.example", "*.kusto.windows.net", "cluster..kusto.windows.net"} {
		if p.TransparentHost(host) {
			t.Fatalf("invalid subdomain matched: %s", host)
		}
	}
	if len(p.Egress) != 0 {
		t.Fatal("wildcard became an ordinary network rule")
	}
}

func TestTransparentOriginsRequireCanonicalHTTPSAndDefaultDeny(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"defaultAction":"allow","transparentProxy":{"origins":["https://github.com"]}}`,
		`{"transparentProxy":{"origins":["http://github.com"]}}`,
		`{"transparentProxy":{"origins":["https://github.com:443"]}}`,
		`{"transparentProxy":{"origins":["https://127.0.0.1"]}}`,
		`{"transparentProxy":{"origins":["https://github.com/path"]}}`,
		`{"transparentProxy":{"origins":["https://*"]}}`,
		`{"transparentProxy":{"origins":["https://*kusto.windows.net"]}}`,
		`{"transparentProxy":{"origins":["https://foo.*.windows.net"]}}`,
		`{"transparentProxy":{"origins":["https://*.*.windows.net"]}}`,
		`{"transparentProxy":{"origins":["https://*.127.0.0.1"]}}`,
		`{"transparentProxy":{"origins":["https://*.kusto.windows.net:443"]}}`,
	} {
		if _, err := ParsePolicy(raw); err == nil {
			t.Fatal("invalid transparent policy accepted")
		}
	}
}
