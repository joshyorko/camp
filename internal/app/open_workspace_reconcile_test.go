package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/remoteworker"
)

func TestOpenReconcileWorkspaceUpUnknownOutcomeUsesStatusWithoutSecondUp(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{omitStatusIdentity: true}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "workspace-up-outcome-unknown"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	before, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Intent.Transition != "WorkspaceUp" || before.Workspace.ID != "" {
		t.Fatalf("pre-reconciliation workspace=%#v pending=%#v", before.Workspace, pending)
	}

	reconciled, err := environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciled.Workspace.ID == "" || reconciled.Workspace.Context != "default" || reconciled.Workspace.Provider != "docker" ||
		reconciled.Workspace.LocalFolder != root || reconciled.Workspace.StagingRoot != root {
		t.Fatalf("reconciled workspace = %#v", reconciled.Workspace)
	}
	if len(devpod.ups) != 1 || devpod.listCalls != 2 || devpod.statusCalls != 1 {
		t.Fatalf("DevPod calls after reconciliation = up:%d list:%d status:%d, want up:1 list:2 status:1", len(devpod.ups), devpod.listCalls, devpod.statusCalls)
	}
	_, pending, err = environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("post-reconciliation pending=%#v error=%v", pending, err)
	}
}

func TestOpenReconcileRemoteWorkspaceUpRequiresActivationAndHydrationReceipts(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	dataPlane := &recordingRemoteDataPlane{bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap")}
	environment.open.deps.RemoteDataPlane = dataPlane
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root"}
	environment.open.deps.DevPod = devpod
	request := OpenRequest{
		SessionID: "remote-workspace-up-receipt-observation", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "ssh", Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}

	_, err := environment.open.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "remote activation receipt is incomplete") {
		t.Fatalf("reconciled remote Open() error = %v, want typed missing activation receipt", err)
	}
	if len(devpod.ups) != 1 {
		t.Fatalf("reconciliation issued %d DevPod ups, want 1", len(devpod.ups))
	}
	_, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if loadErr != nil || len(pending) != 1 || pending[0].Intent.Transition != "WorkspaceUp" {
		t.Fatalf("receipt-less reconciliation pending=%#v error=%v", pending, loadErr)
	}
}

func TestOpenReconcileRemoteWorkspaceUpAcceptsBoundStartupReceipts(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	dataPlane := &recordingRemoteDataPlane{bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap")}
	environment.open.deps.RemoteDataPlane = dataPlane
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root", startupReady: true}
	environment.open.deps.DevPod = devpod
	request := OpenRequest{
		SessionID: "remote-workspace-up-bound-receipts", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "ssh", Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	result, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("reconciled remote Open() error = %v", err)
	}
	if result.Snapshot.State != domain.SessionOpen || len(devpod.ups) != 1 || devpod.startupObservations != 1 {
		t.Fatalf("reconciled result=%#v ups=%d startup observations=%d", result, len(devpod.ups), devpod.startupObservations)
	}
}

func TestOpenFailedWorkspaceUpAmbiguityIncludesBoundedDevPodStderr(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{
		upResults: []ports.Result{{ExitCode: 17, Stderr: []byte("\x1b]0;spoofed\a\x1b[31mfatal\x1b[0m: workspace agent failed before readiness")}},
		upErrors:  []error{errors.New("exit status 17")},
	}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "workspace-up-diagnostic", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) || !strings.Contains(err.Error(), "workspace agent failed before readiness") {
		t.Fatalf("Open() error = %v, want ambiguous outcome with bounded DevPod diagnostic", err)
	}
	if strings.ContainsAny(err.Error(), "\x1b\a") || !strings.Contains(err.Error(), `\x1b`) {
		t.Fatalf("Open() error contains unsafe or unescaped terminal controls: %q", err)
	}
}

func TestWorkspaceUpDiagnosticTextBoundsBeforeSanitizing(t *testing.T) {
	t.Parallel()
	diagnostic := workspaceUpDiagnosticText(ports.Result{Stderr: []byte(strings.Repeat("a", workspaceUpDiagnostic-4) + "\x1b" + "tail")})
	if !strings.HasPrefix(diagnostic, "[earlier DevPod output omitted]\n") {
		t.Fatalf("diagnostic = %q, want omitted-prefix note", diagnostic)
	}
	body := strings.TrimPrefix(diagnostic, "[earlier DevPod output omitted]\n")
	if !strings.Contains(body, `\x1b`) {
		t.Fatalf("diagnostic body = %q, want escaped control", body)
	}
	control := strings.Index(body, `\x1b`)
	if control < 0 {
		t.Fatalf("diagnostic body = %q, want escaped control", body)
	}
	if got := strings.Count(body[:control], "a"); got >= workspaceUpDiagnostic {
		t.Fatalf("diagnostic body prefix length = %d, want bounded raw evidence", got)
	}
	if !strings.HasSuffix(body, "tail") {
		t.Fatalf("diagnostic body = %q, want preserved tail", body)
	}
}

