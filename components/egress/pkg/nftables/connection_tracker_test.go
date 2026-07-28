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
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConnectionTrackerSetDynamicIPs(t *testing.T) {
	now := time.Unix(1_000, 0)
	tracker := newConnectionTracker()
	tracker.now = func() time.Time { return now }
	tracker.setDynamicIPs([]ResolvedIP{
		{Addr: netip.MustParseAddr("1.1.1.1"), TTL: 10 * time.Second},
		{Addr: netip.MustParseAddr("::ffff:192.0.2.1"), TTL: 120 * time.Second},
	})

	require.Equal(t, map[netip.Addr]time.Time{
		netip.MustParseAddr("1.1.1.1"):   now.Add(70 * time.Second),
		netip.MustParseAddr("192.0.2.1"): now.Add(180 * time.Second),
	}, tracker.dynamicIPs)
}

func TestConnectionTrackerRefreshCandidates(t *testing.T) {
	now := time.Unix(1_000, 0)
	tracker := newConnectionTracker()
	tracker.now = func() time.Time { return now }
	tracker.setDynamicIPs([]ResolvedIP{
		{Addr: netip.MustParseAddr("1.1.1.1"), TTL: time.Minute},
		{Addr: netip.MustParseAddr("2001:db8::1"), TTL: time.Minute},
	})

	refresh := tracker.refreshCandidates([]tcpConnection{
		{remote: netip.MustParseAddr("2001:db8::1"), state: "ESTABLISHED"},
		{remote: netip.MustParseAddr("1.1.1.1"), state: "SYN_SENT"},
		{remote: netip.MustParseAddr("2.2.2.2"), state: "ESTABLISHED"},
		{remote: netip.MustParseAddr("1.1.1.1"), state: "TIME_WAIT"},
	})

	require.Equal(t, []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("2001:db8::1"),
	}, refresh.addresses)
	require.Equal(t, map[netip.Addr]struct{}{
		netip.MustParseAddr("1.1.1.1"):     {},
		netip.MustParseAddr("2001:db8::1"): {},
	}, refresh.active)
}

func TestConnectionTrackerFinalRefreshAfterClose(t *testing.T) {
	now := time.Unix(1_000, 0)
	addr := netip.MustParseAddr("1.1.1.1")
	tracker := newConnectionTracker()
	tracker.now = func() time.Time { return now }
	tracker.setDynamicIPs([]ResolvedIP{{Addr: addr, TTL: time.Minute}})
	tracker.recordRefresh(tracker.refreshCandidates([]tcpConnection{{remote: addr, state: "ESTABLISHED"}}))

	refresh := tracker.refreshCandidates(nil)
	require.Equal(t, []netip.Addr{addr}, refresh.addresses)
	tracker.recordRefresh(refresh)
	require.Empty(t, tracker.refreshCandidates(nil).addresses)
	require.Equal(t, now.Add(6*time.Minute), tracker.dynamicIPs[addr])
}

func TestConnectionTrackerForgetsExpiredInactiveIP(t *testing.T) {
	now := time.Unix(1_000, 0)
	tracker := newConnectionTracker()
	tracker.now = func() time.Time { return now }
	tracker.setDynamicIPs([]ResolvedIP{{Addr: netip.MustParseAddr("1.1.1.1"), TTL: 10 * time.Second}})
	now = now.Add(71 * time.Second)

	require.Empty(t, tracker.refreshCandidates(nil).addresses)
	require.Empty(t, tracker.dynamicIPs)
}

func TestConnectionTrackerClear(t *testing.T) {
	tracker := newConnectionTracker()
	tracker.setDynamicIPs([]ResolvedIP{{Addr: netip.MustParseAddr("1.1.1.1"), TTL: time.Minute}})
	tracker.previousActiveIPs[netip.MustParseAddr("1.1.1.1")] = struct{}{}

	tracker.clear()
	require.Empty(t, tracker.dynamicIPs)
	require.Empty(t, tracker.previousActiveIPs)
}

func TestConnectionTrackerConcurrentAccess(t *testing.T) {
	tracker := newConnectionTracker()
	addr := netip.MustParseAddr("1.1.1.1")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			tracker.setDynamicIPs([]ResolvedIP{{Addr: addr, TTL: time.Minute}})
		}()
		go func() {
			defer wg.Done()
			refresh := tracker.refreshCandidates([]tcpConnection{{remote: addr, state: "ESTABLISHED"}})
			tracker.recordRefresh(refresh)
		}()
		go func() {
			defer wg.Done()
			tracker.clearPreviousActiveIPs()
		}()
	}
	wg.Wait()
}

func TestConnectionTrackerRefreshActiveConnections(t *testing.T) {
	now := time.Unix(1_000, 0)
	addr := netip.MustParseAddr("1.1.1.1")
	tracker := newConnectionTracker()
	tracker.now = func() time.Time { return now }
	tracker.setDynamicIPs([]ResolvedIP{{Addr: addr, TTL: time.Minute}})
	var refreshed []netip.Addr

	err := tracker.refreshActiveConnections(
		context.Background(),
		[]tcpConnection{{remote: addr, state: "ESTABLISHED"}},
		func(_ context.Context, refresh connectionRefresh) error {
			refreshed = append(refreshed, refresh.addresses...)
			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{addr}, refreshed)
	require.Equal(t, now.Add(6*time.Minute), tracker.dynamicIPs[addr])
	require.Contains(t, tracker.previousActiveIPs, addr)
}

func TestConnectionTrackerRefreshFailureDoesNotRecordState(t *testing.T) {
	addr := netip.MustParseAddr("1.1.1.1")
	tracker := newConnectionTracker()
	tracker.setDynamicIPs([]ResolvedIP{{Addr: addr, TTL: time.Minute}})
	originalExpiry := tracker.dynamicIPs[addr]

	err := tracker.refreshActiveConnections(
		context.Background(),
		[]tcpConnection{{remote: addr, state: "ESTABLISHED"}},
		func(context.Context, connectionRefresh) error { return fmt.Errorf("nft failed") },
	)
	require.Error(t, err)
	require.Equal(t, originalExpiry, tracker.dynamicIPs[addr])
	require.Empty(t, tracker.previousActiveIPs)
}

func TestConnectionTrackerRejectsStaleRefreshAfterClear(t *testing.T) {
	addr := netip.MustParseAddr("1.1.1.1")
	tracker := newConnectionTracker()
	tracker.setDynamicIPs([]ResolvedIP{{Addr: addr, TTL: time.Minute}})
	refresh := tracker.refreshCandidates([]tcpConnection{{remote: addr, state: "ESTABLISHED"}})

	tracker.clear()
	require.False(t, tracker.isCurrent(refresh))
	tracker.recordRefresh(refresh)
	require.Empty(t, tracker.dynamicIPs)
	require.Empty(t, tracker.previousActiveIPs)
}
