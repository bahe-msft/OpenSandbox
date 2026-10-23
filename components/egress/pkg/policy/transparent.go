package policy

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// TransparentProxyPolicy admits original origins without authorizing public IPs.
type TransparentProxyPolicy struct {
	Origins []string `json:"origins"`
}

func normalizeTransparentPolicy(p *NetworkPolicy) error {
	if p.TransparentProxy == nil {
		return nil
	}
	if len(p.TransparentProxy.Origins) > 128 {
		return fmt.Errorf("too many transparent origins")
	}
	if len(p.TransparentProxy.Origins) > 0 && p.DefaultAction != ActionDeny {
		return fmt.Errorf("transparent proxy requires default-deny policy")
	}
	origins := append([]string{}, p.TransparentProxy.Origins...)
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || origin != "https://"+parsed.Hostname() {
			return fmt.Errorf("transparent origins must be canonical HTTPS DNS origins")
		}
		if len(validation.IsDNS1123Subdomain(parsed.Hostname())) != 0 {
			return fmt.Errorf("invalid transparent origin hostname")
		}
		if _, err := netip.ParseAddr(parsed.Hostname()); err == nil {
			return fmt.Errorf("transparent origins must not use IP addresses")
		}
	}
	slices.Sort(origins)
	p.TransparentProxy = &TransparentProxyPolicy{Origins: slices.Compact(origins)}
	return nil
}

// TransparentHost does not imply network permission to the original server.
func (p *NetworkPolicy) TransparentHost(host string) bool {
	if p == nil || p.TransparentProxy == nil {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	_, found := slices.BinarySearch(p.TransparentProxy.Origins, "https://"+host)
	return found
}