func TestWorkspaceUpDiagnosticTextRedactsSecretsAndEscapesFormatControls(t *testing.T) {
	t.Parallel()
	diagnostic := workspaceUpDiagnosticText(ports.Result{Stderr: []byte("configured-secret\nghp_1234567890abcdef1234567890abcdef123456\nleft-to-right\u202e\u200eend")})
	if strings.Contains(diagnostic, "configured-secret") {
		t.Fatalf("diagnostic leaked secret material: %q", diagnostic)
	}
	if strings.Contains(diagnostic, "ghp_1234567890abcdef1234567890abcdef123456") {
		t.Fatalf("diagnostic leaked opaque credential value: %q", diagnostic)
	}
	if strings.ContainsRune(diagnostic, '\u202e') || strings.ContainsRune(diagnostic, '\u200e') {
		t.Fatalf("diagnostic contains raw bidi/format controls: %q", diagnostic)
	}
	if !strings.Contains(diagnostic, `\u202e`) || !strings.Contains(diagnostic, `\u200e`) {
		t.Fatalf("diagnostic = %q, want escaped bidi/format controls", diagnostic)
	}
	if !strings.Contains(diagnostic, "[redacted DevPod secret]") {
		t.Fatalf("diagnostic = %q, want secret redaction placeholder", diagnostic)
	}
}

func TestWorkspaceUpDiagnosticTextEnforcesFinalRenderedBound(t *testing.T) {
	t.Parallel()
	diagnostic := workspaceUpDiagnosticText(ports.Result{Stderr: []byte(strings.Repeat("A", workspaceUpDiagnostic) + strings.Repeat("\x1b", 32) + "tail")})
	if len(diagnostic) > workspaceUpDiagnostic {
		t.Fatalf("diagnostic length = %d, want <= %d", len(diagnostic), workspaceUpDiagnostic)
	}
	if !strings.HasPrefix(diagnostic, "[earlier DevPod output omitted]\n") {
		t.Fatalf("diagnostic = %q, want rendered truncation note", diagnostic)
	}
	if !strings.Contains(diagnostic, `\x1b`) {
		t.Fatalf("diagnostic = %q, want escaped controls after final bound", diagnostic)
	}
	if !strings.HasSuffix(diagnostic, "tail") {
		t.Fatalf("diagnostic = %q, want preserved tail after final bound", diagnostic)
	}
}

func TestOpenWorkspaceUpSettlementFailurePreservesDiagnosticEvidence(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{
		workspaceAbsent: true,
		upResults:       []ports.Result{{ExitCode: 17, Stderr: []byte("workspace agent failed before readiness")}},
		upErrors:        []error{errors.New("exit status 17")},
	}
	environment.open.deps.DevPod = devpod
	environment.open.deps.Journal = &workspaceUpSettlementFailureJournal{Journal: environment.open.deps.Journal, err: errors.New("journal append failed")}
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "workspace-up-settlement-failure", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err == nil || !strings.Contains(err.Error(), "exit status 17") || !strings.Contains(err.Error(), "workspace agent failed before readiness") || !strings.Contains(err.Error(), "journal append failed") {
		t.Fatalf("Open() error = %v, want diagnostic and settlement failure evidence", err)
	}
}

