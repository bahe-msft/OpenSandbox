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
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alibaba/opensandbox/egress/pkg/constants"
	"github.com/alibaba/opensandbox/egress/pkg/envoyproxy"
	"github.com/alibaba/opensandbox/egress/pkg/envoysds"
	"github.com/alibaba/opensandbox/egress/pkg/iptables"
	"github.com/alibaba/opensandbox/egress/pkg/log"
	"github.com/alibaba/opensandbox/egress/pkg/mitmcert"
	"github.com/alibaba/opensandbox/egress/pkg/mitmproxy"
	"github.com/alibaba/opensandbox/internal/safego"
)

// exitEvent carries an OnExit notification tagged with the generation of the
// mitmdump process that produced it. Generation tagging lets the watcher tell
// "the currently-running mitmdump just died" apart from "a half-launched
// attempt we already killed during a retry storm just finished reaping".
type exitEvent struct {
	gen uint64
	err error
}

type mitmTransparent struct {
	mu          sync.Mutex
	running     *mitmproxy.Running
	envoy       *envoyproxy.Running
	currentGen  uint64 // generation of the mitmdump currently considered live
	port        int
	uid         uint32
	gid         uint32
	addrs       []netip.Addr
	auth        *mitmcert.Authority
	sds         *envoysds.Server
	envoyCfg    envoyproxy.Config
	staticHosts []string
	mitmHosts   []string
	cfg         mitmproxy.Config // OnExit must NOT be set here; built per-Launch
	nextGen     uint64           // atomic; monotonic gen counter handed to each Launch
	restartCh   chan exitEvent
	shutdownCh  chan struct{} // closed by watchMitmproxy on ctx cancel; lets OnExit unblock during shutdown
}

func startTransparentHTTPProxyIfEnabled() (*mitmTransparent, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(constants.EnvHTTPProxyBackend)), "envoy") {
		return startEnvoyTransparentIfEnabled()
	}
	return startMitmproxyTransparentIfEnabled()
}

func (m *mitmTransparent) getRunning() *mitmproxy.Running {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *mitmTransparent) setRunning(r *mitmproxy.Running, gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = r
	m.currentGen = gen
}

func (m *mitmTransparent) getCurrentGen() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentGen
}

// launchTagged starts mitmdump with an OnExit closure that publishes the death
// of this specific process (identified by gen) into restartCh.
//
// The send is blocking with shutdownCh as the only escape: dropping an exit
// event while the watcher is still running can leave egress in a silent dead
// state (the watcher would never see the death and never trigger a restart).
// Stale events from killed half-launched attempts are still cheap to discard
// downstream via the gen check in watchMitmproxy; we just must not lose them
// in transit. Shutdown is the only legitimate reason to give up on a send,
// and we log a warning when that happens so the drop is observable.
func launchTagged(cfg mitmproxy.Config, restartCh chan<- exitEvent, shutdownCh <-chan struct{}, gen uint64) (*mitmproxy.Running, error) {
	cfg.OnExit = func(err error) {
		select {
		case restartCh <- exitEvent{gen: gen, err: err}:
		case <-shutdownCh:
			log.Warnf("[mitmproxy] dropping exit event during shutdown (gen=%d): %v", gen, err)
		}
	}
	return mitmproxy.Launch(cfg)
}

