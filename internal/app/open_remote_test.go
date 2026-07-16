package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
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
			Leases:      leases, Hydrator: hydrator, DevPod: devpod, Target: &openTargetResolver{events: &events},
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
}

func (r *recordingOpenPointers) Read(_ context.Context, _ string, lineage domain.Lineage) (coordination.PointerRecord, error) {
	if lineage.IsMain() {
		return r.source, nil
	}
	return coordination.PointerRecord{}, ports.ErrNotFound
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
	branchCalls  int
	acquireCalls int
}

func (r *recordingOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, ports.ErrNotFound
}

func (r *recordingOpenLeases) Acquire(_ context.Context, _ string, _ domain.Lineage, owner coordination.LeaseOwner, _ *coordination.PointerRecord, _ time.Time, _ time.Duration) (coordination.LeaseToken, error) {
	r.acquireCalls++
	r.owner = owner
	return r.token(owner, domain.Lineage{Branch: "main"}), nil
}

func (r *recordingOpenLeases) AcquireBranchFrom(_ context.Context, _ string, lineage domain.Lineage, owner coordination.LeaseOwner, _ coordination.PointerRecord, _ time.Time, _ time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	r.owner = owner
	return r.token(owner, lineage), nil
}

func (r *recordingOpenLeases) token(owner coordination.LeaseOwner, lineage domain.Lineage) coordination.LeaseToken {
	opened := r.generation
	return coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, SessionID: owner.SessionID, Machine: owner.Machine,
		OpenedGeneration: &opened, CreatedAt: r.now, HeartbeatAt: r.now, ExpiresAt: r.now.Add(time.Hour),
	}, Revision: "lease-r1"}
}

type recordingOpenHydrator struct {
	ownership *capsule.Ownership
	events    *[]string
	request   hydration.Request
	calls     int
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
