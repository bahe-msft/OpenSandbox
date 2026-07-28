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

package nftables

import (
	"context"
	"net/netip"
	"sort"
	"sync"
	"time"
)

type connectionTracker struct {
	mu                sync.Mutex
	dynamicIPs        map[netip.Addr]time.Time
	previousActiveIPs map[netip.Addr]struct{}
	listConnections   func(context.Context) ([]tcpConnection, error)
	now               func() time.Time
}

type connectionRefresh struct {
	addresses []netip.Addr
	active    map[netip.Addr]struct{}
	at        time.Time
}

func newConnectionTracker() *connectionTracker {
	return &connectionTracker{
		dynamicIPs:        make(map[netip.Addr]time.Time),
		previousActiveIPs: make(map[netip.Addr]struct{}),
		listConnections:   listTCPConnections,
		now:               time.Now,
	}
}

func (t *connectionTracker) setDynamicIPs(ips []ResolvedIP) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for _, ip := range ips {
		addr := ip.Addr.Unmap()
		if addr.IsValid() {
			t.dynamicIPs[addr] = now.Add(clampTTL(ip.TTL))
		}
	}
}

func (t *connectionTracker) refreshCandidates(connections []tcpConnection) connectionRefresh {
	t.mu.Lock()
	defer t.mu.Unlock()
	active := activeRemoteIPs(connections)
	now := t.now()
	for addr, expiresAt := range t.dynamicIPs {
		if !expiresAt.After(now) {
			if _, isActive := active[addr]; isActive {
				continue
			}
			if _, wasActive := t.previousActiveIPs[addr]; wasActive {
				continue
			}
			delete(t.dynamicIPs, addr)
			delete(t.previousActiveIPs, addr)
		}
	}

	current := make(map[netip.Addr]struct{})
	refresh := make(map[netip.Addr]struct{})
	for addr := range active {
		if _, ok := t.dynamicIPs[addr]; ok {
			current[addr] = struct{}{}
			refresh[addr] = struct{}{}
		}
	}
	// A final refresh when activity ends makes the existing timeout the
	// reconnect grace period instead of extending every DNS answer globally.
	for addr := range t.previousActiveIPs {
		if _, stillActive := current[addr]; !stillActive {
			if _, known := t.dynamicIPs[addr]; known {
				refresh[addr] = struct{}{}
			}
		}
	}

	addresses := make([]netip.Addr, 0, len(refresh))
	for addr := range refresh {
		addresses = append(addresses, addr)
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Compare(addresses[j]) < 0 })
	return connectionRefresh{addresses: addresses, active: current, at: now}
}

func (t *connectionTracker) recordRefresh(refresh connectionRefresh) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, addr := range refresh.addresses {
		t.dynamicIPs[addr] = refresh.at.Add(time.Duration(dynSetTimeoutS) * time.Second)
	}
	t.previousActiveIPs = refresh.active
}

func (t *connectionTracker) clearPreviousActiveIPs() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearPreviousActiveIPsLocked()
}

func (t *connectionTracker) clearPreviousActiveIPsLocked() {
	t.previousActiveIPs = make(map[netip.Addr]struct{})
}

func (t *connectionTracker) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dynamicIPs = make(map[netip.Addr]time.Time)
	t.clearPreviousActiveIPsLocked()
}

func activeRemoteIPs(connections []tcpConnection) map[netip.Addr]struct{} {
	active := make(map[netip.Addr]struct{})
	for _, connection := range connections {
		if !activeTCPState(connection.state) || !connection.remote.IsValid() {
			continue
		}
		active[connection.remote.Unmap()] = struct{}{}
	}
	return active
}

func activeTCPState(state string) bool {
	switch state {
	case "ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT1", "FIN_WAIT2", "CLOSE_WAIT", "CLOSING", "LAST_ACK":
		return true
	default:
		return false
	}
}