func TestOpenWorkspaceUpFailureErrorRedactsOpaqueCredentialValues(t *testing.T) {
	t.Parallel()
	const diagnostic = "github=ghp_1234567890abcdef1234567890abcdef123456 jwt=eyJhbGciOiJIUzI1NiJ9.opaque.payload aws=AKIAIOSFODNN7EXAMPLE generic=0123456789abcdef0123456789abcdef"
	err := workspaceUpFailureError(errors.New("exit status 17"), ports.Result{Stderr: []byte(diagnostic)})
	for _, secret := range []string{"ghp_1234567890abcdef1234567890abcdef123456", "eyJhbGciOiJIUzI1NiJ9.opaque.payload", "AKIAIOSFODNN7EXAMPLE", "0123456789abcdef0123456789abcdef"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("workspaceUpFailureError leaked opaque credential %q: %q", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "[redacted DevPod secret]") {
		t.Fatalf("workspaceUpFailureError = %q, want redaction placeholder", err)
	}
}

func TestOpenWaitForWorkspaceReadyUsesTimeoutForInitialStatusProbe(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	ticks := make(chan time.Time, 1)
	ticks <- time.Unix(101, 0).UTC()
	environment.open.deps.Clock = &controlledClock{
		now:    time.Unix(100, 0).UTC(),
		ticker: &controlledTicker{channel: ticks},
	}
	devpod := &unknownOutcomeWorkspaceDevPod{statusStates: []devpodadapter.WorkspaceState{devpodadapter.StateBusy, devpodadapter.StateRunning}}
	environment.open.deps.DevPod = devpod
	status, err := environment.open.waitForWorkspaceReady(context.Background(), "default", "workspace-ready", "docker")
	if err != nil {
		t.Fatalf("waitForWorkspaceReady() error = %v", err)
	}
	if status.State != devpodadapter.StateRunning {
		t.Fatalf("waitForWorkspaceReady() status = %#v, want running", status)
	}
	if len(devpod.statusHasDeadline) != 2 || !devpod.statusHasDeadline[0] || !devpod.statusHasDeadline[1] {
		t.Fatalf("status deadlines = %#v, want deadlines for initial and poll calls", devpod.statusHasDeadline)
	}
}

func TestOpenReconcileWorkspaceUpWaitsForBusyWorkspaceWithoutSecondUp(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	ticks := make(chan time.Time, 1)
	ticks <- time.Unix(101, 0).UTC()
	environment.open.deps.Clock = &controlledClock{
		now:    time.Unix(100, 0).UTC(),
		ticker: &controlledTicker{channel: ticks},
	}
	devpod := &unknownOutcomeWorkspaceDevPod{
		folder:       "/workspaces/root",
		statusStates: []devpodadapter.WorkspaceState{devpodadapter.StateBusy, devpodadapter.StateRunning},
	}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "workspace-up-busy", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}

	result, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("resumed Open() error = %v", err)
	}
	if result.Snapshot.State != domain.SessionOpen {
		t.Fatalf("resumed state = %q, want %q", result.Snapshot.State, domain.SessionOpen)
	}
	if len(devpod.ups) != 1 || devpod.statusCalls != 2 {
		t.Fatalf("DevPod calls = up:%d status:%d, want up:1 status:2", len(devpod.ups), devpod.statusCalls)
	}
}

func TestOpenReconcileWorkspaceUpRejectsReplacementSourceAfterBusyWait(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	ticks := make(chan time.Time, 1)
	ticks <- time.Unix(101, 0).UTC()
	environment.open.deps.Clock = &controlledClock{
		now:    time.Unix(100, 0).UTC(),
		ticker: &controlledTicker{channel: ticks},
	}
	devpod := &unknownOutcomeWorkspaceDevPod{
		replacementSourceAfterFirstList: "/workspaces/replacement",
		statusStates:                    []devpodadapter.WorkspaceState{devpodadapter.StateBusy, devpodadapter.StateRunning},
	}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "workspace-up-busy-replaced", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	if _, err := environment.open.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "source identity") {
		t.Fatalf("resumed Open() error = %v, want replacement source rejection", err)
	}
	if len(devpod.ups) != 1 || devpod.listCalls != 2 {
		t.Fatalf("DevPod calls = up:%d list:%d, want up:1 list:2", len(devpod.ups), devpod.listCalls)
	}
}

func TestOpenReconcileWorkspaceUpUsesCommittedServiceMappingsAfterDurableReload(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{omitStatusIdentity: true}
	environment.open.deps.DevPod = devpod
	environment.open.deps.Services = dynamicOpenServices{registryPort: 45001, fileserverPort: 48081}
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "workspace-up-dynamic-endpoints"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	snapshot, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("Load() pending=%#v error=%v", pending, err)
	}
	if snapshot.Recovery.Configuration.RegistryPort != 5000 || snapshot.Recovery.Configuration.FileserverPort != 8080 {
		t.Fatalf("durable preferred ports unexpectedly changed: %#v", snapshot.Recovery.Configuration)
	}
	snapshot.Services = []domain.ServiceUnitRecord{
		{Name: "registry", Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: 45001, GuestPort: 5000}},
		{Name: "fileserver", Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: 48081, GuestPort: 8080}},
	}
	if _, _, err := environment.open.observeWorkspaceUp(context.Background(), snapshot, pending[0].Intent); err != nil {
		t.Fatalf("observeWorkspaceUp() error = %v", err)
	}
}

func TestOpenKnownWorkspaceUpFailureClearsAttemptOnlyAfterAbsenceAndRetries(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{
		workspaceAbsent: true,
		upResults:       []ports.Result{{ExitCode: 17, Stderr: []byte("\x1b[31mpull denied\x1b[0m")}, {ExitCode: -1}},
		upErrors:        []error{errors.New("exit status 17"), ports.ErrAmbiguous},
	}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "workspace-up-known-failure", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "exit status 17") || !strings.Contains(err.Error(), "pull denied") ||
		strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("first Open() error = %v", err)
	}
	_, pending, err := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("known failure pending=%#v error=%v", pending, err)
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("retry Open() error = %v, want ErrAmbiguous", err)
	}
	if len(devpod.ups) != 2 || devpod.listCalls != 1 {
		t.Fatalf("DevPod calls = up:%d list:%d, want up:2 list:1", len(devpod.ups), devpod.listCalls)
	}
}

