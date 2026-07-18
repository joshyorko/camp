package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestGuestListenerFallsBackToProcSocketOwnership(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	inspector := &unitInspector{procRoot: "/proc"}
	got, err := inspector.guestListeners(context.Background(), os.Getpid(), port)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "pid="+strconv.Itoa(os.Getpid())) {
		t.Fatalf("guest listener = %q", got)
	}
}

func TestPortAllocatorOmitsOccupiedPreferredPortAndReturnsDistinctCandidates(t *testing.T) {
	t.Parallel()
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	preferred := occupied.Addr().(*net.TCPAddr).Port

	candidates, err := NewPortAllocator().Candidates(context.Background(), preferred, 3)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %#v, want 3", candidates)
	}
	seen := map[int]bool{}
	for _, candidate := range candidates {
		if candidate == preferred {
			t.Fatalf("occupied preferred port %d was returned", preferred)
		}
		if candidate < 1 || candidate > 65535 {
			t.Fatalf("candidate %d is outside the TCP port range", candidate)
		}
		if seen[candidate] {
			t.Fatalf("candidate %d was returned more than once", candidate)
		}
		seen[candidate] = true
	}
}

func TestLaunchEndpointRetriesOnlyBeforeStableMappingIsCommitted(t *testing.T) {
	t.Parallel()
	snapshot := domain.JournalSnapshot{SessionID: "session-a"}
	spec := ServiceSpec{Name: "registry", Mapping: PortMapping{HostAddress: "127.0.0.1", GuestPort: 5100}}
	var attempts []int
	ensure := func(_ context.Context, current domain.JournalSnapshot, candidate ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
		attempts = append(attempts, candidate.Mapping.HostPort)
		if candidate.Mapping.HostPort == 5000 {
			return domain.ServiceUnitRecord{}, current, ErrUnknownPortOccupant
		}
		return domain.ServiceUnitRecord{Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: candidate.Mapping.HostPort, GuestPort: 5100}}, current, nil
	}
	record, _, err := LaunchEndpoint(context.Background(), snapshot, spec, []int{5000, 5001}, 0, ensure)
	if err != nil || record.Mapping.HostPort != 5001 || len(attempts) != 2 {
		t.Fatalf("LaunchEndpoint() = %#v attempts=%#v error=%v", record, attempts, err)
	}
	attempts = nil
	_, _, err = LaunchEndpoint(context.Background(), snapshot, spec, []int{5000, 5001}, 5000, ensure)
	if !errors.Is(err, ErrUnknownPortOccupant) || len(attempts) != 1 || attempts[0] != 5000 {
		t.Fatalf("stable LaunchEndpoint() attempts=%#v error=%v", attempts, err)
	}
}
