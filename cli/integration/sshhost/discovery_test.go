//go:build integration

package sshhost_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestAgentlessDiscoveryThroughDisposableDevelopmentHost(t *testing.T) {
	manager := testConfiguredManager(t, testHostConnector(t))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("close Manager: %v", err)
		}
	})

	snapshot := waitForBaseline(t, manager)
	host := snapshot.Host
	if host.Discovery.State != core.DiscoveryHealthy && host.Discovery.State != core.DiscoveryDegraded {
		t.Fatalf("Discovery State = %q, want healthy or degraded", host.Discovery.State)
	}
	if host.Discovery.Capability.RemoteListeners != core.CapabilityFull || host.Discovery.Capability.SocketIdentity != core.CapabilityFull {
		t.Fatalf("Discovery Capability = %#v, want complete listener and socket evidence", host.Discovery.Capability)
	}
	assertFixtureObservation(t, host.ListenerObservations, core.FamilyIPv4, fixturePortV4())
	assertFixtureObservation(t, host.ListenerObservations, core.FamilyIPv6, fixturePortV6())

	baselineRevision := snapshot.Revision
	// The first identical scan reclassifies the baseline listeners from new to
	// continuous — a one-time lifetime transition that legitimately advances
	// the revision. Afterwards a stable host must not advance it again.
	stableDeadline := time.Now().Add(6 * time.Second)
	previous := baselineRevision
	for {
		time.Sleep(2200 * time.Millisecond)
		repeated, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot after repeated scan: %v", err)
		}
		if repeated.Revision == previous {
			return // two identical scans, no revision advance: stable
		}
		if time.Now().After(stableDeadline) {
			t.Fatalf("stable host kept advancing revision: %d -> %d", previous, repeated.Revision)
		}
		previous = repeated.Revision
	}
}

func assertFixtureObservation(t *testing.T, observations []core.ListenerObservation, family core.AddressFamily, port uint16) {
	t.Helper()
	for _, observation := range observations {
		if observation.Family != family || observation.BindScope != core.BindLoopback || observation.RemotePort != port {
			continue
		}
		if len(observation.SocketIdentities) == 0 || !strings.HasPrefix(string(observation.SocketIdentities[0]), "socket:") {
			t.Fatalf("fixture Socket Identities = %#v", observation.SocketIdentities)
		}
		for _, chain := range observation.Processes {
			if len(chain.Processes) != 0 && strings.Contains(chain.Processes[0].Executable, "python") {
				return
			}
		}
		t.Fatalf("fixture observation lacks Python Process Chain: %#v", observation)
	}
	t.Fatalf("missing %s loopback Listener Observation on port %d: %#v", family, port, observations)
}