type dynamicOpenServices struct{ registryPort, fileserverPort int }

func (s dynamicOpenServices) Start(_ context.Context, snapshot domain.JournalSnapshot) (domain.JournalSnapshot, error) {
	snapshot.Recovery.Configuration.RegistryPort = s.registryPort
	snapshot.Recovery.Configuration.FileserverPort = s.fileserverPort
	return snapshot, nil
}

func TestOpenResumesOpeningAfterWorkspaceUpObservationWithoutSecondUp(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root"}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(filepath.Join(root, "MemoryD"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "workspace-up-resume", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}

	result, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("resumed Open() error = %v", err)
	}
	if result.Snapshot.State != domain.SessionOpen || result.Snapshot.Workspace.EffectiveRoot != devpod.folder || result.MappedTarget != filepath.Join(devpod.folder, "MemoryD") {
		t.Fatalf("resumed result = %#v", result)
	}
	if len(devpod.ups) != 1 || devpod.listCalls != 2 || devpod.statusCalls != 1 || devpod.folderCalls != 1 || devpod.sshCalls != 1 {
		t.Fatalf("DevPod calls = up:%d list:%d status:%d folder:%d ssh:%d", len(devpod.ups), devpod.listCalls, devpod.statusCalls, devpod.folderCalls, devpod.sshCalls)
	}
	loaded, pending, err := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if err != nil || len(pending) != 0 || loaded.State != domain.SessionOpen || loaded.Workspace.EffectiveRoot != devpod.folder {
		t.Fatalf("durable resumed state = %#v pending=%#v error=%v", loaded, pending, err)
	}
}

func TestOpenEntryFailureLeavesDurableOpenSessionWithAttachGuidance(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root", sshErr: errors.New("terminal disconnected")}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "workspace-entry-failure", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	_, err := environment.open.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "camp attach") {
		t.Fatalf("resumed Open() error = %v, want attach guidance", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if loadErr != nil || loaded.State != domain.SessionOpen || len(pending) != 0 {
		t.Fatalf("durable entry-failure state = %#v pending=%#v error=%v", loaded, pending, loadErr)
	}
	if len(devpod.ups) != 1 || devpod.sshCalls != 1 {
		t.Fatalf("DevPod calls = up:%d ssh:%d, want up:1 ssh:1", len(devpod.ups), devpod.sshCalls)
	}
}

func TestOpenReconcileWorkspaceUpRejectsClosedSessionBeforeObservation(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "closed-workspace-up-intent"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	snapshot, _, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State = domain.SessionClosed
	stateIntent := ports.IntentRecord{ID: sessionID + "-closed-state", SessionID: sessionID, Transition: "ClosedStateRecorded", Attempt: 1, Timestamp: snapshot.UpdatedAt}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), stateIntent); err != nil {
		t.Fatal(err)
	}
	if err := environment.open.deps.Journal.RecordFact(context.Background(), ports.FactRecord{
		IntentID: stateIntent.ID, SessionID: sessionID, Transition: stateIntent.Transition, Timestamp: snapshot.UpdatedAt,
	}, snapshot); err != nil {
		t.Fatal(err)
	}

	if _, err := environment.open.Reconcile(context.Background(), sessionID); err == nil {
		t.Fatal("Reconcile() accepted WorkspaceUp for a closed session")
	}
	if devpod.listCalls != 0 || devpod.statusCalls != 0 || len(devpod.ups) != 1 {
		t.Fatalf("DevPod calls after closed-session reconciliation = up:%d list:%d status:%d", len(devpod.ups), devpod.listCalls, devpod.statusCalls)
	}
}

