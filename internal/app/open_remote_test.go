package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/archive"
	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	imageops "github.com/joshyorko/camp/internal/images"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
)

func TestOpenRemoteBranchUsesSourceGenerationAndReentryDoesNotRehydrate(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	request := OpenRequest{
		Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD",
		EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker", Machine: "machine-a",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	first, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("remote Open() error = %v", err)
	}
	if first.Snapshot.OpenedGeneration == nil || first.Snapshot.OpenedGeneration.Generation != 42 || first.Snapshot.CurrentBase == nil || first.Snapshot.CurrentBase.Generation != 42 {
		t.Fatalf("opened checkpoint = %#v current=%#v", first.Snapshot.OpenedGeneration, first.Snapshot.CurrentBase)
	}
	if environment.leases.branchCalls != 1 || environment.leases.acquireCalls != 0 || environment.leases.owner.SessionID != first.Snapshot.SessionID {
		t.Fatalf("lease calls = branch:%d acquire:%d owner:%#v", environment.leases.branchCalls, environment.leases.acquireCalls, environment.leases.owner)
	}
	if environment.hydrator.calls != 1 || environment.hydrator.request.Generation.Generation != 42 || environment.hydrator.request.Token == "" || len(environment.hydrator.request.Token) != 64 {
		t.Fatalf("hydration request = %#v calls=%d", environment.hydrator.request, environment.hydrator.calls)
	}
	if first.Snapshot.Materialization.Mode != domain.MaterializationCreated || !first.Snapshot.Materialization.CleanupPermitted {
		t.Fatalf("remote materialization = %#v", first.Snapshot.Materialization)
	}
	if len(environment.devpod.ups) != 1 || environment.devpod.ups[0].CampEnvironment == nil || environment.devpod.ups[0].CampEnvironment.Checkpoint != "42" {
		t.Fatalf("DevPod environment = %#v", environment.devpod.ups)
	}
	if environment.devpod.ups[0].Context != "default" || environment.devpod.ups[0].Provider != "docker" {
		t.Fatalf("DevPod selection = %#v", environment.devpod.ups[0])
	}

	second, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("remote re-entry error = %v", err)
	}
	if second.Snapshot.SessionID != first.Snapshot.SessionID || environment.hydrator.calls != 1 || environment.leases.branchCalls != 1 || environment.leases.acquireCalls != 0 || len(environment.devpod.ups) != 1 {
		t.Fatalf("re-entry repeated lifecycle: snapshot=%#v hydrate=%d leases=%#v ups=%d", second.Snapshot, environment.hydrator.calls, environment.leases, len(environment.devpod.ups))
	}
}

func TestOpenRemoteUsesPreparedBootstrapSourceForExactlyOneDevPodUp(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	dataPlane := &recordingRemoteDataPlane{bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap")}
	environment.open.deps.RemoteDataPlane = dataPlane
	result, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"}, Mode: domain.SessionReadWrite,
		RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "ssh", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("remote Open() error = %v", err)
	}
	if dataPlane.calls != 1 {
		t.Fatalf("remote data plane calls = %d, want 1", dataPlane.calls)
	}
	if dataPlane.requests[0].AttemptID != result.Snapshot.SessionID+"-hauler-kit-v1" {
		t.Fatalf("remote data plane attempt = %q", dataPlane.requests[0].AttemptID)
	}
	if result.Snapshot.Recovery.RemoteDataPlane == nil || result.Snapshot.Recovery.RemoteDataPlane.Mode != domain.DataPlaneHaulerKitV1 {
		t.Fatalf("remote data plane record = %#v", result.Snapshot.Recovery.RemoteDataPlane)
	}
	if len(environment.devpod.ups) != 1 {
		t.Fatalf("DevPod up calls = %#v", environment.devpod.ups)
	}
	up := environment.devpod.ups[0]
	if up.SourceMode != devpodadapter.SourceModeBootstrap || up.BootstrapPath != dataPlane.bootstrapRoot || up.WorkspacePath == dataPlane.bootstrapRoot {
		t.Fatalf("DevPod source = %#v", up)
	}
}

func TestOpenLocalProviderDoesNotSelectOrPrepareRemoteDataPlane(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	dataPlane := &recordingRemoteDataPlane{bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap")}
	environment.open.deps.RemoteDataPlane = dataPlane
	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "local-provider-open", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", LocalProvider: true, Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("local-provider Open() error = %v", err)
	}
	if dataPlane.calls != 0 || result.Snapshot.Recovery.RemoteDataPlane != nil {
		t.Fatalf("local-provider remote data plane = calls:%d record:%#v", dataPlane.calls, result.Snapshot.Recovery.RemoteDataPlane)
	}
	if len(environment.devpod.ups) != 1 || environment.devpod.ups[0].SourceMode == devpodadapter.SourceModeBootstrap {
		t.Fatalf("local-provider DevPod up = %#v", environment.devpod.ups)
	}
}

func TestOpenRemoteUnknownWorkspaceOutcomeReusesRecordedKitAttempt(t *testing.T) {
	environment := newRemoteOpenTestEnvironment(t)
	dataPlane := &recordingRemoteDataPlane{bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap")}
	environment.open.deps.RemoteDataPlane = dataPlane
	devpod := &unknownOutcomeWorkspaceDevPod{
		folder: "/workspaces/root", upResults: []ports.Result{{}}, upErrors: []error{ports.ErrAmbiguous},
	}
	environment.open.deps.DevPod = devpod
	request := OpenRequest{
		SessionID: "remote-unknown-up", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "ssh", Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("first Open() error = %v, want ambiguous", err)
	}
	if dataPlane.calls != 1 || len(devpod.ups) != 1 {
		t.Fatalf("first attempt = preparations:%d ups:%d", dataPlane.calls, len(devpod.ups))
	}
	result, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("reconciled Open() error = %v", err)
	}
	if dataPlane.calls != 1 || len(devpod.ups) != 1 {
		t.Fatalf("recovery repeated logical kit or DevPod up: preparations:%d ups:%d", dataPlane.calls, len(devpod.ups))
	}
	if result.Snapshot.Recovery.RemoteDataPlane == nil ||
		result.Snapshot.Recovery.RemoteDataPlane.AttemptID != "remote-unknown-up-hauler-kit-v1" {
		t.Fatalf("recovered remote data plane = %#v", result.Snapshot.Recovery.RemoteDataPlane)
	}
}

func TestOpenRemoteDataPlaneFailurePreventsDevPodUp(t *testing.T) {
	environment := newRemoteOpenTestEnvironment(t)
	dataPlane := &recordingRemoteDataPlane{
		bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap"),
		err:           errors.New("kit verification failed"),
	}
	environment.open.deps.RemoteDataPlane = dataPlane
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "remote-preparation-failure", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "ssh", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, dataPlane.err) {
		t.Fatalf("Open() error = %v, want preparation failure", err)
	}
	if len(environment.devpod.ups) != 0 {
		t.Fatalf("DevPod up called after preparation failure: %#v", environment.devpod.ups)
	}
}