// startMitmproxyTransparentIfEnabled starts mitmdump in transparent mode, waits for the listener, and installs OUTPUT REDIRECT, then syncs the CA.
func startMitmproxyTransparentIfEnabled() (*mitmTransparent, error) {
	if !constants.IsTruthy(os.Getenv(constants.EnvMitmproxyTransparent)) {
		return nil, nil
	}

	mpPort := constants.EnvIntOrDefault(constants.EnvMitmproxyPort, constants.DefaultMitmproxyPort)
	mpUID, _, mpHome, err := mitmproxy.LookupUser(mitmproxy.RunAsUser)
	if err != nil {
		return nil, fmt.Errorf("lookup user %q: %w (ensure this user exists in the image)", mitmproxy.RunAsUser, err)
	}

	cfg := mitmproxy.Config{
		ListenPort: mpPort,
		UserName:   mitmproxy.RunAsUser,
		ScriptPath: strings.TrimSpace(os.Getenv(constants.EnvMitmproxyScript)),
	}
	// Buffer absorbs OnExit events from a retry storm so OnExit goroutines
	// don't all park waiting for the watcher to drain. Correctness does not
	// depend on the size: launchTagged uses a blocking send with shutdownCh
	// as the only escape, so events cannot be silently dropped while the
	// watcher is alive.
	restartCh := make(chan exitEvent, 64)
	shutdownCh := make(chan struct{})
	const initialGen uint64 = 1
	running, err := launchTagged(cfg, restartCh, shutdownCh, initialGen)
	if err != nil {
		return nil, fmt.Errorf("start mitmdump: %w", err)
	}

	waitAddr := fmt.Sprintf("127.0.0.1:%d", mpPort)
	if err := mitmproxy.WaitListenPort(waitAddr, 15*time.Second); err != nil {
		return nil, fmt.Errorf("wait listen %s: %w", waitAddr, err)
	}
	if err := iptables.SetupTransparentHTTP(mpPort, mpUID); err != nil {
		return nil, fmt.Errorf("iptables transparent: %w", err)
	}
	log.Infof("mitmproxy: transparent intercept active (OUTPUT tcp 80,443 -> %d; trust mitm CA in clients)", mpPort)

	if err := mitmproxy.SyncRootCA("", mpHome); err != nil {
		return nil, fmt.Errorf("mitm CA export: %w", err)
	}
	return &mitmTransparent{
		running:    running,
		currentGen: initialGen,
		port:       mpPort,
		uid:        mpUID,
		cfg:        cfg,
		nextGen:    initialGen,
		restartCh:  restartCh,
		shutdownCh: shutdownCh,
	}, nil
}