func TestOpenReconcileRejectsRecoveringWithoutOpenObjectiveBeforeObservation(t *testing.T) {
	t.Parallel()
	for _, objective := range []domain.RecoveryObjective{"", "checkpoint"} {
		objective := objective
		t.Run(string(objective), func(t *testing.T) {
			t.Parallel()
			environment := newOpenTestEnvironment(t)
			devpod := &unknownOutcomeWorkspaceDevPod{}
			environment.open.deps.DevPod = devpod
			root := filepath.Join(t.TempDir(), "SecondBrain")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			name := "missing"
			if objective != "" {
				name = "unknown"
			}
			sessionID := "recovering-objective-" + name
			_, err := environment.open.Run(context.Background(), OpenRequest{
				SessionID: sessionID, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
				ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
				Runtime: environment.runtime, Backend: environment.backend,
			})
			if !errors.Is(err, ports.ErrAmbiguous) {
				t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
			}
			snapshot, _, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.State = domain.SessionRecovering
			snapshot.Recovery.Objective = objective
			stateIntent := ports.IntentRecord{ID: sessionID + "-recovering-state", SessionID: sessionID, Transition: "RecoveringStateRecorded", Attempt: 1, Timestamp: snapshot.UpdatedAt}
			if err := environment.open.deps.Journal.RecordIntent(context.Background(), stateIntent); err != nil {
				t.Fatal(err)
			}
			if err := environment.open.deps.Journal.RecordFact(context.Background(), ports.FactRecord{
				IntentID: stateIntent.ID, SessionID: sessionID, Transition: stateIntent.Transition, Timestamp: snapshot.UpdatedAt,
			}, snapshot); err != nil {
				t.Fatal(err)
			}

			if _, err := environment.open.Reconcile(context.Background(), sessionID); err == nil || !strings.Contains(err.Error(), "recovery objective") {
				t.Fatalf("Reconcile() error = %v, want recovery objective rejection", err)
			}
			if devpod.listCalls != 0 || devpod.statusCalls != 0 || len(devpod.ups) != 1 {
				t.Fatalf("DevPod effects after invalid objective = up:%d list:%d status:%d", len(devpod.ups), devpod.listCalls, devpod.statusCalls)
			}
		})
	}
}

func TestOpenReconcileValidatesObjectiveOnExactObserverSnapshot(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "observer-objective-swap"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	environment.open.deps.Journal = &objectiveSwapJournal{Journal: environment.open.deps.Journal}

	if _, err := environment.open.Reconcile(context.Background(), sessionID); !errors.Is(err, ErrOpenRecoveryObjective) {
		t.Fatalf("Reconcile() error = %v, want ErrOpenRecoveryObjective", err)
	}
	if devpod.listCalls != 0 || devpod.statusCalls != 0 || len(devpod.ups) != 1 {
		t.Fatalf("DevPod effects after observer objective swap = up:%d list:%d status:%d", len(devpod.ups), devpod.listCalls, devpod.statusCalls)
	}
}

func TestOpenRunRejectsInvalidRecoveryObjectiveBeforeReentryEffects(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "invalid-objective-reentry", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Recovery.Objective = "checkpoint"
	intent := ports.IntentRecord{ID: request.SessionID + "-invalid-objective", SessionID: request.SessionID, Transition: "InvalidObjectiveInjected", Attempt: 1, Timestamp: snapshot.UpdatedAt}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := environment.open.deps.Journal.RecordFact(context.Background(), ports.FactRecord{
		IntentID: intent.ID, SessionID: request.SessionID, Transition: intent.Transition, Timestamp: snapshot.UpdatedAt,
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	eventCount, sshCount := len(*environment.events), len(environment.devpod.ssh)

	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ErrOpenRecoveryObjective) {
		t.Fatalf("Open() error = %v, want ErrOpenRecoveryObjective", err)
	}
	if len(*environment.events) != eventCount || len(environment.devpod.ssh) != sshCount {
		t.Fatalf("re-entry effects after invalid objective: events=%v ssh=%d", *environment.events, len(environment.devpod.ssh))
	}
}

type objectiveSwapJournal struct {
	ports.Journal
	loads int
}

func (j *objectiveSwapJournal) Load(ctx context.Context, sessionID string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	snapshot, pending, err := j.Journal.Load(ctx, sessionID)
	j.loads++
	if err == nil && j.loads == 2 {
		snapshot.Recovery.Objective = "checkpoint"
	}
	return snapshot, pending, err
}

func TestOpenBlockingEntryDoesNotLeavePendingCheckpointBlocker(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	entryStarted := make(chan struct{})
	releaseEntry := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseEntry)
		}
	}()
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root", entryStarted: entryStarted, releaseEntry: releaseEntry}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "blocking-terminal-entry", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := environment.open.Run(context.Background(), request)
		done <- err
	}()
	<-entryStarted
	loaded, pending, err := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if err != nil || loaded.State != domain.SessionOpen || len(pending) != 0 {
		t.Fatalf("journal while terminal is active = %#v pending=%#v error=%v", loaded, pending, err)
	}
	close(releaseEntry)
	released = true
	if err := <-done; err != nil {
		t.Fatalf("Open() after terminal release error = %v", err)
	}
}

