package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/app"
)

var (
	_ OperationalStatus = (*ProductionLifecycle)(nil)
	_ ImageOperations   = (*ProductionLifecycle)(nil)
	_ ServeOperations   = (*ProductionLifecycle)(nil)
	_ ProviderLister    = (*ProductionLifecycle)(nil)
)

func TestProductionOperationalSelectorForwardsOnlyBoundaryFields(t *testing.T) {
	t.Parallel()

	got := operationalSelector(SessionRequest{SessionID: "session-1", Capsule: "brain", Branch: "work"})
	if got != (app.SessionSelector{SessionID: "session-1", Capsule: "brain", Branch: "work"}) {
		t.Fatalf("selector = %#v", got)
	}
}

func TestProductionStatusHumanOutputIsStableAndObserved(t *testing.T) {
	t.Parallel()

	model := app.SessionReadModel{
		ID: "session-1", Capsule: "brain", Branch: "main", State: "open", Mode: "read-write",
		Services:  []app.ServiceReadModel{{Name: "registry", Liveness: app.ServiceLivenessLive}},
		Recovery:  app.RecoveryReadModel{Condition: app.RecoveryNone},
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}
	var output bytes.Buffer
	if err := writeStatusResult(&output, ModeHuman, model); err != nil {
		t.Fatal(err)
	}
	want := "SESSION\tCAPSULE\tBRANCH\tSTATE\tMODE\tRECOVERY\nsession-1\tbrain\tmain\topen\tread-write\tnone\nSERVICE\tLIVENESS\nregistry\tlive\n"
	if output.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestProductionProviderHumanOutputIsSortedAndNonSecret(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := writeProvidersResult(&output, ModeHuman, []app.Provider{{Name: "docker"}, {Name: "ssh"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "PROVIDER\ndocker\nssh\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestProductionServeLogsPreserveBytesAndReportTruncation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := writeServeLogsResult(&output, ModeHuman, supervisor.LogChunk{Bytes: []byte("line one\n"), Truncated: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[earlier log bytes omitted]\nline one\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
