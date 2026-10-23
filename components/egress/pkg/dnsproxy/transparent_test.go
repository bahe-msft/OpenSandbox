package dnsproxy

import (
	"testing"

	"github.com/miekg/dns"

	"github.com/alibaba/opensandbox/egress/pkg/nftables"
	"github.com/alibaba/opensandbox/egress/pkg/policy"
)

func TestWildcardDNSReturnsSyntheticAddressAndRevokesWithoutPublicLookup(t *testing.T) {
	t.Parallel()
	pol, err := policy.ParsePolicy(`{"defaultAction":"deny","transparentProxy":{"origins":["https://*.kusto.windows.net"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &Proxy{userPolicy: pol, effectivePolicy: pol}
	proxy.onResolved = func(string, []nftables.ResolvedIP) {
		t.Error("wildcard interception admitted a network address")
	}
	query := new(dns.Msg)
	query.SetQuestion("cluster.westus2.kusto.windows.net.", dns.TypeA)
	writer := &fakeRespWriter{remote: addrFromIP("192.0.2.10")}
	proxy.serveDNS(writer, query)
	if len(writer.msgs) != 1 || len(writer.msgs[0].Answer) != 1 || writer.msgs[0].Answer[0].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatal("wildcard DNS returned a missing or public address")
	}

	proxy.UpdatePolicy(policy.DefaultDenyPolicy())
	writer = &fakeRespWriter{remote: addrFromIP("192.0.2.10")}
	proxy.serveDNS(writer, query)
	if writer.msgs[0].Rcode != dns.RcodeNameError {
		t.Fatal("removed wildcard still resolved")
	}
}

func TestTransparentDNSReturnsOnlyInterceptionAnswers(t *testing.T) {
	t.Parallel()
	pol, err := policy.ParsePolicy(`{"defaultAction":"deny","transparentProxy":{"origins":["https://github.com"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &Proxy{userPolicy: pol, effectivePolicy: pol}
	proxy.onResolved = func(string, []nftables.ResolvedIP) {
		t.Error("interception address entered network allow sets")
	}
	for _, kind := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeHTTPS} {
		writer := &fakeRespWriter{remote: addrFromIP("192.0.2.10")}
		query := new(dns.Msg)
		query.SetQuestion("github.com.", kind)
		proxy.serveDNS(writer, query)
		if len(writer.msgs) != 1 || writer.msgs[0].Rcode != dns.RcodeSuccess {
			t.Fatal("admitted DNS query failed")
		}
		if kind == dns.TypeA {
			if len(writer.msgs[0].Answer) != 1 || writer.msgs[0].Answer[0].(*dns.A).A.String() != "192.0.2.1" {
				t.Fatal("public or missing address returned for mapped origin")
			}
		} else if len(writer.msgs[0].Answer) != 0 {
			t.Fatal("unsupported address type escaped through public DNS")
		}
	}
	proxy.UpdatePolicy(policy.DefaultDenyPolicy())
	writer := &fakeRespWriter{remote: addrFromIP("192.0.2.10")}
	query := new(dns.Msg)
	query.SetQuestion("github.com.", dns.TypeA)
	proxy.serveDNS(writer, query)
	if writer.msgs[0].Rcode != dns.RcodeNameError {
		t.Fatal("removed origin still resolved")
	}
}
