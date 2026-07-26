//go:build linux

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/doctor"
	"github.com/joshyorko/camp/internal/domain"
)

type doctorReachabilityToolResolver struct{}

func (doctorReachabilityToolResolver) Inspect(context.Context, string) (doctor.ManagedToolIdentity, error) {
	return doctor.ManagedToolIdentity{Path: "/missing/devpod"}, nil
}

func TestProductionReachabilityProbesGateUnconfiguredCapabilities(t *testing.T) {
	probes := productionReachabilityProbes(config.Bootstrap{}, nil, doctorReachabilityToolResolver{})
	want := map[string]bool{"provider": true, "workspace": true, "forwarding": true, "service": true}
	for _, probe := range probes {
		if !want[probe.Capability()] {
			continue
		}
		result := probe.Probe(context.Background())
		if result.Status != doctor.StatusSkippedNotConfigured {
			t.Fatalf("%s status = %s, want %s", probe.Capability(), result.Status, doctor.StatusSkippedNotConfigured)
		}
		delete(want, probe.Capability())
	}
	if len(want) != 0 {
		t.Fatalf("missing reachability probes: %v", want)
	}
}

func TestProductionReachabilityProbesSelectConfiguredProviderWorkspaceForwardingAndService(t *testing.T) {
	session := domain.JournalSnapshot{
		State:     domain.SessionOpen,
		Workspace: domain.WorkspaceRecord{ID: "workspace-1", Context: "default"},
		Recovery:  domain.RecoveryRecord{Forwarding: []domain.ForwardingRecord{{Name: "registry", EvidencePath: "/tmp/registry.pid"}}},
		Services:  []domain.ServiceUnitRecord{{Name: "registry"}},
	}
	probes := productionReachabilityProbes(config.Bootstrap{DevPodProvider: "docker", DevPodContext: "default"}, []domain.JournalSnapshot{session}, doctorReachabilityToolResolver{})
	for _, probe := range probes {
		if probe.Capability() == "provider" || probe.Capability() == "workspace" || probe.Capability() == "forwarding" || probe.Capability() == "service" {
			if result := probe.Probe(context.Background()); result.Status == doctor.StatusSkippedNotConfigured {
				t.Fatalf("configured %s was gated: %#v", probe.Capability(), result)
			}
		}
	}
}

func TestProductionProviderProbeNamesExactCampRepairCommand(t *testing.T) {
	probes := productionReachabilityProbes(config.Bootstrap{DevPodProvider: "docker", DevPodContext: "work"}, nil, doctorReachabilityToolResolver{})
	for _, probe := range probes {
		if probe.Capability() != "provider" {
			continue
		}
		result := probe.Probe(context.Background())
		if result.Code != "provider_unreachable" || !strings.Contains(result.Remediation, "camp provider add docker --context work") {
			t.Fatalf("provider result = %#v", result)
		}
		return
	}
	t.Fatal("provider probe missing")
}

func TestProductionReachabilityProbesIgnoreClosedSessions(t *testing.T) {
	closed := domain.JournalSnapshot{
		State:     domain.SessionClosed,
		Workspace: domain.WorkspaceRecord{ID: "workspace-1", Context: "default"},
		Recovery:  domain.RecoveryRecord{Forwarding: []domain.ForwardingRecord{{Name: "registry"}}},
		Services:  []domain.ServiceUnitRecord{{Name: "registry"}},
	}
	probes := productionReachabilityProbes(config.Bootstrap{}, []domain.JournalSnapshot{closed}, doctorReachabilityToolResolver{})
	for _, probe := range probes {
		if result := probe.Probe(context.Background()); result.Status != doctor.StatusSkippedNotConfigured {
			t.Fatalf("closed-session %s status = %s, want %s", probe.Capability(), result.Status, doctor.StatusSkippedNotConfigured)
		}
	}
}