func TestOpenRemoteReentryReverifiesCompletedBootstrapBeforeDevPodUp(t *testing.T) {
	environment := newRemoteOpenTestEnvironment(t)
	dataPlane := &recordingRemoteDataPlane{bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap")}
	environment.open.deps.RemoteDataPlane = dataPlane
	cut := errors.New("target cut")
	environment.open.deps.Target = &failOnceOpenTarget{
		next: &openTargetResolver{events: environment.devpod.events}, err: cut,
	}
	request := OpenRequest{
		SessionID: "remote-bootstrap-reentry", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "ssh", Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, cut) {
		t.Fatalf("first Open() error = %v", err)
	}
	dataPlane.err = errors.New("tampered completed bootstrap")
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, dataPlane.err) {
		t.Fatalf("reentry Open() error = %v, want bootstrap verification failure", err)
	}
	if dataPlane.calls != 2 || len(environment.devpod.ups) != 0 {
		t.Fatalf("reentry verification boundary = calls:%d ups:%d", dataPlane.calls, len(environment.devpod.ups))
	}
}

func TestOpenSchemaV1LegacySnapshotDoesNotUpgradeToHaulerKitInPlace(t *testing.T) {
	environment := newRemoteOpenTestEnvironment(t)
	legacy := &legacyRemoteDataPlaneJournal{Journal: environment.open.deps.Journal}
	environment.open.deps.Journal = legacy
	dataPlane := &recordingRemoteDataPlane{bootstrapRoot: filepath.Join(environment.paths.DataRoot, "bootstrap")}
	environment.open.deps.RemoteDataPlane = dataPlane
	request := OpenRequest{
		SessionID: "legacy-schema-v1", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "ssh", Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, errLegacyJournalCut) {
		t.Fatalf("legacy setup Open() error = %v", err)
	}
	result, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("legacy Open() error = %v", err)
	}
	if dataPlane.calls != 0 || result.Snapshot.Recovery.RemoteDataPlane != nil {
		t.Fatalf("legacy session was upgraded: calls:%d record:%#v", dataPlane.calls, result.Snapshot.Recovery.RemoteDataPlane)
	}
	if len(environment.devpod.ups) != 1 || environment.devpod.ups[0].SourceMode == devpodadapter.SourceModeBootstrap {
		t.Fatalf("legacy DevPod source = %#v", environment.devpod.ups)
	}
}

func TestOpenRemoteRestoresHydratedNamedImagesBeforeEntry(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	inventory := domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: time.Unix(42, 0).UTC(), Images: []domain.Image{{
		OriginalTags: []string{"127.0.0.1:5000/camp-acceptance:named"}, CapturedReference: "127.0.0.1:5000/camp/acceptance:captured",
		CapturedManifestDigest: "sha256:" + strings.Repeat("b", 64),
	}}}
	environment.hydrator.inventory = &inventory
	restorer := &recordingOpenImageRestorer{events: environment.hydrator.events}
	environment.open.deps.Images = restorer
	environment.open.deps.Services = &openServices{events: environment.hydrator.events, registry: true}
	environment.open.deps.Forwarders = &openForwarders{events: environment.hydrator.events}

	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "image-restore", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		EntryMode: domain.EntryTerminal, Context: "default", Provider: "room-of-requirement",
		Runtime: environment.runtime, Backend: environment.backend, RemoteAvailable: true,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if restorer.calls != 1 {
		t.Fatalf("image restore calls = %d, want 1", restorer.calls)
	}
	if !reflect.DeepEqual(restorer.request.Inventory, inventory) {
		t.Fatalf("image restore inventory = %#v, want %#v", restorer.request.Inventory, inventory)
	}
	if restorer.request.Scope.WorkspaceID != result.WorkspaceID || restorer.request.Scope.Context != "default" {
		t.Fatalf("image restore scope = %#v", restorer.request.Scope)
	}
	if restorer.request.RegistryAuthority != "127.0.0.1:5000" || restorer.request.RegistryEndpoint != "http://127.0.0.1:5000" {
		t.Fatalf("image restore registry = %#v", restorer.request)
	}
	if !result.Snapshot.Workspace.ImagesRestored {
		t.Fatal("open snapshot did not durably record restored workspace images")
	}
	if got := strings.Join(*environment.hydrator.events, ","); !strings.Contains(got, "forward:fileserver,images,ssh") {
		t.Fatalf("events = %q, want image restore after forwarders and before entry", got)
	}
	if _, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite, EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "room-of-requirement", Runtime: environment.runtime, Backend: environment.backend,
	}); err != nil {
		t.Fatalf("reentry Open() error = %v", err)
	}
	if restorer.calls != 1 {
		t.Fatalf("image restore calls after reentry = %d, want 1", restorer.calls)
	}
}

func TestOpenReplaysPendingWorkspaceImageRestoreAgainstExactWorkspace(t *testing.T) {
	t.Parallel()
	events := []string{}
	restorer := &recordingOpenImageRestorer{events: &events}
	open := NewOpen(OpenDependencies{Images: restorer, Clock: fixedAppClock{now: time.Unix(100, 0).UTC()}})
	inventory := domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
		CapturedReference: "127.0.0.1:5000/camp/app:captured", CapturedManifestDigest: "sha256:" + strings.Repeat("c", 64),
	}}}
	request := imageops.RestoreRequest{
		Scope:             imageops.EngineScope{Context: "default", WorkspaceID: "brain-main"},
		RegistryAuthority: "127.0.0.1:5000", RegistryEndpoint: "http://127.0.0.1:5000", Inventory: inventory,
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(inventory)
	if err := os.WriteFile(filepath.Join(root, ".camp", "images.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{
		SessionID: "image-replay", Materialization: domain.Materialization{CanonicalPath: root},
		Workspace: domain.WorkspaceRecord{ID: "brain-main", Context: "default"}, Images: inventory,
		Services: []domain.ServiceUnitRecord{{
			Name: "registry", DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
			Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: 5000},
			Child:   domain.ProcessRecord{Argv: []string{"hauler", "--directory", "/tmp/camp-registry"}},
		}},
	}
	intent := ports.IntentRecord{ID: "image-replay-intent", SessionID: snapshot.SessionID, Transition: "WorkspaceImagesRestored", Timestamp: time.Unix(99, 0).UTC(), Input: safeJSON(request)}

	fact, reconciled, err := open.observeWorkspaceImagesRestored(context.Background(), snapshot, intent)
	if err != nil {
		t.Fatalf("observeWorkspaceImagesRestored() error = %v", err)
	}
	if restorer.calls != 1 || !reconciled.Workspace.ImagesRestored || fact.IntentID != intent.ID {
		t.Fatalf("replay calls=%d workspace=%#v fact=%#v", restorer.calls, reconciled.Workspace, fact)
	}
}