func TestOpenReconcileWorkspaceRootQueryCompletesOriginalIntentWithoutSecondUp(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root"}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "workspace-root-outcome-unknown"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	snapshot, err := environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("WorkspaceUp reconciliation error = %v", err)
	}
	intent := ports.IntentRecord{
		ID: transitionID(sessionID, "WorkspaceRootResolved"), SessionID: sessionID, Transition: "WorkspaceRootResolved",
		Attempt: 1, Timestamp: snapshot.UpdatedAt, Input: safeJSON(struct {
			ID string `json:"id"`
		}{ID: snapshot.Workspace.ID}),
	}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}

	reconciled, err := environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("WorkspaceRootResolved reconciliation error = %v", err)
	}
	if reconciled.Workspace.EffectiveRoot != devpod.folder || len(devpod.ups) != 1 || devpod.folderCalls != 1 {
		t.Fatalf("reconciled workspace=%#v calls=up:%d folder:%d", reconciled.Workspace, len(devpod.ups), devpod.folderCalls)
	}
	_, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("post-reconciliation pending=%#v error=%v", pending, err)
	}
}

func TestOpenReconcileSessionOpenedCompletesReadyStateWithoutEntryEffect(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root"}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "session-open-outcome-unknown"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	snapshot, err := environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	rootIntent := ports.IntentRecord{
		ID: transitionID(sessionID, "WorkspaceRootResolved"), SessionID: sessionID, Transition: "WorkspaceRootResolved",
		Attempt: 1, Timestamp: snapshot.UpdatedAt, Input: safeJSON(openWorkspaceRootInput{ID: snapshot.Workspace.ID}),
	}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), rootIntent); err != nil {
		t.Fatal(err)
	}
	snapshot, err = environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	openedIntent := ports.IntentRecord{
		ID: transitionID(sessionID, "SessionOpened"), SessionID: sessionID, Transition: "SessionOpened",
		Attempt: 1, Timestamp: snapshot.UpdatedAt, Input: safeJSON(struct {
			ID string `json:"id"`
		}{ID: snapshot.Workspace.ID}),
	}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), openedIntent); err != nil {
		t.Fatal(err)
	}

	reconciled, err := environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SessionOpened reconciliation error = %v", err)
	}
	if reconciled.State != domain.SessionOpen || len(devpod.ups) != 1 || devpod.sshCalls != 0 {
		t.Fatalf("reconciled state=%q calls=up:%d ssh:%d", reconciled.State, len(devpod.ups), devpod.sshCalls)
	}
	_, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("post-reconciliation pending=%#v error=%v", pending, err)
	}
}

