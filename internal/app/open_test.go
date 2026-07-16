package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
)

func TestOpenAdoptsRootPreservesOwnershipAndResolvesTargetAfterCommit(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(filepath.Join(root, "MemoryD"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("adopted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "MemoryD", "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "adopted-session", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if result.Snapshot.State != domain.SessionOpen || result.Snapshot.Materialization.Mode != domain.MaterializationAdopted || result.Snapshot.Materialization.CleanupPermitted {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Target.Relative != "MemoryD" || result.Target.Absolute != filepath.Join(root, "MemoryD") {
		t.Fatalf("target = %#v", result.Target)
	}
	if !reflect.DeepEqual(*environment.events, []string{"initialized", "target", "up", "folder", "ssh"}) {
		t.Fatalf("events = %#v", *environment.events)
	}
	if len(environment.devpod.ups) != 1 || len(environment.devpod.ssh) != 1 {
		t.Fatalf("DevPod calls = up:%d ssh:%d", len(environment.devpod.ups), len(environment.devpod.ssh))
	}
	up := environment.devpod.ups[0]
	if up.CampEnvironment == nil || up.CampEnvironment.Capsule != "brain" || up.CampEnvironment.Checkpoint != "" {
		t.Fatalf("Camp environment = %#v", up.CampEnvironment)
	}
	if up.Context != "default" || up.Provider != "docker" || up.DevcontainerPath == "" {
		t.Fatalf("Up options = %#v", up)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("adopted root disappeared: %v", err)
	}
	removed, err := environment.ownership.RemoveOwned(context.Background(), result.Snapshot.Materialization)
	if err != nil || removed {
		t.Fatalf("adopted RemoveOwned() = %v, %v", removed, err)
	}
}

func TestOpenReentrySelectsExistingSessionWithoutSecondWorkspaceCreation(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(filepath.Join(root, "MemoryD"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "reentry-session", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	upCount, eventCount := len(environment.devpod.ups), len(*environment.events)
	second, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "main", Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("re-entry Open() error = %v", err)
	}
	if second.Snapshot.SessionID != first.Snapshot.SessionID || len(environment.devpod.ups) != upCount || len(*environment.events) != eventCount+2 {
		t.Fatalf("re-entry snapshot/calls = %#v, ups=%d events=%#v", second.Snapshot, len(environment.devpod.ups), *environment.events)
	}
}

func TestOpenReentryCanonicalizesExplicitRootForSessionSelection(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(filepath.Join(root, "MemoryD"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "canonical-root-reentry-session", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	upCount, eventCount := len(environment.devpod.ups), len(*environment.events)
	second, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "main", ExplicitRoot: root + string(filepath.Separator) + ".", Target: "MemoryD",
		EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("re-entry Open() error = %v", err)
	}
	if second.Snapshot.SessionID != first.Snapshot.SessionID || len(environment.devpod.ups) != upCount || len(*environment.events) != eventCount+2 {
		t.Fatalf("canonical-root re-entry snapshot/calls = %#v, ups=%d events=%#v", second.Snapshot, len(environment.devpod.ups), *environment.events)
	}
}

func TestOpenReentryRejectsIncoherentAdoptedSourceBeforeSideEffects(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*domain.JournalSnapshot)
	}{
		{name: "source-kind", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.Source.Kind = domain.SourceDecisionRemote }},
		{name: "source-root", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.Source.Root = t.TempDir() }},
		{name: "cleanup-policy", mutate: func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.Cleanup.RemoveOwnedMaterialization = true }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := newOpenTestEnvironment(t)
			root := filepath.Join(t.TempDir(), "SecondBrain")
			if err := os.MkdirAll(filepath.Join(root, "MemoryD"), 0o700); err != nil {
				t.Fatal(err)
			}
			first, err := environment.open.Run(context.Background(), OpenRequest{
				SessionID: "adopted-source-reentry-" + test.name, Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
				ExplicitRoot: root, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
				Runtime: environment.runtime, Backend: environment.backend,
			})
			if err != nil {
				t.Fatalf("first Open() error = %v", err)
			}
			snapshot := first.Snapshot
			test.mutate(&snapshot)
			eventCount, sshCount := len(*environment.events), len(environment.devpod.ssh)
			_, err = environment.open.reenter(context.Background(), snapshot, OpenRequest{
				SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch,
				Context: snapshot.Workspace.Context, Provider: snapshot.Workspace.Provider, Target: "MemoryD", EntryMode: domain.EntryTerminal,
			})
			if !errors.Is(err, ErrOpenSessionMismatch) {
				t.Fatalf("reenter() error = %v, want ErrOpenSessionMismatch", err)
			}
			if len(*environment.events) != eventCount || len(environment.devpod.ssh) != sshCount {
				t.Fatalf("re-entry effects after adopted source mismatch: events=%v ssh=%d", *environment.events, len(environment.devpod.ssh))
			}
		})
	}
}