func TestOpenRemoteReentryRejectsInvalidOwnershipBeforeTargetOrSSH(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "invalid-reentry-ownership-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	markerPath := filepath.Join(first.Snapshot.Materialization.CanonicalPath, ".camp", "runtime", "ownership.json")
	if err := os.WriteFile(markerPath, []byte(`{"token":"attacker"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	upCount, sshCount := len(environment.devpod.ups), len(environment.devpod.ssh)
	eventCount := len(*environment.devpod.events)
	_, err = environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, capsule.ErrOwnershipMismatch) {
		t.Fatalf("re-entry Open() error = %v, want ErrOwnershipMismatch", err)
	}
	if len(environment.devpod.ups) != upCount || len(environment.devpod.ssh) != sshCount || len(*environment.devpod.events) != eventCount {
		t.Fatalf("re-entry effects after ownership mismatch: ups=%d ssh=%d events=%v", len(environment.devpod.ups), len(environment.devpod.ssh), *environment.devpod.events)
	}
}

func TestOpenReentryRejectsCreatedMaterializationOutsideSessionPathBeforeSideEffects(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "created-path-reentry-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	otherRoot := filepath.Join(environment.ownership.MaterializationRoot(), "brain", "feature", "other-session")
	if err := os.MkdirAll(otherRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	other, err := environment.ownership.MarkCreated(otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := first.Snapshot
	snapshot.Materialization = other
	eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
	_, err = environment.open.reenter(context.Background(), snapshot, OpenRequest{
		SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, Target: "MemoryD", EntryMode: domain.EntryTerminal,
	})
	if !errors.Is(err, capsule.ErrOwnershipMismatch) {
		t.Fatalf("reenter() error = %v, want ErrOwnershipMismatch", err)
	}
	if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
		t.Fatalf("re-entry effects after path mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
	}
}

func TestOpenReentryRejectsWorkspaceStagingRootMismatchBeforeSideEffects(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "staging-root-reentry-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	snapshot := first.Snapshot
	snapshot.Workspace.StagingRoot = t.TempDir()
	eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
	if _, err := environment.open.reenter(context.Background(), snapshot, OpenRequest{
		SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, Target: "MemoryD", EntryMode: domain.EntryTerminal,
	}); err == nil {
		t.Fatal("reenter() accepted a staging root different from the materialization")
	}
	if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
		t.Fatalf("re-entry effects after staging mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
	}
}

func TestOpenReentryRejectsWorkspaceLocalFolderMismatchBeforeSideEffects(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "local-folder-reentry-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	snapshot := first.Snapshot
	snapshot.Workspace.LocalFolder = t.TempDir()
	eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
	if _, err := environment.open.reenter(context.Background(), snapshot, OpenRequest{
		SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, Target: "MemoryD", EntryMode: domain.EntryTerminal,
	}); err == nil {
		t.Fatal("reenter() accepted a local folder different from the materialization")
	}
	if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
		t.Fatalf("re-entry effects after local-folder mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
	}
}

func TestOpenReentryRequiresPersistedEffectiveRootBeforeSideEffects(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "effective-root-reentry-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	snapshot := first.Snapshot
	snapshot.Workspace.EffectiveRoot = ""
	eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
	if _, err := environment.open.reenter(context.Background(), snapshot, OpenRequest{
		SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, Target: "MemoryD", EntryMode: domain.EntryTerminal,
	}); err == nil {
		t.Fatal("reenter() accepted a missing effective workspace root")
	}
	if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
		t.Fatalf("re-entry effects after missing effective root: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
	}
}

func TestOpenReentryRejectsNonDeterministicWorkspaceIDBeforeSideEffects(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "workspace-id-reentry-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	snapshot := first.Snapshot
	snapshot.Workspace.ID = "attacker-workspace"
	eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
	if _, err := environment.open.reenter(context.Background(), snapshot, OpenRequest{
		SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, Target: "MemoryD", EntryMode: domain.EntryTerminal,
	}); err == nil {
		t.Fatal("reenter() accepted a non-deterministic workspace ID")
	}
	if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
		t.Fatalf("re-entry effects after workspace ID mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
	}
}

func TestOpenReentryRejectsExplicitSessionCapsuleOrBranchMismatch(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		capsule string
		branch  string
	}{
		{name: "capsule", capsule: "other", branch: "feature"},
		{name: "branch", capsule: "brain", branch: "other"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := newRemoteOpenTestEnvironment(t)
			const sessionID = "identity-reentry-session"
			_, err := environment.open.Run(context.Background(), OpenRequest{
				SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
				Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
				Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
			})
			if err != nil {
				t.Fatalf("first Open() error = %v", err)
			}
			eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
			_, err = environment.open.Run(context.Background(), OpenRequest{
				SessionID: sessionID, Capsule: test.capsule, Branch: test.branch, Target: "MemoryD", EntryMode: domain.EntryTerminal,
				Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
			})
			if !errors.Is(err, ErrOpenSessionMismatch) {
				t.Fatalf("re-entry Open() error = %v, want ErrOpenSessionMismatch", err)
			}
			if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
				t.Fatalf("re-entry effects after identity mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
			}
		})
	}
}

func TestOpenReentryRejectsWorkspaceContextOrProviderMismatchBeforeSideEffects(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*domain.JournalSnapshot)
	}{
		{name: "context", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Workspace.Context = "attacker" }},
		{name: "provider", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Workspace.Provider = "attacker" }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := newRemoteOpenTestEnvironment(t)
			first, err := environment.open.Run(context.Background(), OpenRequest{
				SessionID: "routing-reentry-" + test.name, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
				Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
				Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
			})
			if err != nil {
				t.Fatalf("first Open() error = %v", err)
			}
			snapshot := first.Snapshot
			test.mutate(&snapshot)
			eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
			_, err = environment.open.reenter(context.Background(), snapshot, OpenRequest{
				SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch,
				Context: "default", Provider: "docker", Target: "MemoryD", EntryMode: domain.EntryTerminal,
			})
			if !errors.Is(err, ErrOpenSessionMismatch) {
				t.Fatalf("reenter() error = %v, want ErrOpenSessionMismatch", err)
			}
			if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
				t.Fatalf("re-entry effects after routing mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
			}
		})
	}
}

func TestOpenReentryRejectsExplicitModeMismatchBeforeSideEffects(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "mode-reentry-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
	_, err = environment.open.reenter(context.Background(), first.Snapshot, OpenRequest{
		SessionID: first.Snapshot.SessionID, Capsule: first.Snapshot.Capsule, Branch: first.Snapshot.Lineage.Branch, Mode: domain.SessionReadOnly,
		Context: first.Snapshot.Workspace.Context, Provider: first.Snapshot.Workspace.Provider, Target: "MemoryD", EntryMode: domain.EntryTerminal,
	})
	if !errors.Is(err, ErrOpenSessionMismatch) {
		t.Fatalf("reenter() error = %v, want ErrOpenSessionMismatch", err)
	}
	if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
		t.Fatalf("re-entry effects after mode mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
	}
}

func TestOpenReentryRejectsIncoherentRemoteSourceOrModeBeforeSideEffects(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*domain.JournalSnapshot)
	}{
		{name: "source-kind", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.Source.Kind = domain.SourceDecisionAdopted }},
		{name: "source-lineage", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.Source.Lineage = nil }},
		{name: "source-generation", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.Source.Generation = nil }},
		{name: "opened-generation", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.OpenedGeneration = nil }},
		{name: "current-base", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.CurrentBase = nil }},
		{name: "generation-mismatch", mutate: func(snapshot *domain.JournalSnapshot) {
			other := domain.GenerationRef{Generation: snapshot.Recovery.Source.Generation.Generation + 1, ArchiveSHA256: snapshot.Recovery.Source.Generation.ArchiveSHA256}
			snapshot.Recovery.Source.Generation = &other
		}},
		{name: "cleanup-policy", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.Cleanup.RemoveOwnedMaterialization = false }},
		{name: "read-only-with-lease", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Mode = domain.SessionReadOnly }},
		{name: "writer-without-lease", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Lease = domain.LeaseRecord{} }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := newRemoteOpenTestEnvironment(t)
			first, err := environment.open.Run(context.Background(), OpenRequest{
				SessionID: "source-mode-reentry-" + test.name, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
				Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
				Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
			})
			if err != nil {
				t.Fatalf("first Open() error = %v", err)
			}
			snapshot := first.Snapshot
			test.mutate(&snapshot)
			eventCount, sshCount := len(*environment.devpod.events), len(environment.devpod.ssh)
			_, err = environment.open.reenter(context.Background(), snapshot, OpenRequest{
				SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch,
				Context: snapshot.Workspace.Context, Provider: snapshot.Workspace.Provider, Target: "MemoryD", EntryMode: domain.EntryTerminal,
			})
			if !errors.Is(err, ErrOpenSessionMismatch) {
				t.Fatalf("reenter() error = %v, want ErrOpenSessionMismatch", err)
			}
			if len(*environment.devpod.events) != eventCount || len(environment.devpod.ssh) != sshCount {
				t.Fatalf("re-entry effects after source/mode mismatch: events=%v ssh=%d", *environment.devpod.events, len(environment.devpod.ssh))
			}
		})
	}
}

func TestOpenJournalsRemoteLeaseAcquisitionIntentAndFact(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "lease-journal-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if result.Snapshot.Lease.Lease == nil || result.Snapshot.Lease.Revision == "" {
		t.Fatalf("lease was not persisted in snapshot: %#v", result.Snapshot.Lease)
	}
	body, err := os.ReadFile(filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"transition":"RemoteLeaseAcquisition"`)) {
		t.Fatalf("journal has no lease acquisition transition: %s", body)
	}
	var receipt struct {
		Machine          string                `json:"machine"`
		OpenedGeneration *domain.GenerationRef `json:"openedGeneration"`
		CreatedAt        time.Time             `json:"createdAt"`
		HeartbeatAt      time.Time             `json:"heartbeatAt"`
		ExpiresAt        time.Time             `json:"expiresAt"`
		BranchSource     bool                  `json:"branchSource"`
		ObservedRevision string                `json:"observedRevision"`
	}
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		var entry struct {
			Kind string            `json:"kind"`
			Fact *ports.FactRecord `json:"fact"`
		}
		if len(line) == 0 || json.Unmarshal(line, &entry) != nil || entry.Kind != "fact" || entry.Fact == nil || entry.Fact.Transition != "RemoteLeaseAcquisition" {
			continue
		}
		if err := json.Unmarshal(entry.Fact.Output, &receipt); err != nil {
			t.Fatal(err)
		}
	}
	if receipt.Machine != "machine-a" || receipt.OpenedGeneration == nil || *receipt.OpenedGeneration != remoteOpenGeneration() ||
		!receipt.CreatedAt.Equal(time.Unix(100, 0).UTC()) || !receipt.HeartbeatAt.Equal(receipt.CreatedAt) || !receipt.ExpiresAt.Equal(receipt.CreatedAt.Add(30*time.Minute)) ||
		!receipt.BranchSource || receipt.ObservedRevision != "main-r1" {
		t.Fatalf("lease receipt = %#v", receipt)
	}
	loaded, pending, err := environment.open.deps.Journal.Load(context.Background(), result.Snapshot.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 || loaded.Lease.Lease == nil || loaded.Lease.Revision == "" {
		t.Fatalf("journal lease state = %#v pending=%#v", loaded.Lease, pending)
	}
}