func startEnvoyTransparentIfEnabled() (*mitmTransparent, error) {
	if !constants.IsTruthy(os.Getenv(constants.EnvMitmproxyTransparent)) {
		return nil, nil
	}
	mpPort := constants.EnvIntOrDefault(constants.EnvEnvoyPort, constants.DefaultEnvoyPort)
	adminPort := constants.EnvIntOrDefault(constants.EnvEnvoyAdminPort, constants.DefaultEnvoyAdminPort)
	extProcAddr := envOrDefault(constants.EnvEnvoyExtProcAddr, constants.DefaultEnvoyExtProcAddr)
	workDir := "/tmp/opensandbox-envoy"
	staticHosts := csvHosts(strings.TrimSpace(os.Getenv(constants.EnvEnvoyMitmHosts)))
	mitmHosts := append([]string(nil), staticHosts...)
	if len(mitmHosts) == 0 {
		mitmHosts = []string{"dev.azure.com"}
	}
	proxyUID, proxyGID, _, err := mitmproxy.LookupUser(mitmproxy.RunAsUser)
	if err != nil {
		return nil, fmt.Errorf("lookup user %q: %w (ensure this user exists in the image)", mitmproxy.RunAsUser, err)
	}
	auth, err := mitmcert.LoadOrCreateAuthority(strings.TrimSpace(os.Getenv(constants.EnvEnvoyMitmCADir)))
	if err != nil {
		return nil, fmt.Errorf("envoy mitm CA: %w", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := auth.MintLeafForHosts(mitmHosts[0], mitmHosts)
	if err != nil {
		return nil, fmt.Errorf("envoy mitm SDS cert for %v: %w", mitmHosts, err)
	}
	sdsAddr := envOrDefault(constants.EnvEnvoySDSAddr, constants.DefaultEnvoySDSAddr)
	sdsSecret := envOrDefault(constants.EnvEnvoySDSSecret, constants.DefaultEnvoySDSSecret)
	sds, err := envoysds.New(sdsSecret, certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("envoy sds: %w", err)
	}
	sds.SetMintFunc(func(name string) ([]byte, []byte, error) {
		return auth.MintLeaf(name)
	})
	sdsLis, err := net.Listen("tcp", sdsAddr)
	if err != nil {
		return nil, fmt.Errorf("envoy sds listen %s: %w", sdsAddr, err)
	}
	safego.Go(func() {
		if err := sds.Serve(context.Background(), sdsLis); err != nil {
			log.Errorf("envoy sds server error: %v", err)
		}
	})
	log.Infof("envoy sds server listening on %s", sdsAddr)

	envoyCfg := envoyproxy.Config{
		Path:        strings.TrimSpace(os.Getenv(constants.EnvEnvoyPath)),
		ListenPort:  mpPort,
		AdminPort:   adminPort,
		ExtProcAddr: extProcAddr,
		SDSAddr:     sdsAddr,
		SDSSecret:   sdsSecret,
		OnDemandSDS: envBoolDefaultTrue(constants.EnvEnvoyOnDemandSDS),
		WorkDir:     workDir,
		UID:         proxyUID,
		GID:         proxyGID,
	}
	waitAddr := fmt.Sprintf("127.0.0.1:%d", mpPort)
	running, err := envoyproxy.Launch(envoyCfg)
	if err != nil {
		return nil, fmt.Errorf("start envoy: %w", err)
	}
	if err := mitmproxy.WaitListenPort(waitAddr, 15*time.Second); err != nil {
		return nil, fmt.Errorf("wait listen %s: %w", waitAddr, err)
	}
	if err := iptables.SetupTransparentHTTP(mpPort, proxyUID); err != nil {
		return nil, fmt.Errorf("iptables transparent: %w", err)
	}
	if err := mitmcert.ExportCA(auth); err != nil {
		return nil, fmt.Errorf("envoy mitm CA export: %w", err)
	}
	log.Infof("envoy: transparent intercept active (OUTPUT tcp 80,443 -> %d, on-demand MITM enabled, default MITM hosts=%v)", mpPort, mitmHosts)
	return &mitmTransparent{envoy: running, port: mpPort, uid: proxyUID, gid: proxyGID, auth: auth, sds: sds, envoyCfg: envoyCfg, staticHosts: staticHosts, mitmHosts: mitmHosts}, nil
}

func (m *mitmTransparent) setAllowHost(fn func(string) bool) {
	if m == nil || m.sds == nil {
		return
	}
	m.sds.SetAllowFunc(fn)
}

func (m *mitmTransparent) updateEnvoyHosts(hosts []string) {
	if m == nil || m.envoy == nil || m.auth == nil || m.sds == nil {
		return
	}
	hosts = mergeHosts(m.staticHosts, hosts)
	if len(hosts) == 0 {
		hosts = []string{"dev.azure.com"}
	}
	m.mu.Lock()
	if sameStrings(m.mitmHosts, hosts) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	certPEM, keyPEM, err := m.auth.MintLeafForHosts(hosts[0], hosts)
	if err != nil {
		log.Errorf("envoy: update default SDS cert for hosts %v: %v", hosts, err)
		return
	}
	if err := m.sds.Update(certPEM, keyPEM); err != nil {
		log.Errorf("envoy: update default SDS secret for hosts %v: %v", hosts, err)
		return
	}
	m.mu.Lock()
	m.mitmHosts = hosts
	m.mu.Unlock()
	log.Infof("envoy: updated default MITM hosts=%v", hosts)
}

func safeCertName(host string) string {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

func csvHosts(s string) []string {
	seen := map[string]struct{}{}
	var hosts []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(strings.TrimSuffix(part, "."))
		if part != "" {
			part = strings.ToLower(part)
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			hosts = append(hosts, part)
		}
	}
	return hosts
}

func mergeHosts(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, group := range groups {
		for _, host := range group {
			host = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(host), "."))
			if host == "" {
				continue
			}
			if _, ok := seen[host]; ok {
				continue
			}
			seen[host] = struct{}{}
			out = append(out, host)
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// watchMitmproxy monitors mitmdump for unexpected exits, logs the error, and restarts it.
// Must be called after startMitmproxyTransparentIfEnabled.
func (m *mitmTransparent) watchMitmproxy(ctx context.Context, gate *mitmproxy.HealthGate) {
	// Closing shutdownCh on ctx cancel unblocks any OnExit closures that are
	// parked on the (now-unread) restartCh send so they don't leak past
	// shutdown.
	safego.Go(func() {
		<-ctx.Done()
		close(m.shutdownCh)
	})
	safego.Go(func() {
		for {
			select {
			case ev := <-m.restartCh:
				select {
				case <-ctx.Done():
					return
				default:
				}
				cur := m.getCurrentGen()
				if ev.gen != cur {
					// Stale event: a previous half-launched attempt that we
					// killed is just now being reaped. The currently-live
					// mitmdump is unaffected; ignore and keep watching.
					log.Infof("[mitmproxy] ignoring stale exit event (gen=%d, current=%d): %v", ev.gen, cur, ev.err)
					continue
				}

				log.Errorf("[mitmproxy] mitmdump exited (gen=%d): %v; restarting...", ev.gen, ev.err)
				gate.SetReady(false)
				m.restartWithBackoff(ctx, gate)

			case <-ctx.Done():
				return
			}
		}
	})
}

// restartWithBackoff retries mitmdump launch indefinitely with exponential backoff
// (1s, 2s, 4s, ..., capped at 30s) until it succeeds or ctx is cancelled.
// Transient OOM / resource pressure must not leave egress in a permanent dead state.
//
// Each attempt is tagged with a fresh generation; setRunning publishes that
// generation as the "live" one. Exit events for older (killed) generations are
// filtered out by watchMitmproxy, so we do NOT drain restartCh here -- doing
// so could swallow a real death of the freshly-restarted mitmdump.
func (m *mitmTransparent) restartWithBackoff(ctx context.Context, gate *mitmproxy.HealthGate) {
	const (
		initialBackoff = time.Second
		maxBackoff     = 30 * time.Second
	)
	backoff := initialBackoff
	waitAddr := fmt.Sprintf("127.0.0.1:%d", m.cfg.ListenPort)

	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		gen := atomic.AddUint64(&m.nextGen, 1)
		newRunning, launchErr := launchTagged(m.cfg, m.restartCh, m.shutdownCh, gen)
		if launchErr == nil {
			if waitErr := mitmproxy.WaitListenPort(waitAddr, 15*time.Second); waitErr == nil {
				m.setRunning(newRunning, gen)
				gate.SetReady(true)
				log.Infof("[mitmproxy] mitmdump restarted (pid %d, gen %d, attempt %d)", newRunning.Cmd.Process.Pid, gen, attempt)
				return
			} else {
				log.Errorf("[mitmproxy] restart attempt %d (gen %d): wait listen %s: %v", attempt, gen, waitAddr, waitErr)
				// GracefulShutdown SIGTERMs then SIGKILLs and waits for reap, so
				// the listen port is released before the next attempt's Launch
				// races to bind it. Direct Process.Kill returns immediately and
				// can cause spurious WaitListenPort failures on port contention.
				mitmproxy.GracefulShutdown(newRunning, time.Second)
			}
		} else {
			log.Errorf("[mitmproxy] restart attempt %d (gen %d): launch failed: %v", attempt, gen, launchErr)
		}

		log.Warnf("[mitmproxy] restart attempt %d failed; retrying in %s", attempt, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}