func TestOpenRejectsUnsafeXDGLayoutAndNonCanonicalBackend(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{
		SessionID: "unsafe-layout", Capsule: "brain", Branch: "main", Mode: domain.SessionReadOnly,
		ExplicitRoot: root, EntryMode: domain.EntryTerminal, Runtime: environment.runtime,
		Backend: environment.backend,
	}
	deps := environment.open.deps
	deps.Paths.SessionRoot = deps.Paths.DataRoot
	if _, err := NewOpen(deps).Run(context.Background(), request); err == nil {
		t.Fatal("overlapping XDG paths were accepted")
	}

	request.SessionID = "unsafe-backend"
	request.Backend = config.FileBackend{Root: environment.backend.Root}
	if _, err := environment.open.Run(context.Background(), request); err == nil {
		t.Fatal("backend without canonical file URL was accepted")
	}
}

func TestOpenRejectsPathLikeCapsuleAndBranchBeforeMaterialization(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, request := range []OpenRequest{
		{SessionID: "unsafe-capsule", Capsule: "../brain", Branch: "main", ExplicitRoot: root, Runtime: environment.runtime, Backend: environment.backend},
		{SessionID: "unsafe-branch", Capsule: "brain", Branch: "../escape", RemoteAvailable: true, Runtime: environment.runtime, Backend: environment.backend},
	} {
		if _, err := environment.open.Run(context.Background(), request); err == nil {
			t.Fatalf("path-like request was accepted: %#v", request)
		}
	}
}

func TestOpenPersistsWorkspaceIdentityBeforeFolderResolutionFailure(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	environment.devpod.folderErr = errors.New("folder lookup failed")
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "folder-failure", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, Target: "", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err == nil {
		t.Fatal("Open() unexpectedly succeeded")
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), "folder-failure")
	if loadErr != nil || len(pending) == 0 || loaded.Workspace.ID == "" || loaded.Workspace.LocalFolder != root {
		t.Fatalf("durable workspace recovery state = %#v pending=%#v error=%v", loaded.Workspace, pending, loadErr)
	}
}

type openTestEnvironment struct {
	open      *Open
	ownership *capsule.Ownership
	runtime   config.Runtime
	backend   config.FileBackend
	events    *[]string
	devpod    *openDevPod
}

func newOpenTestEnvironment(t *testing.T) openTestEnvironment {
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
	initializer := &openInitializer{events: &events}
	devpod := &openDevPod{events: &events, folder: "/workspaces/root"}
	resolver := &openTargetResolver{events: &events}
	runtime := config.Runtime{Bootstrap: config.Bootstrap{Capsule: "brain", RegistryPort: 5000, FileserverPort: 8080}}
	return openTestEnvironment{
		ownership: ownership, runtime: runtime, backend: backend, events: &events, devpod: devpod,
		open: NewOpen(OpenDependencies{
			Journal: log, Paths: paths, Backend: backend, Ownership: ownership, Initializer: initializer,
			DevPod: devpod, Target: resolver, Clock: fixedAppClock{now: time.Unix(100, 0).UTC()},
		}),
	}
}

type openInitializer struct {
	events *[]string
}

func (i *openInitializer) Initialize(_ context.Context, root, capsuleID string) (capsule.Initialization, error) {
	*i.events = append(*i.events, "initialized")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		return capsule.Initialization{}, err
	}
	if err := os.WriteFile(filepath.Join(root, ".camp", "capsule.yaml"), []byte("id: "+capsuleID+"\nschemaVersion: 1\ndefaultBranch: main\ncreatedAt: 2026-07-14T00:00:00Z\n"), 0o600); err != nil {
		return capsule.Initialization{}, err
	}
	return capsule.Initialization{Metadata: domain.CapsuleMetadata{SchemaVersion: domain.SchemaVersion, ID: capsuleID, DefaultBranch: "main", CreatedAt: time.Unix(1, 0).UTC()}, Lock: domain.CapsuleLock{SchemaVersion: domain.SchemaVersion, Room: domain.RoomLock{Image: "room", Digest: "sha256:" + strings.Repeat("a", 64)}}}, nil
}

type openDevPod struct {
	events    *[]string
	ups       []devpodadapter.UpOptions
	ssh       []devpodadapter.SSHOptions
	folder    string
	folderErr error
}

func (d *openDevPod) Up(_ context.Context, options devpodadapter.UpOptions) (ports.Result, error) {
	*d.events = append(*d.events, "up")
	d.ups = append(d.ups, options)
	return ports.Result{}, nil
}
func (d *openDevPod) ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error) {
	*d.events = append(*d.events, "folder")
	if d.folderErr != nil {
		return "", d.folderErr
	}
	return d.folder, nil
}
func (d *openDevPod) SSH(_ context.Context, options devpodadapter.SSHOptions) (ports.Result, error) {
	*d.events = append(*d.events, "ssh")
	d.ssh = append(d.ssh, options)
	return ports.Result{}, nil
}

type openTargetResolver struct {
	events *[]string
}

func (r *openTargetResolver) Resolve(_ context.Context, root, requested string) (target.Result, error) {
	*r.events = append(*r.events, "target")
	if _, err := os.Stat(filepath.Join(root, ".camp")); err != nil {
		return target.Result{}, errors.New("target resolved before capsule commit")
	}
	return target.Result{Absolute: filepath.Join(root, requested), Relative: requested}, nil
}