func TestOpenRejectsMismatchedReturnedLeaseBeforeRecordingFact(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	environment.leases.mutate = func(token *coordination.LeaseToken) {
		token.Lease.OpenedGeneration = &domain.GenerationRef{Generation: 41, ArchiveSHA256: strings.Repeat("b", 64)}
	}
	const sessionID = "mismatched-returned-lease-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err == nil {
		t.Fatal("Open() accepted a lease token for the wrong opened generation")
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("mismatched lease snapshot=%#v pending=%#v error=%v", loaded.Lease, pending, loadErr)
	}
	if environment.hydrator.calls != 0 || len(environment.devpod.ups) != 0 {
		t.Fatalf("effects after mismatched lease: hydrate=%d up=%d", environment.hydrator.calls, len(environment.devpod.ups))
	}
}

func TestOpenReconcilesUnknownRemoteLeaseAcquisitionWithoutReacquiring(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	leases := &unknownOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	environment.open.deps.Leases = leases
	const sessionID = "lease-outcome-unknown-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	before, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("pre-reconciliation snapshot lease = %#v pending=%#v", before.Lease, pending)
	}

	reconciled, err := environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciled.Lease.Lease == nil || reconciled.Lease.Lease.SessionID != sessionID || reconciled.Lease.Revision != "lease-r1" {
		t.Fatalf("reconciled lease = %#v", reconciled.Lease)
	}
	if reconciled.OpenedGeneration == nil || *reconciled.OpenedGeneration != remoteOpenGeneration() || reconciled.CurrentBase == nil || *reconciled.CurrentBase != remoteOpenGeneration() {
		t.Fatalf("reconciled baseline opened=%#v current=%#v", reconciled.OpenedGeneration, reconciled.CurrentBase)
	}
	if reconciled.CurrentPointer != nil || reconciled.ExpectedPointerRevision != "" || reconciled.Recovery.Source.Kind != domain.SourceDecisionRemote ||
		reconciled.Recovery.Source.Lineage == nil || *reconciled.Recovery.Source.Lineage != (domain.Lineage{Branch: "main"}) ||
		reconciled.Recovery.Source.Generation == nil || *reconciled.Recovery.Source.Generation != remoteOpenGeneration() {
		t.Fatalf("reconciled absent-branch source pointer=%#v revision=%q source=%#v", reconciled.CurrentPointer, reconciled.ExpectedPointerRevision, reconciled.Recovery.Source)
	}
	if leases.branchCalls != 1 || leases.readCalls != 1 {
		t.Fatalf("lease calls after reconciliation = branch:%d read:%d, want branch:1 read:1", leases.branchCalls, leases.readCalls)
	}
	_, pending, err = environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("post-reconciliation pending=%#v error=%v", pending, err)
	}
}

