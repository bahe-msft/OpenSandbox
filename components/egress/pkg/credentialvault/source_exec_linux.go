//go:build linux

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

package credentialvault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	credentialproviderv1 "github.com/alibaba/opensandbox/egress/pkg/credentialprovider/v1"
	"google.golang.org/protobuf/proto"
)

const (
	DefaultCredentialProviderDir = "/usr/libexec/opensandbox/credential-providers"
	maxProviderOutputBytes       = 64 << 10
	providerTimeout              = time.Second
)

var (
	providerTypePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	providerSlots       = make(chan struct{}, 16)
)

type execCredentialSource struct {
	pluginName string
	path       string
	args       []string

	mu         sync.Mutex
	cached     string
	cacheUntil time.Time
}

func (s *execCredentialSource) Type() string  { return "plugin" }
func (s *execCredentialSource) Dynamic() bool { return true }

func (s *execCredentialSource) Resolve(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.cached != "" && time.Now().Before(s.cacheUntil) {
		value := s.cached
		s.mu.Unlock()
		return value, nil
	}
	s.mu.Unlock()

	select {
	case providerSlots <- struct{}{}:
		defer func() { <-providerSlots }()
	case <-ctx.Done():
		return "", fmt.Errorf("credential provider unavailable")
	}

	request, err := proto.Marshal(&credentialproviderv1.CredentialRequest{
		ApiVersion: credentialproviderv1.APIVersion,
		PluginName: s.pluginName,
	})
	if err != nil {
		return "", fmt.Errorf("credential provider request failed")
	}

	providerCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	cmd := exec.CommandContext(providerCtx, s.path, s.args...)
	cmd.Stdin = bytes.NewReader(request)
	cmd.Stderr = io.Discard
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("credential provider unavailable")
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("credential provider unavailable")
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxProviderOutputBytes+1))
	if len(output) > maxProviderOutputBytes {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil || len(output) == 0 || len(output) > maxProviderOutputBytes {
		return "", fmt.Errorf("credential provider unavailable")
	}

	var response credentialproviderv1.CredentialResponse
	if err := proto.Unmarshal(output, &response); err != nil {
		return "", fmt.Errorf("credential provider returned an invalid response")
	}
	if response.ApiVersion != credentialproviderv1.APIVersion {
		return "", fmt.Errorf("credential provider returned an unsupported API version")
	}
	if response.Status != credentialproviderv1.CredentialResponse_SUCCESS {
		return "", fmt.Errorf("credential provider did not return a credential")
	}
	if len(response.Credential) == 0 || len(response.Credential) > maxProviderOutputBytes {
		return "", fmt.Errorf("credential provider returned an invalid credential")
	}

	value, err := renderProviderCredential(response.Credential, response.Encoding)
	if err != nil {
		return "", err
	}

	if response.ExpiresAt != nil {
		expiresAt := response.ExpiresAt.AsTime()
		if err := response.ExpiresAt.CheckValid(); err != nil || !expiresAt.After(time.Now()) {
			return "", fmt.Errorf("credential provider returned an expired credential")
		}
		if response.Cacheable {
			// Do not serve a cached credential at the edge of its validity window.
			cacheUntil := expiresAt.Add(-5 * time.Second)
			if cacheUntil.After(time.Now()) {
				s.mu.Lock()
				s.cached = value
				s.cacheUntil = cacheUntil
				s.mu.Unlock()
			}
		}
	}
	return value, nil
}

func renderProviderCredential(value []byte, encoding credentialproviderv1.CredentialResponse_Encoding) (string, error) {
	switch encoding {
	case credentialproviderv1.CredentialResponse_UTF8:
		if !utf8.Valid(value) || bytes.IndexByte(value, 0) >= 0 || bytes.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("credential provider returned invalid UTF-8 credential data")
		}
		return string(value), nil
	case credentialproviderv1.CredentialResponse_BASE64:
		return base64.StdEncoding.EncodeToString(value), nil
	case credentialproviderv1.CredentialResponse_BASE64URL:
		return base64.RawURLEncoding.EncodeToString(value), nil
	default:
		return "", fmt.Errorf("credential provider returned an unsupported encoding")
	}
}

// RegisterExecCredentialProviders registers the fixed "plugin" source type.
// Its value selects an owner-controlled executable basename from dir.
func RegisterExecCredentialProviders(registry *SourceRegistry, dir, rawConfig string) error {
	providers := make(map[string]string)
	dirInfo, err := os.Lstat(dir)
	if err == nil {
		dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
		if !ok || !dirInfo.IsDir() || dirInfo.Mode().Perm()&0o022 != 0 || int(dirStat.Uid) != os.Geteuid() {
			return fmt.Errorf("credential provider directory must be owner-owned and not group/world-writable")
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return fmt.Errorf("read credential provider directory: %w", readErr)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !providerTypePattern.MatchString(name) {
				return fmt.Errorf("invalid credential provider executable name %q", name)
			}
			path := filepath.Join(dir, name)
			info, inspectErr := os.Lstat(path)
			if inspectErr != nil {
				return fmt.Errorf("inspect credential provider %q: %w", name, inspectErr)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.Mode().IsRegular() || info.Mode()&0o100 == 0 || info.Mode().Perm()&0o022 != 0 || int(stat.Uid) != os.Geteuid() {
				return fmt.Errorf("credential provider %q must be an owner-executable, owner-owned regular file that is not group/world-writable", name)
			}
			providers[name] = path
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect credential provider directory: %w", err)
	}

	argsByProvider := make(map[string][]string)
	if strings.TrimSpace(rawConfig) != "" {
		var configs []struct {
			Name string   `json:"name"`
			Args []string `json:"args"`
		}
		decoder := json.NewDecoder(strings.NewReader(rawConfig))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&configs); err != nil {
			return fmt.Errorf("parse credential provider configuration: %w", err)
		}
		for _, config := range configs {
			if !providerTypePattern.MatchString(config.Name) || len(config.Args) > 32 {
				return fmt.Errorf("invalid credential provider configuration")
			}
			if _, duplicate := argsByProvider[config.Name]; duplicate {
				return fmt.Errorf("duplicate credential provider configuration %q", config.Name)
			}
			if _, installed := providers[config.Name]; !installed {
				return fmt.Errorf("configured credential provider %q is not installed", config.Name)
			}
			for _, arg := range config.Args {
				if arg == "" || len(arg) > 4096 || strings.IndexByte(arg, 0) >= 0 {
					return fmt.Errorf("invalid argument for credential provider %q", config.Name)
				}
			}
			argsByProvider[config.Name] = append([]string(nil), config.Args...)
		}
	}

	registry.Register("plugin", func(raw json.RawMessage) (CredentialSource, error) {
		var source struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&source); err != nil {
			return nil, fmt.Errorf("parse plugin credential source: %w", err)
		}
		pluginName := strings.TrimSpace(source.Value)
		if source.Type != "plugin" || pluginName != source.Value || !providerTypePattern.MatchString(pluginName) {
			return nil, fmt.Errorf("invalid credential plugin name")
		}
		providerPath, ok := providers[pluginName]
		if !ok {
			return nil, fmt.Errorf("unsupported credential plugin %q", pluginName)
		}
		return &execCredentialSource{pluginName: pluginName, path: providerPath, args: append([]string(nil), argsByProvider[pluginName]...)}, nil
	})
	return nil
}
