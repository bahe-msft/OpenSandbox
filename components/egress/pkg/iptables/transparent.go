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

package iptables

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/alibaba/opensandbox/egress/pkg/log"
)

func transparentHTTPRules(localPort int, mitmUID uint32, op string) [][]string {
	target := strconv.Itoa(localPort)
	uid := strconv.FormatUint(uint64(mitmUID), 10)
	loopRules := [][]string{
		{"iptables", "-t", "nat", op, "OUTPUT", "-p", "tcp", "-d", "127.0.0.0/8", "-j", "RETURN"},
	}
	redir := [][]string{
		{
			"iptables", "-t", "nat", op, "OUTPUT", "-p", "tcp",
			"-m", "owner", "!", "--uid-owner", uid,
			"-m", "multiport", "--dports", "80,443",
			"-j", "REDIRECT", "--to-ports", target,
		},
	}
	return append(loopRules, redir...)
}

func transparentHTTPHostRules(localPort int, mitmUID uint32, addrs []netip.Addr, op string) [][]string {
	target := strconv.Itoa(localPort)
	uid := strconv.FormatUint(uint64(mitmUID), 10)
	rules := [][]string{
		{"iptables", "-t", "nat", op, "OUTPUT", "-p", "tcp", "-d", "127.0.0.0/8", "-j", "RETURN"},
	}
	for _, addr := range addrs {
		if !addr.Is4() {
			continue
		}
		rules = append(rules, []string{
			"iptables", "-t", "nat", op, "OUTPUT", "-p", "tcp",
			"-d", addr.String(),
			"-m", "owner", "!", "--uid-owner", uid,
			"-m", "multiport", "--dports", "80,443",
			"-j", "REDIRECT", "--to-ports", target,
		})
	}
	return rules
}

// SetupTransparentHTTP: non-mitm UIDs get OUTPUT tcp:80,443 → localPort; loopback and mitm’s traffic excluded.
func SetupTransparentHTTP(localPort int, mitmUID uint32) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("iptables transparent: only supported on linux")
	}

	if localPort <= 0 {
		return fmt.Errorf("iptables transparent: invalid port or uid")
	}
	target := strconv.Itoa(localPort)
	uid := strconv.FormatUint(uint64(mitmUID), 10)
	log.Infof("installing iptables transparent: OUTPUT tcp dport 80,443 -> 127.0.0.1:%s (skip uid %s)", target, uid)

	rules := transparentHTTPRules(localPort, mitmUID, "-A")
	for _, args := range rules {
		if output, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("iptables transparent: %v (output: %s)", err, output)
		}
	}
	log.Infof("iptables transparent rules installed successfully")
	return nil
}

func SetupTransparentHTTPForHosts(localPort int, mitmUID uint32, hosts []string) ([]netip.Addr, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("iptables transparent: only supported on linux")
	}
	if localPort <= 0 {
		return nil, fmt.Errorf("iptables transparent: invalid port or uid")
	}
	addrs, err := resolveHosts(hosts)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("iptables transparent: no IPv4 addresses resolved for %v", hosts)
	}
	log.Infof("installing iptables transparent: OUTPUT tcp dport 80,443 for %v -> 127.0.0.1:%d", addrs, localPort)
	for _, args := range transparentHTTPHostRules(localPort, mitmUID, addrs, "-A") {
		if output, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("iptables transparent: %v (output: %s)", err, output)
		}
	}
	log.Infof("iptables transparent host-scoped rules installed successfully")
	return addrs, nil
}

func RemoveTransparentHTTP(localPort int, mitmUID uint32) {
	if runtime.GOOS != "linux" {
		return
	}
	if localPort <= 0 {
		return
	}
	rules := transparentHTTPRules(localPort, mitmUID, "-D")
	for i := len(rules) - 1; i >= 0; i-- {
		args := rules[i]
		if output, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			log.Warnf("iptables transparent remove rule (ignored): %v (output: %s)", err, strings.TrimSpace(string(output)))
		}
	}
	log.Infof("iptables transparent rules removed")
}

func RemoveTransparentHTTPForAddrs(localPort int, mitmUID uint32, addrs []netip.Addr) {
	if runtime.GOOS != "linux" || localPort <= 0 {
		return
	}
	rules := transparentHTTPHostRules(localPort, mitmUID, addrs, "-D")
	for i := len(rules) - 1; i >= 0; i-- {
		args := rules[i]
		if output, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			log.Warnf("iptables transparent remove rule (ignored): %v (output: %s)", err, strings.TrimSpace(string(output)))
		}
	}
	log.Infof("iptables transparent host-scoped rules removed")
}

func resolveHosts(hosts []string) ([]netip.Addr, error) {
	seen := map[netip.Addr]struct{}{}
	var out []netip.Addr
	for _, host := range hosts {
		host = strings.TrimSpace(strings.TrimSuffix(host, "."))
		if host == "" {
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", host, err)
		}
		for _, ip := range ips {
			addr, ok := netip.AddrFromSlice(ip)
			if !ok || !addr.Is4() {
				continue
			}
			if _, exists := seen[addr]; exists {
				continue
			}
			seen[addr] = struct{}{}
			out = append(out, addr)
		}
	}
	return out, nil
}