func TestOpenReconcileAbsentLeaseRemainsPendingWithoutReacquiring(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	leases := &absentOutcomeOpenLeases{}
	environment.open.deps.Leases = leases
	const sessionID = "lease-outcome-absent-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	_, err = environment.open.Reconcile(context.Background(), sessionID)
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Reconcile() error = %v, want ErrNotFound", err)
	}
	if leases.branchCalls != 1 || leases.readCalls != 1 {
		t.Fatalf("lease calls after reconciliation = branch:%d read:%d, want branch:1 read:1", leases.branchCalls, leases.readCalls)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("absent snapshot lease=%#v pending=%#v error=%v", loaded.Lease, pending, loadErr)
	}
}

func TestOpenReconcileRejectsApproximateLeaseWithoutRecordingFact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*coordination.LeaseToken)
	}{
		{name: "schema", mutate: func(token *coordination.LeaseToken) { token.Lease.SchemaVersion = 0 }},
		{name: "opened generation", mutate: func(token *coordination.LeaseToken) {
			token.Lease.OpenedGeneration = &domain.GenerationRef{Generation: 41, ArchiveSHA256: strings.Repeat("b", 64)}
		}},
		{name: "created time", mutate: func(token *coordination.LeaseToken) { token.Lease.CreatedAt = token.Lease.CreatedAt.Add(time.Second) }},
		{name: "heartbeat time", mutate: func(token *coordination.LeaseToken) {
			token.Lease.HeartbeatAt = token.Lease.HeartbeatAt.Add(time.Second)
		}},
		{name: "expiry terms", mutate: func(token *coordination.LeaseToken) { token.Lease.ExpiresAt = token.Lease.ExpiresAt.Add(time.Second) }},
		{name: "revision", mutate: func(token *coordination.LeaseToken) { token.Revision = "" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := newRemoteOpenTestEnvironment(t)
			leases := &unknownOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC(), mutate: test.mutate}
			environment.open.deps.Leases = leases
			sessionID := "approximate-lease-" + strings.ReplaceAll(test.name, " ", "-")
			_, err := environment.open.Run(context.Background(), OpenRequest{
				SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
				Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
				Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
			})
			if !errors.Is(err, ports.ErrAmbiguous) {
				t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
			}
			if _, err = environment.open.Reconcile(context.Background(), sessionID); err == nil {
				t.Fatal("Reconcile() accepted an approximate lease")
			}
			loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
			if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || leases.branchCalls != 1 || leases.readCalls != 1 {
				t.Fatalf("snapshot lease=%#v pending=%#v branch=%d read=%d error=%v", loaded.Lease, pending, leases.branchCalls, leases.readCalls, loadErr)
			}
		})
	}
}

func TestOpenReconcileRejectsPointerDriftWithoutRecordingLeaseFact(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	pointers := &recordingOpenPointers{source: remoteOpenPointer()}
	leases := &unknownOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	environment.open.deps.Pointers = pointers
	environment.open.deps.Leases = leases
	const sessionID = "lease-pointer-drift-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	pointers.source.Revision = "main-r2"
	if _, err = environment.open.Reconcile(context.Background(), sessionID); !errors.Is(err, coordination.ErrPointerChanged) {
		t.Fatalf("Reconcile() error = %v, want ErrPointerChanged", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || leases.branchCalls != 1 || leases.readCalls != 0 {
		t.Fatalf("snapshot lease=%#v pending=%#v branch=%d read=%d error=%v", loaded.Lease, pending, leases.branchCalls, leases.readCalls, loadErr)
	}
}

func TestOpenReconcileReadOnlySnapshotNeverObservesOrAcquiresLease(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	pointers := &recordingOpenPointers{source: remoteOpenPointer()}
	leases := &absentOutcomeOpenLeases{}
	environment.open.deps.Pointers = pointers
	environment.open.deps.Leases = leases
	now := time.Unix(100, 0).UTC()
	const sessionID = "read-only-lease-intent-session"
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: sessionID, Capsule: "brain", Lineage: domain.Lineage{Branch: "feature"},
		Mode: domain.SessionReadOnly, State: domain.SessionOpening, CreatedAt: now, UpdatedAt: now,
		Recovery: domain.RecoveryRecord{Objective: domain.RecoveryObjectiveOpen},
	}
	if err := environment.open.deps.Journal.Create(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	source := remoteOpenPointer()
	input := openLeaseAcquisitionInput{
		Capsule: "brain", Lineage: snapshot.Lineage, Owner: coordination.LeaseOwner{SessionID: sessionID, Machine: "machine-a"},
		Observed: &source, Source: &source, ObservedRevision: string(source.Revision), BranchSource: true, Now: now, LeaseTTL: time.Minute,
	}
	intent := ports.IntentRecord{ID: transitionID(sessionID, "RemoteLeaseAcquisition"), SessionID: sessionID, Transition: "RemoteLeaseAcquisition", Attempt: 1, Timestamp: now, Input: safeJSON(input)}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.open.Reconcile(context.Background(), sessionID); !errors.Is(err, ErrOpenReadOnlyLease) {
		t.Fatalf("Reconcile() error = %v, want ErrOpenReadOnlyLease", err)
	}
	if len(pointers.calls) != 0 || leases.readCalls != 0 || leases.branchCalls != 0 {
		t.Fatalf("read-only reconciliation effects: pointers=%v lease-read=%d lease-acquire=%d", pointers.calls, leases.readCalls, leases.branchCalls)
	}
}

func TestOpenReconcileRejectsInconsistentBranchSourceIntent(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	pointers := &recordingOpenPointers{source: remoteOpenPointer()}
	now := time.Unix(100, 0).UTC()
	source := remoteOpenPointer()
	opened := source.Pointer.Generation
	const sessionID = "inconsistent-branch-source-session"
	lease := coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "feature"}, SessionID: sessionID, Machine: "machine-a",
		OpenedGeneration: &opened, CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}, Revision: "lease-r1"}
	leases := &unknownOutcomeOpenLeases{token: lease, available: true}
	environment.open.deps.Pointers = pointers
	environment.open.deps.Leases = leases
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: sessionID, Capsule: "brain", Lineage: domain.Lineage{Branch: "feature"},
		Mode: domain.SessionReadWrite, State: domain.SessionOpening, CreatedAt: now, UpdatedAt: now,
		Recovery: domain.RecoveryRecord{Objective: domain.RecoveryObjectiveOpen},
	}
	if err := environment.open.deps.Journal.Create(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	observed := source
	observed.Revision = "different-observation-r1"
	input := openLeaseAcquisitionInput{
		Capsule: "brain", Lineage: snapshot.Lineage, Owner: coordination.LeaseOwner{SessionID: sessionID, Machine: "machine-a"},
		Observed: &observed, Source: &source, ObservedRevision: string(observed.Revision), BranchSource: true, Now: now, LeaseTTL: time.Minute,
	}
	intent := ports.IntentRecord{ID: transitionID(sessionID, "RemoteLeaseAcquisition"), SessionID: sessionID, Transition: "RemoteLeaseAcquisition", Attempt: 1, Timestamp: now, Input: safeJSON(input)}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.open.Reconcile(context.Background(), sessionID); err == nil {
		t.Fatal("Reconcile() accepted mismatched observed and source pointers")
	}
	_, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v error=%v", pending, err)
	}
}

