//go:build linux

package doctor

import (
	"context"
	"errors"
	"testing"
)

func TestHostCapabilityProbeReportsFunctionalEvidence(t *testing.T) {
	probe := HostCapabilityProbe{
		Name: "proc-self-fd",
		Check: func(context.Context) (map[string]string, error) {
			return map[string]string{"path": "/proc/self/fd", "operation": "open-readlink"}, nil
		},
	}
	result := probe.Probe(context.Background())
	if result.Status != StatusHealthy || result.Code != "proc_self_fd_available" || result.Evidence["operation"] != "open-readlink" {
		t.Fatalf("result = %#v", result)
	}
}

func TestHostCapabilityProbeBlocksWithoutLeakingCause(t *testing.T) {
	probe := HostCapabilityProbe{Name: "tun", Check: func(context.Context) (map[string]string, error) {
		return nil, errors.New("token=secret")
	}}
	result := probe.Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "tun_unavailable" || result.Summary == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestLinuxHostProbesExposeEveryRequiredBoundary(t *testing.T) {
	probes := LinuxHostProbes()
	want := map[string]bool{"proc-self-fd": false, "tun": false, "user-namespace": false, "lsm": false, "container-boundary": false}
	for _, probe := range probes {
		want[probe.Capability()] = true
	}
	for capability, found := range want {
		if !found {
			t.Fatalf("missing %s probe", capability)
		}
	}
}

func TestDetectContainerBoundaryDoesNotTreatArbitraryCgroupCharactersAsContainerEvidence(t *testing.T) {
	if got := detectContainerBoundary(false, "0::/user.slice/user-1000.slice/session.scope"); got != "host" {
		t.Fatalf("boundary = %q, want host", got)
	}
	if got := detectContainerBoundary(false, "0::/kubepods/burstable/pod"); got != "container" {
		t.Fatalf("boundary = %q, want container", got)
	}
}