func TestOpenEntryStartFailureClearsPendingIntentAndLeavesSessionOpen(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root", startErr: errors.New("exec failed before start")}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "terminal-start-failure", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	_, err := environment.open.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "camp attach") {
		t.Fatalf("resumed Open() error = %v, want attach guidance", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if loadErr != nil || loaded.State != domain.SessionOpen || len(pending) != 0 {
		t.Fatalf("durable start-failure state = %#v pending=%#v error=%v", loaded, pending, loadErr)
	}
	if len(devpod.ups) != 1 || devpod.sshCalls != 0 {
		t.Fatalf("DevPod calls = up:%d ssh:%d, want up:1 ssh:0", len(devpod.ups), devpod.sshCalls)
	}
}

func TestOpenEntryStartFailureAcceptsFactAppendedBeforeJournalError(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	environment.open.deps.Journal = &factAppendErrorJournal{
		Journal:    environment.open.deps.Journal,
		failOutput: `"started":false`,
		err:        errors.New("snapshot replacement failed after fact append"),
	}
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root", startErr: errors.New("exec failed before start")}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "terminal-fact-append-error", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	_, err := environment.open.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "camp attach") {
		t.Fatalf("resumed Open() error = %v, want attach guidance", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if loadErr != nil || loaded.State != domain.SessionOpen || len(pending) != 0 {
		t.Fatalf("durable append-error state = %#v pending=%#v error=%v", loaded, pending, loadErr)
	}
}

func TestOpenEntryStartAcknowledgmentAcceptsFactAppendedBeforeJournalError(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	log := &factAppendErrorJournal{
		Journal:    environment.open.deps.Journal,
		failOutput: `"started":true`,
		err:        errors.New("snapshot replacement failed after started fact append"),
	}
	environment.open.deps.Journal = log
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root"}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "terminal-started-fact-append-error", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	_, err := environment.open.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "camp attach") {
		t.Fatalf("resumed Open() error = %v, want attach guidance", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if loadErr != nil || loaded.State != domain.SessionOpen || len(pending) != 0 {
		t.Fatalf("durable started-append-error state = %#v pending=%#v error=%v", loaded, pending, loadErr)
	}
	if len(log.terminalOutputs) != 1 || !strings.Contains(log.terminalOutputs[0], `"started":true`) {
		t.Fatalf("terminal facts = %q, want one started acknowledgment", log.terminalOutputs)
	}
}

func TestOpenEntryStartAcknowledgmentRetainsStartedStateWhenInitialFactFails(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	log := &factAppendErrorJournal{
		Journal:             environment.open.deps.Journal,
		failBeforeOutput:    `"started":true`,
		failBeforeRemaining: 1,
		err:                 errors.New("started fact failed before append"),
	}
	environment.open.deps.Journal = log
	devpod := &unknownOutcomeWorkspaceDevPod{folder: "/workspaces/root"}
	environment.open.deps.DevPod = devpod
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "terminal-started-fact-preappend-error", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ErrAmbiguous", err)
	}
	_, err := environment.open.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "camp attach") {
		t.Fatalf("resumed Open() error = %v, want attach guidance", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if loadErr != nil || loaded.State != domain.SessionOpen || len(pending) != 0 {
		t.Fatalf("durable preappend-error state = %#v pending=%#v error=%v", loaded, pending, loadErr)
	}
	if len(log.terminalOutputs) != 1 || !strings.Contains(log.terminalOutputs[0], `"started":true`) {
		t.Fatalf("durable terminal facts = %q, want one started acknowledgment", log.terminalOutputs)
	}
}

func TestOpenReentryAbandonsPendingTerminalEntryWithoutRedispatch(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "terminal-entry-crash", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	result, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	intent := ports.IntentRecord{
		ID: transitionID(request.SessionID, "TerminalEntryDispatched"), SessionID: request.SessionID, Transition: "TerminalEntryDispatched",
		Attempt: 1, Timestamp: result.Snapshot.UpdatedAt, Input: safeJSON(struct {
			ID      string `json:"id"`
			Workdir string `json:"workdir"`
		}{ID: result.WorkspaceID, Workdir: result.MappedTarget}),
	}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}

	_, err = environment.open.Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "camp attach") {
		t.Fatalf("re-entry error = %v, want attach guidance", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), request.SessionID)
	if loadErr != nil || loaded.State != domain.SessionOpen || len(pending) != 0 {
		t.Fatalf("reconciled terminal crash state = %#v pending=%#v error=%v", loaded, pending, loadErr)
	}
	if len(environment.devpod.ssh) != 1 {
		t.Fatalf("terminal entry calls = %d, want original call only", len(environment.devpod.ssh))
	}
}

type factAppendErrorJournal struct {
	ports.Journal
	failOutput          string
	failBeforeOutput    string
	failBeforeRemaining int
	err                 error
	terminalOutputs     []string
}

func (j *factAppendErrorJournal) RecordFact(ctx context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	output := string(fact.Output)
	if fact.Transition == "TerminalEntryDispatched" && j.failBeforeRemaining > 0 && strings.Contains(output, j.failBeforeOutput) {
		j.failBeforeRemaining--
		return j.err
	}
	if err := j.Journal.RecordFact(ctx, fact, snapshot); err != nil {
		return err
	}
	if fact.Transition == "TerminalEntryDispatched" {
		j.terminalOutputs = append(j.terminalOutputs, output)
		if strings.Contains(output, j.failOutput) {
			return j.err
		}
	}
	return nil
}

type workspaceUpSettlementFailureJournal struct {
	ports.Journal
	err error
}

func (j *workspaceUpSettlementFailureJournal) RecordFact(ctx context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	if fact.Transition == "WorkspaceUp" {
		return j.err
	}
	return j.Journal.RecordFact(ctx, fact, snapshot)
}

type unknownOutcomeWorkspaceDevPod struct {
	ups                             []devpodadapter.UpOptions
	listCalls                       int
	statusCalls                     int
	statusHasDeadline               []bool
	folder                          string
	folderCalls                     int
	sshCalls                        int
	sshErr                          error
	startErr                        error
	omitStatusIdentity              bool
	entryStarted                    chan struct{}
	releaseEntry                    chan struct{}
	workspaceAbsent                 bool
	upResults                       []ports.Result
	upErrors                        []error
	statusStates                    []devpodadapter.WorkspaceState
	replacementSourceAfterFirstList string
	startupReady                    bool
	startupObservations             int
}

func (d *unknownOutcomeWorkspaceDevPod) Up(_ context.Context, options devpodadapter.UpOptions) (ports.Result, error) {
	d.ups = append(d.ups, options)
	index := len(d.ups) - 1
	if index < len(d.upErrors) {
		return d.upResults[index], d.upErrors[index]
	}
	return ports.Result{}, ports.ErrAmbiguous
}

func (d *unknownOutcomeWorkspaceDevPod) StatusInContext(ctx context.Context, devpodContext, workspaceID string) (devpodadapter.WorkspaceStatus, error) {
	d.statusCalls++
	_, hasDeadline := ctx.Deadline()
	d.statusHasDeadline = append(d.statusHasDeadline, hasDeadline)
	state := devpodadapter.StateRunning
	if index := d.statusCalls - 1; index < len(d.statusStates) {
		state = d.statusStates[index]
	}
	if d.omitStatusIdentity {
		return devpodadapter.WorkspaceStatus{ID: workspaceID, State: state}, nil
	}
	provider := ""
	if len(d.ups) != 0 {
		provider = d.ups[0].Provider
	}
	return devpodadapter.WorkspaceStatus{ID: workspaceID, Context: devpodContext, Provider: provider, State: state}, nil
}

func (d *unknownOutcomeWorkspaceDevPod) ListInContext(_ context.Context, devpodContext string) ([]devpodadapter.Workspace, error) {
	d.listCalls++
	if len(d.ups) == 0 || d.workspaceAbsent {
		return nil, nil
	}
	up := d.ups[0]
	source := up.WorkspacePath
	if up.SourceMode == devpodadapter.SourceModeBootstrap {
		source = up.BootstrapPath
	}
	if d.listCalls > 1 && d.replacementSourceAfterFirstList != "" {
		source = d.replacementSourceAfterFirstList
	}
	return []devpodadapter.Workspace{{
		ID: up.WorkspaceID, Provider: devpodadapter.WorkspaceProvider{Name: up.Provider},
		Source: devpodadapter.WorkspaceSource{LocalFolder: source}, Context: devpodContext,
	}}, nil
}

func (d *unknownOutcomeWorkspaceDevPod) ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error) {
	d.folderCalls++
	if d.folder == "" {
		return "", errors.New("unexpected workspace folder resolution")
	}
	return d.folder, nil
}