func TestOpenReconciliationRejectsAnotherSessionsLeaseWithoutReacquiring(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	leases := &conflictingOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	environment.open.deps.Leases = leases
	const sessionID = "lease-outcome-conflict-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	_, err = environment.open.Reconcile(context.Background(), sessionID)
	if !errors.Is(err, coordination.ErrLeaseHeld) {
		t.Fatalf("Reconcile() error = %v, want ErrLeaseHeld", err)
	}
	if leases.branchCalls != 1 || leases.readCalls != 1 {
		t.Fatalf("lease calls after conflict = branch:%d read:%d, want branch:1 read:1", leases.branchCalls, leases.readCalls)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("conflict snapshot lease=%#v pending=%#v error=%v", loaded.Lease, pending, loadErr)
	}
}

func TestOpenRemoteJournalAndArtifactsDoNotPersistConfiguredCredentials(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	environment.runtime.AccessToken = "configured-secret"
	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "credential-free-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadOnly, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("credential-free Open() error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "snapshot.json"),
		filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "journal.jsonl"),
	} {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(body), "configured-secret") {
			t.Fatalf("credential persisted in %s", path)
		}
	}
}

func TestOpenRemotePersistsDurableObjectiveAndHydrationPlanBeforeFirstHydrationEffect(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	inspector := &recoveryInspectingOpenHydrator{
		journal:  environment.open.deps.Journal,
		delegate: environment.hydrator,
	}
	environment.open.deps.Hydrator = inspector
	const sessionID = "durable-hydration-plan"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadOnly, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if inspector.snapshot.Recovery.Objective != domain.RecoveryObjectiveOpen {
		t.Fatalf("durable recovery objective = %#v, want open", inspector.snapshot.Recovery.Objective)
	}
	plan := inspector.snapshot.Recovery.Hydration
	if plan == nil {
		t.Fatal("durable hydration plan is nil")
	}
	wantStage := filepath.Join(environment.paths.SessionRoot, sessionID, "materialization-stage")
	wantFinal := filepath.Join(environment.ownership.MaterializationRoot(), "brain", "feature", sessionID)
	if plan.Token == "" || plan.Token != environment.hydrator.request.Token || plan.StageRoot != wantStage || plan.FinalRoot != wantFinal {
		t.Fatalf("durable hydration plan = %#v, request = %#v", plan, environment.hydrator.request)
	}
	journalBody, err := os.ReadFile(filepath.Join(environment.paths.SessionRoot, sessionID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	foundPlanIntent := false
	for _, line := range bytes.Split(journalBody, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Kind   string              `json:"kind"`
			Intent *ports.IntentRecord `json:"intent"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Kind != "intent" || entry.Intent == nil || entry.Intent.Transition != "MaterializationPlanned" {
			continue
		}
		foundPlanIntent = true
		var input map[string]any
		if err := json.Unmarshal(entry.Intent.Input, &input); err != nil {
			t.Fatal(err)
		}
		if input["token"] == "" || input["stage"] != wantStage || input["final"] != wantFinal || input["stageRoot"] != nil || input["finalRoot"] != nil {
			t.Fatalf("materialization plan intent = %#v, want compatible token/stage/final envelope", input)
		}
	}
	if !foundPlanIntent {
		t.Fatal("journal is missing MaterializationPlanned intent")
	}
}

func TestOpenRemoteUsesHydrationHooksForDurableMaterializationPhases(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	archiveRoot := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(archiveRoot, ".camp", "runtime", "MemoryD"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, ".camp", "capsule.yaml"), []byte("schemaVersion: 1\nid: brain\ndefaultBranch: main\ncreatedAt: 1970-01-01T00:00:01Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, "README.md"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(t.TempDir(), "inner.tar.zst")
	archiveInfo, err := archive.NewTarZstd().Create(context.Background(), archiveRoot, inner)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(inner)
	if err != nil {
		t.Fatal(err)
	}
	digestSum := sha256.Sum256(body)
	digest := hex.EncodeToString(digestSum[:])
	generation := domain.GenerationRef{Generation: 42, ArchiveSHA256: digest}
	pointer := coordination.PointerRecord{Pointer: domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: "brain/generations/42-" + digest + ".tar.zst", Size: archiveInfo.Size, CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
	}, Revision: "main-r1"}
	metadata := domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: pointer.Pointer.ObjectKey, MetadataKey: "brain/generations/42-" + digest + ".json", Size: archiveInfo.Size, CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
		Verified: domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true},
	}
	environment.open.deps.Pointers = &recordingOpenPointers{source: pointer}
	environment.open.deps.Generations = &recordingOpenGenerations{metadata: metadata}
	environment.open.deps.Leases = &recordingOpenLeases{generation: generation, now: time.Unix(100, 0).UTC()}
	environment.open.deps.Hydrator = hydration.NewController(
		&openHydrationStore{body: body, metadata: ports.ObjectMeta{Key: pointer.Pointer.ObjectKey, Size: archiveInfo.Size, SHA256: digest}},
		&openHydrationHauler{inner: inner}, archive.NewTarZstd(), environment.ownership, hydration.Hooks{},
	)
	result, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"}, Mode: domain.SessionReadOnly,
		RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if result.Snapshot.Materialization.Mode != domain.MaterializationCreated || result.Target.Relative != "MemoryD" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Snapshot.Materialization.CanonicalPath, "README.md")); err != nil {
		t.Fatalf("hydrated root = %v", err)
	}
	journalBody, err := os.ReadFile(filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"HydrationMaterializationStageCreated", "HydrationGenerationFetched", "HydrationGenerationLoaded", "HydrationMaterializationExtractComplete", "HydrationMaterializationRenameComplete", "HydrationMaterializationOwnershipFact"} {
		if !bytes.Contains(journalBody, []byte(phase)) {
			t.Fatalf("journal missing hydration phase %q: %s", phase, journalBody)
		}
	}
}

func TestOpenRejectsHydratorResultOutsidePlannedOwnership(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	outside := t.TempDir()
	environment.open.deps.Hydrator = &invalidOpenHydrator{root: outside}
	_, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"}, Mode: domain.SessionReadOnly,
		RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err == nil {
		t.Fatal("Open() accepted a hydrator result outside the planned destination")
	}
	if len(environment.devpod.ups) != 0 {
		t.Fatalf("DevPod started after invalid hydration result: %#v", environment.devpod.ups)
	}
}

type remoteOpenTestEnvironment struct {
	open      *Open
	paths     config.XDGPaths
	backend   config.FileBackend
	runtime   config.Runtime
	ownership *capsule.Ownership
	devpod    *openDevPod
	hydrator  *recordingOpenHydrator
	leases    *recordingOpenLeases
}

type recordingRemoteDataPlane struct {
	bootstrapRoot string
	calls         int
	requests      []RemoteDataPlaneRequest
	err           error
}

var errLegacyJournalCut = errors.New("legacy journal cut")

type legacyRemoteDataPlaneJournal struct {
	ports.Journal
	cut bool
}

type failOnceOpenTarget struct {
	next OpenTargetResolver
	err  error
}

func (r *failOnceOpenTarget) Resolve(ctx context.Context, root, requested string) (target.Result, error) {
	if r.err != nil {
		err := r.err
		r.err = nil
		return target.Result{}, err
	}
	return r.next.Resolve(ctx, root, requested)
}

func (j *legacyRemoteDataPlaneJournal) Create(ctx context.Context, snapshot domain.JournalSnapshot) error {
	snapshot.Recovery.RemoteDataPlane = nil
	return j.Journal.Create(ctx, snapshot)
}

func (j *legacyRemoteDataPlaneJournal) RecordFact(ctx context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	snapshot.Recovery.RemoteDataPlane = nil
	if err := j.Journal.RecordFact(ctx, fact, snapshot); err != nil {
		return err
	}
	if !j.cut && fact.Transition == "DevcontainerResolved" {
		j.cut = true
		return errLegacyJournalCut
	}
	return nil
}

func (r *recordingRemoteDataPlane) Prepare(_ context.Context, request RemoteDataPlaneRequest) (RemoteDataPlaneResult, error) {
	r.calls++
	r.requests = append(r.requests, request)
	if r.err != nil {
		return RemoteDataPlaneResult{}, r.err
	}
	return RemoteDataPlaneResult{
		BootstrapRoot: r.bootstrapRoot,
		Record: domain.RemoteDataPlaneRecord{
			Mode: domain.DataPlaneHaulerKitV1, AttemptID: request.AttemptID, BootstrapRoot: r.bootstrapRoot,
			KitSHA256: strings.Repeat("a", 64), KitSize: 1, ManifestSHA256: strings.Repeat("b", 64), ManifestSize: 1,
			OuterImage: "example.test/room@sha256:" + strings.Repeat("c", 64),
		},
	}, nil
}

func newRemoteOpenTestEnvironment(t *testing.T) remoteOpenTestEnvironment {
	t.Helper()
	home := t.TempDir()
	paths, err := config.ResolveXDGPaths(config.XDGInput{Home: home, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := config.ResolveFileBackend("file://" + filepath.Join(home, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := journal.NewStore(paths.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	ownership, err := capsule.NewOwnership(filepath.Dir(paths.DataRoot))
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	devpod := &openDevPod{events: &events, folder: "/workspaces/root"}
	runtime := config.Runtime{Bootstrap: config.Bootstrap{Capsule: "brain", RegistryPort: 5000, FileserverPort: 8080}}
	hydrator := &recordingOpenHydrator{ownership: ownership, events: &events}
	leases := &recordingOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	return remoteOpenTestEnvironment{
		paths: paths, backend: backend, runtime: runtime, ownership: ownership, devpod: devpod, hydrator: hydrator, leases: leases,
		open: NewOpen(OpenDependencies{
			Journal: log, Paths: paths, Backend: backend, Ownership: ownership,
			Initializer: &openInitializer{events: &events},
			Pointers:    &recordingOpenPointers{source: remoteOpenPointer()},
			Generations: &recordingOpenGenerations{metadata: remoteOpenMetadata()},
			Leases:      leases, Hydrator: hydrator, Services: &openServices{events: &events}, DevPod: devpod, Target: &openTargetResolver{events: &events},
			Clock: fixedAppClock{now: time.Unix(100, 0).UTC()},
		}),
	}
}

func remoteOpenGeneration() domain.GenerationRef {
	return domain.GenerationRef{Generation: 42, ArchiveSHA256: strings.Repeat("a", 64)}
}

func remoteOpenPointer() coordination.PointerRecord {
	generation := remoteOpenGeneration()
	return coordination.PointerRecord{Pointer: domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: "brain/generations/42-" + generation.ArchiveSHA256 + ".tar.zst", Size: 123,
		CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
	}, Revision: "main-r1"}
}

func remoteOpenMetadata() domain.GenerationMetadata {
	generation := remoteOpenGeneration()
	return domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: "brain/generations/42-" + generation.ArchiveSHA256 + ".tar.zst", MetadataKey: "brain/generations/42-" + generation.ArchiveSHA256 + ".json",
		Size: 123, CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
		Verified: domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true},
	}
}

type recordingOpenPointers struct {
	source coordination.PointerRecord
	calls  []string
}

func (r *recordingOpenPointers) Read(_ context.Context, _ string, lineage domain.Lineage) (coordination.PointerRecord, error) {
	r.calls = append(r.calls, lineage.Branch)
	if lineage.IsMain() {
		return r.source, nil
	}
	return coordination.PointerRecord{}, ports.ErrNotFound
}

func (r *recordingOpenPointers) Revalidate(ctx context.Context, observed coordination.PointerRecord) error {
	current, err := r.Read(ctx, observed.Pointer.Capsule, observed.Pointer.Lineage)
	if err != nil {
		return err
	}
	if current.Revision != observed.Revision || !bytes.Equal(safeJSON(current.Pointer), safeJSON(observed.Pointer)) {
		return coordination.ErrPointerChanged
	}
	return nil
}

type recordingOpenGenerations struct {
	metadata domain.GenerationMetadata
}

func (r *recordingOpenGenerations) ReadMetadata(context.Context, string, domain.Lineage, domain.GenerationRef) (domain.GenerationMetadata, ports.ObjectMeta, error) {
	return r.metadata, ports.ObjectMeta{Key: r.metadata.MetadataKey, Size: 1, SHA256: "metadata"}, nil
}

type recordingOpenLeases struct {
	generation   domain.GenerationRef
	now          time.Time
	owner        coordination.LeaseOwner
	mutate       func(*coordination.LeaseToken)
	branchCalls  int
	acquireCalls int
}

type unknownOutcomeOpenLeases struct {
	generation  domain.GenerationRef
	now         time.Time
	token       coordination.LeaseToken
	available   bool
	mutate      func(*coordination.LeaseToken)
	branchCalls int
	readCalls   int
}

type absentOutcomeOpenLeases struct {
	branchCalls int
	readCalls   int
}

type conflictingOutcomeOpenLeases struct {
	generation  domain.GenerationRef
	now         time.Time
	token       coordination.LeaseToken
	branchCalls int
	readCalls   int
}

func (r *conflictingOutcomeOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	r.readCalls++
	return r.token, nil
}

func (r *conflictingOutcomeOpenLeases) Acquire(context.Context, string, domain.Lineage, coordination.LeaseOwner, *coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, errors.New("unexpected main-lineage lease acquisition")
}

func (r *conflictingOutcomeOpenLeases) AcquireBranchFrom(_ context.Context, capsule string, lineage domain.Lineage, _ coordination.LeaseOwner, _ coordination.PointerRecord, _ time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	opened := r.generation
	r.token = coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: capsule, Lineage: lineage, SessionID: "other-session", Machine: "machine-b",
		OpenedGeneration: &opened, CreatedAt: r.now, HeartbeatAt: r.now, ExpiresAt: r.now.Add(ttl),
	}, Revision: "other-lease-r1"}
	return coordination.LeaseToken{}, ports.ErrAmbiguous
}

func (r *absentOutcomeOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	r.readCalls++
	return coordination.LeaseToken{}, ports.ErrNotFound
}

func (r *absentOutcomeOpenLeases) Acquire(context.Context, string, domain.Lineage, coordination.LeaseOwner, *coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, errors.New("unexpected main-lineage lease acquisition")
}

func (r *absentOutcomeOpenLeases) AcquireBranchFrom(context.Context, string, domain.Lineage, coordination.LeaseOwner, coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	if r.branchCalls == 1 {
		return coordination.LeaseToken{}, ports.ErrAmbiguous
	}
	return coordination.LeaseToken{}, errors.New("reconciliation repeated lease acquisition")
}

func (r *unknownOutcomeOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	r.readCalls++
	if !r.available {
		return coordination.LeaseToken{}, ports.ErrNotFound
	}
	return r.token, nil
}

func (r *unknownOutcomeOpenLeases) Acquire(context.Context, string, domain.Lineage, coordination.LeaseOwner, *coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, errors.New("unexpected main-lineage lease acquisition")
}

func (r *unknownOutcomeOpenLeases) AcquireBranchFrom(_ context.Context, capsule string, lineage domain.Lineage, owner coordination.LeaseOwner, _ coordination.PointerRecord, _ time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	opened := r.generation
	r.token = coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: capsule, Lineage: lineage, SessionID: owner.SessionID, Machine: owner.Machine,
		OpenedGeneration: &opened, CreatedAt: r.now, HeartbeatAt: r.now, ExpiresAt: r.now.Add(ttl),
	}, Revision: "lease-r1"}
	r.available = true
	if r.mutate != nil {
		r.mutate(&r.token)
	}
	return coordination.LeaseToken{}, ports.ErrAmbiguous
}

func (r *recordingOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, ports.ErrNotFound
}

func (r *recordingOpenLeases) Acquire(_ context.Context, _ string, _ domain.Lineage, owner coordination.LeaseOwner, _ *coordination.PointerRecord, now time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.acquireCalls++
	r.owner = owner
	return r.token(owner, domain.Lineage{Branch: "main"}, now, ttl), nil
}

func (r *recordingOpenLeases) AcquireBranchFrom(_ context.Context, _ string, lineage domain.Lineage, owner coordination.LeaseOwner, _ coordination.PointerRecord, now time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	r.owner = owner
	return r.token(owner, lineage, now, ttl), nil
}

func (r *recordingOpenLeases) token(owner coordination.LeaseOwner, lineage domain.Lineage, now time.Time, ttl time.Duration) coordination.LeaseToken {
	opened := r.generation
	token := coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, SessionID: owner.SessionID, Machine: owner.Machine,
		OpenedGeneration: &opened, CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(ttl),
	}, Revision: "lease-r1"}
	if r.mutate != nil {
		r.mutate(&token)
	}
	return token
}

type recordingOpenHydrator struct {
	ownership *capsule.Ownership
	events    *[]string
	request   hydration.Request
	calls     int
	inventory *domain.ImageInventory
}

type recordingOpenImageRestorer struct {
	events  *[]string
	request imageops.RestoreRequest
	calls   int
}

func (r *recordingOpenImageRestorer) Restore(_ context.Context, request imageops.RestoreRequest) (imageops.RestoreResult, error) {
	r.calls++
	r.request = request
	*r.events = append(*r.events, "images")
	return imageops.RestoreResult{Restored: len(request.Inventory.Images)}, nil
}

type recoveryInspectingOpenHydrator struct {
	journal  ports.Journal
	delegate OpenHydrator
	snapshot domain.JournalSnapshot
}

func (r *recoveryInspectingOpenHydrator) Hydrate(ctx context.Context, request hydration.Request) (hydration.Result, error) {
	snapshot, _, err := r.journal.Load(ctx, request.SessionID)
	if err != nil {
		return hydration.Result{}, err
	}
	r.snapshot = snapshot
	return r.delegate.Hydrate(ctx, request)
}

type invalidOpenHydrator struct {
	root string
}

func (r *invalidOpenHydrator) Hydrate(context.Context, hydration.Request) (hydration.Result, error) {
	return hydration.Result{Materialization: domain.Materialization{
		SchemaVersion: domain.SchemaVersion, CanonicalPath: r.root, Mode: domain.MaterializationCreated,
		OwnershipMarker: strings.Repeat("b", 64), CleanupPermitted: true,
	}}, nil
}

type openHydrationStore struct {
	body     []byte
	metadata ports.ObjectMeta
}

func (s *openHydrationStore) Get(context.Context, string) (io.ReadCloser, ports.ObjectMeta, error) {
	return io.NopCloser(bytes.NewReader(s.body)), s.metadata, nil
}

type openHydrationHauler struct {
	inner string
}

func (h *openHydrationHauler) Load(context.Context, string, []string) (ports.Result, error) {
	return ports.Result{}, nil
}

func (h *openHydrationHauler) Extract(_ context.Context, _ string, _ string, output string) (ports.Result, error) {
	if err := os.MkdirAll(output, 0o700); err != nil {
		return ports.Result{}, err
	}
	body, err := os.ReadFile(h.inner)
	if err != nil {
		return ports.Result{}, err
	}
	if err := os.WriteFile(filepath.Join(output, "brain.tar.zst"), body, 0o600); err != nil {
		return ports.Result{}, err
	}
	return ports.Result{}, nil
}

func (r *recordingOpenHydrator) Hydrate(_ context.Context, request hydration.Request) (hydration.Result, error) {
	r.calls++
	r.request = request
	*r.events = append(*r.events, "hydrate")
	if err := os.MkdirAll(request.FinalRoot, 0o700); err != nil {
		return hydration.Result{}, err
	}
	if r.inventory != nil {
		body, err := json.Marshal(r.inventory)
		if err != nil {
			return hydration.Result{}, err
		}
		if err := os.MkdirAll(filepath.Join(request.FinalRoot, ".camp"), 0o700); err != nil {
			return hydration.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(request.FinalRoot, ".camp", "images.json"), body, 0o600); err != nil {
			return hydration.Result{}, err
		}
	}
	materialization, err := r.ownership.MarkCreatedWithToken(request.FinalRoot, request.Token)
	if err != nil {
		return hydration.Result{}, err
	}
	return hydration.Result{Materialization: materialization, StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, Token: request.Token}, nil
}

var _ OpenPointerReader = (*recordingOpenPointers)(nil)
var _ OpenGenerationReader = (*recordingOpenGenerations)(nil)
var _ OpenLeaseManager = (*recordingOpenLeases)(nil)
var _ OpenHydrator = (*recordingOpenHydrator)(nil)
var _ OpenDevPod = (*openDevPod)(nil)
var _ OpenTargetResolver = (*openTargetResolver)(nil)
