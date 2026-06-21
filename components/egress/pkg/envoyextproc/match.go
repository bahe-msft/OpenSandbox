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

package envoyextproc

import (
	"net"
	"strconv"
	"strings"

	"github.com/alibaba/opensandbox/egress/pkg/credentialvault"
)

type requestInfo struct {
	scheme string
	host   string
	port   int
	method string
	path   string
}

func parseRequestInfo(headers map[string]string) requestInfo {
	host := header(headers, ":authority")
	if host == "" {
		host = header(headers, "host")
	}
	port := 0
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		port, _ = strconv.Atoi(p)
	}
	scheme := strings.ToLower(header(headers, ":scheme"))
	if scheme == "" {
		scheme = "https"
	}
	if port == 0 {
		if scheme == "http" {
			port = 80
		} else {
			port = 443
		}
	}
	path := header(headers, ":path")
	if path == "" {
		path = "/"
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" {
		path = "/"
	}
	return requestInfo{
		scheme: scheme,
		host:   strings.TrimSuffix(strings.ToLower(host), "."),
		port:   port,
		method: strings.ToUpper(header(headers, ":method")),
		path:   path,
	}
}

func selectBinding(req requestInfo, snapshot credentialvault.ActiveSnapshot) *credentialvault.ActiveBinding {
	type candidate struct {
		precedence int
		binding    *credentialvault.ActiveBinding
	}
	var matches []candidate
	for i := range snapshot.Bindings {
		binding := &snapshot.Bindings[i]
		ok, precedence := bindingMatches(req, binding)
		if ok {
			matches = append(matches, candidate{precedence: precedence, binding: binding})
		}
	}
	if len(matches) == 0 {
		return nil
	}
	best := matches[0]
	ambiguous := false
	for _, m := range matches[1:] {
		if m.precedence > best.precedence {
			best = m
			ambiguous = false
			continue
		}
		if m.precedence == best.precedence {
			ambiguous = true
		}
	}
	if ambiguous {
		return nil
	}
	return best.binding
}

func bindingMatches(req requestInfo, binding *credentialvault.ActiveBinding) (bool, int) {
	match := binding.Match
	if len(match.Schemes) > 0 && !stringIn(req.scheme, match.Schemes) {
		return false, 0
	}
	if len(match.Ports) > 0 && !intIn(req.port, match.Ports) {
		return false, 0
	}
	if len(match.Methods) > 0 && !stringIn(req.method, upperStrings(match.Methods)) {
		return false, 0
	}
	if len(match.Paths) > 0 && !pathMatchesAny(req.path, match.Paths) {
		return false, 0
	}
	best := 0
	for _, pattern := range match.Hosts {
		if ok, precedence := hostMatches(req.host, pattern); ok && precedence > best {
			best = precedence
		}
	}
	return best > 0, best
}

func header(headers map[string]string, key string) string {
	return headers[strings.ToLower(key)]
}

func hostMatches(host, pattern string) (bool, int) {
	pattern = strings.TrimSuffix(strings.ToLower(pattern), ".")
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:]
		apex := pattern[2:]
		return strings.HasSuffix(host, suffix) && host != apex, 1
	}
	return host == pattern, 2
}

func pathMatchesAny(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(path, strings.TrimSuffix(pattern, "*")) {
				return true
			}
			continue
		}
		if path == pattern {
			return true
		}
	}
	return false
}

func stringIn(s string, list []string) bool {
	for _, item := range list {
		if s == item {
			return true
		}
	}
	return false
}

func intIn(n int, list []int) bool {
	for _, item := range list {
		if n == item {
			return true
		}
	}
	return false
}

func upperStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToUpper(s))
	}
	return out
}