func (d *unknownOutcomeWorkspaceDevPod) SSH(_ context.Context, options devpodadapter.SSHOptions) (ports.Result, error) {
	d.sshCalls++
	if len(options.ForwardedArgv) != 0 {
		d.startupObservations++
		if !d.startupReady {
			receiptBody, err := json.Marshal(remoteworker.ErrorReceipt{Status: "error", Code: "remote_worker_failed", Diagnostic: "remote activation receipt is incomplete"})
			if err != nil {
				return ports.Result{ExitCode: 1}, err
			}
			envelope, err := json.Marshal(remoteworker.Result{SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: remoteworker.OperationObserve, Receipt: receiptBody})
			if err != nil {
				return ports.Result{ExitCode: 1}, err
			}
			if _, err := options.Stdout.Write(append(envelope, '\n')); err != nil {
				return ports.Result{ExitCode: 1}, err
			}
			return ports.Result{ExitCode: 1}, errors.New("remote startup receipts are missing")
		}
		body, err := io.ReadAll(options.Stdin)
		if err != nil {
			return ports.Result{ExitCode: 1}, err
		}
		var request remoteworker.Request
		if err := json.Unmarshal(body, &request); err != nil {
			return ports.Result{ExitCode: 1}, err
		}
		receipt := remoteworker.StartupReceipt{
			Status:     "ready",
			Activation: remoteworker.ActivationReceipt{Status: "completed", SourceImage: request.Expected.SourceImage, LocalImage: request.Expected.Image},
			Hydration: remoteworker.HydrationReceipt{Status: "completed", SessionID: request.SessionID, WorkspaceRoot: request.WorkspaceRoot,
				RuntimeRoot: request.RuntimeRoot, ManifestPath: request.ManifestPath, Expected: request.Expected, RootSHA256: strings.Repeat("a", 64)},
		}
		receiptBody, err := json.Marshal(receipt)
		if err != nil {
			return ports.Result{ExitCode: 1}, err
		}
		envelope, err := json.Marshal(remoteworker.Result{SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: remoteworker.OperationObserve, Receipt: receiptBody})
		if err != nil {
			return ports.Result{ExitCode: 1}, err
		}
		if _, err := options.Stdout.Write(append(envelope, '\n')); err != nil {
			return ports.Result{ExitCode: 1}, err
		}
		return ports.Result{}, nil
	}
	if d.entryStarted != nil {
		close(d.entryStarted)
	}
	if d.releaseEntry != nil {
		<-d.releaseEntry
	}
	return ports.Result{}, d.sshErr
}

func (d *unknownOutcomeWorkspaceDevPod) SSHWithStart(ctx context.Context, options devpodadapter.SSHOptions, started func() error) (ports.Result, error) {
	if d.startErr != nil {
		return ports.Result{}, d.startErr
	}
	if err := started(); err != nil {
		return ports.Result{}, err
	}
	return d.SSH(ctx, options)
}
