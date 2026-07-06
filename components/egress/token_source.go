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

package main

import (
	"os"
	"strings"
)

type egressTokenSource interface {
	Token() string
	Configured() bool
}

type staticEgressTokenSource struct {
	token string
}

func (s staticEgressTokenSource) Token() string {
	return strings.TrimSpace(s.token)
}

func (s staticEgressTokenSource) Configured() bool {
	return strings.TrimSpace(s.token) != ""
}

type fileEgressTokenSource struct {
	path string
}

func (s fileEgressTokenSource) Token() string {
	if strings.TrimSpace(s.path) == "" {
		return ""
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s fileEgressTokenSource) Configured() bool {
	return strings.TrimSpace(s.path) != ""
}

type fallbackEgressTokenSource struct {
	primary  egressTokenSource
	fallback egressTokenSource
}

func (s fallbackEgressTokenSource) Token() string {
	if s.primary != nil {
		if token := s.primary.Token(); token != "" {
			return token
		}
	}
	if s.fallback != nil {
		return s.fallback.Token()
	}
	return ""
}

func (s fallbackEgressTokenSource) Configured() bool {
	return (s.primary != nil && s.primary.Configured()) || (s.fallback != nil && s.fallback.Configured())
}

func newEgressTokenSource(token, tokenFile string) egressTokenSource {
	fileSource := fileEgressTokenSource{path: strings.TrimSpace(tokenFile)}
	staticSource := staticEgressTokenSource{token: token}
	if fileSource.Configured() {
		return fallbackEgressTokenSource{primary: fileSource, fallback: staticSource}
	}
	return staticSource
}
