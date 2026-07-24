package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	"github.com/joshyorko/camp/internal/adapters/objectstore"
	"github.com/joshyorko/camp/internal/adapters/s3store"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
)

func TestOpenComposesResolvedS3BackendAndPersistsSanitizedIdentity(t *testing.T) {
	environment := newOpenTestEnvironment(t)
	server := newSafeS3WriterServer(t)
	defer server.Close()
	backend, err := config.ResolveBackend("s3://camp-bucket/team", config.S3Values{
		Endpoint: server.URL, Region: "us-east-1", PathStyle: true, Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := environment.open.deps
	opener, err := NewOpenWithBackend(context.Background(), deps, backend, objectstore.Options{
		HTTPClient: server.Client(), Signer: s3store.SignFunc(func(*http.Request) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := opener.Run(context.Background(), OpenRequest{
		SessionID: "s3-session", Capsule: "brain", Branch: "main", ExplicitRoot: root,
		Runtime: environment.runtime, ResolvedBackend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	configuration := result.Snapshot.Recovery.Configuration
	if configuration.BackendKind != "s3" || configuration.BackendURL != "s3://camp-bucket/team" || configuration.BackendFingerprint != backend.Fingerprint {
		t.Fatalf("recovery backend identity = %#v", configuration)
	}
	if _, err := environment.open.Reconcile(context.Background(), "s3-session"); !errors.Is(err, ErrOpenSessionMismatch) {
		t.Fatalf("file-composed recovery error = %v, want backend mismatch", err)
	}
}

func newSafeS3WriterServer(t *testing.T) *httptest.Server {
	t.Helper()
	bodies := map[string][]byte{}
	revisions := map[string]int{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := bodies[r.URL.Path]
		revision := revisions[r.URL.Path]
		etag := fmt.Sprintf(`"revision-%d"`, revision)
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") == "*" && body != nil || r.Header.Get("If-Match") != "" && r.Header.Get("If-Match") != etag {
				http.Error(w, "condition failed", http.StatusPreconditionFailed)
				return
			}
			body, _ = io.ReadAll(r.Body)
			revision++
			bodies[r.URL.Path] = body
			revisions[r.URL.Path] = revision
			w.Header().Set("ETag", fmt.Sprintf(`"revision-%d"`, revision))
		case http.MethodGet, http.MethodHead:
			if body == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			if r.Method == http.MethodGet {
				_, _ = w.Write(body)
			}
		case http.MethodDelete:
			if r.Header.Get("If-Match") != etag {
				http.Error(w, "condition failed", http.StatusPreconditionFailed)
				return
			}
			delete(bodies, r.URL.Path)
			delete(revisions, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
}

func TestOpenResolvedFileBackendRemainsSupported(t *testing.T) {
	environment := newOpenTestEnvironment(t)
	backend, err := config.ResolveBackend(environment.backend.SanitizedURL, config.S3Values{})
	if err != nil {
		t.Fatal(err)
	}
	deps := environment.open.deps
	opener, err := NewOpenWithBackend(context.Background(), deps, backend, objectstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := opener.Run(context.Background(), OpenRequest{
		SessionID: "file-session", Capsule: "brain", Branch: "main", ExplicitRoot: root,
		Runtime: environment.runtime, ResolvedBackend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Recovery.Configuration.BackendKind != "file" {
		t.Fatalf("recovery backend = %#v", result.Snapshot.Recovery.Configuration)
	}
}

func TestOpenRejectsRequestBackendThatDiffersFromComposedStore(t *testing.T) {
	environment := newOpenTestEnvironment(t)
	composed, err := config.ResolveBackend(environment.backend.SanitizedURL, config.S3Values{})
	if err != nil {
		t.Fatal(err)
	}
	opener, err := NewOpenWithBackend(context.Background(), environment.open.deps, composed, objectstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer server.Close()
	requested, err := config.ResolveBackend("s3://other-store/team", config.S3Values{
		Endpoint: server.URL, Region: "us-east-1", PathStyle: true, Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = opener.Run(context.Background(), OpenRequest{
		SessionID: "backend-mismatch", Capsule: "brain", Branch: "main", ExplicitRoot: root,
		Runtime: environment.runtime, ResolvedBackend: requested,
	})
	if !errors.Is(err, ErrOpenSessionMismatch) {
		t.Fatalf("Open() error = %v, want backend mismatch", err)
	}
	if _, _, loadErr := environment.open.deps.Journal.Load(context.Background(), "backend-mismatch"); !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("journal load error = %v, want no session side effect", loadErr)
	}
}

func TestNewOpenWithBackendRebindsHydrationToComposedStore(t *testing.T) {
	environment := newOpenTestEnvironment(t)
	backend, err := config.ResolveBackend(environment.backend.SanitizedURL, config.S3Values{})
	if err != nil {
		t.Fatal(err)
	}
	deps := environment.open.deps
	deps.Hydrator = hydration.NewController(nil, nil, nil, nil, hydration.Hooks{})
	opener, err := NewOpenWithBackend(context.Background(), deps, backend, objectstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if opener.deps.Hydrator == deps.Hydrator {
		t.Fatal("hydrator retained its previously injected generation store")
	}
}

func TestOpenRejectsResolvedBackendWithoutConstructorBinding(t *testing.T) {
	environment := newOpenTestEnvironment(t)
	backend, err := config.ResolveBackend(environment.backend.SanitizedURL, config.S3Values{})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = environment.open.Run(context.Background(), OpenRequest{
		SessionID: "unbound-backend", Capsule: "brain", Branch: "main", ExplicitRoot: root,
		Runtime: environment.runtime, ResolvedBackend: backend,
	})
	if !errors.Is(err, ErrOpenSessionMismatch) {
		t.Fatalf("Open() error = %v, want unbound backend mismatch", err)
	}
	if _, _, loadErr := environment.open.deps.Journal.Load(context.Background(), "unbound-backend"); !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("journal load error = %v, want no session side effect", loadErr)
	}
}

func TestNewOpenWithBackendRejectsUnsafeConditionalWriterBeforeBinding(t *testing.T) {
	environment := newOpenTestEnvironment(t)
	var body []byte
	revision := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, _ = io.ReadAll(r.Body)
			revision++
			w.Header().Set("ETag", fmt.Sprintf(`"revision-%d"`, revision))
		case http.MethodGet:
			w.Header().Set("ETag", fmt.Sprintf(`"revision-%d"`, revision))
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		case http.MethodDelete:
			body = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	backend, err := config.ResolveBackend("s3://camp-bucket/team", config.S3Values{
		Endpoint: server.URL, Region: "us-east-1", PathStyle: true, Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewOpenWithBackend(context.Background(), environment.open.deps, backend, objectstore.Options{
		HTTPClient: server.Client(), Signer: s3store.SignFunc(func(*http.Request) error { return nil }),
	})
	if !errors.Is(err, s3store.ErrUnsafeWriter) {
		t.Fatalf("NewOpenWithBackend() error = %v, want unsafe writer", err)
	}
}

func TestOpenRejectsRequestFileBackendThatDiffersFromConstructor(t *testing.T) {
	environment := newOpenTestEnvironment(t)
	different, err := config.ResolveFileBackend("file://" + filepath.Join(t.TempDir(), "different-backend"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = environment.open.Run(context.Background(), OpenRequest{
		SessionID: "file-backend-mismatch", Capsule: "brain", Branch: "main", ExplicitRoot: root,
		Runtime: environment.runtime, Backend: different,
	})
	if !errors.Is(err, ErrOpenSessionMismatch) {
		t.Fatalf("Open() error = %v, want backend mismatch", err)
	}
	if _, _, loadErr := environment.open.deps.Journal.Load(context.Background(), "file-backend-mismatch"); !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("journal load error = %v, want no session side effect", loadErr)
	}
}

func TestOpenAdoptsRootPreservesOwnershipAndResolvesTargetAfterCommit(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	runtimeRoot := t.TempDir()
	environment.open.deps.Paths.RuntimeRoot = runtimeRoot
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
		ExplicitRoot: root, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if result.Snapshot.State != domain.SessionOpen || result.Snapshot.Materialization.Mode != domain.MaterializationAdopted || result.Snapshot.Materialization.CleanupPermitted {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Snapshot.Recovery.Session.RuntimeRoot != filepath.Join(runtimeRoot, "adopted-session") {
		t.Fatalf("session runtime root = %q", result.Snapshot.Recovery.Session.RuntimeRoot)
	}
	if result.Snapshot.Workspace.Provider != "docker" || !result.Snapshot.Workspace.LocalProvider {
		t.Fatalf("workspace classification = %#v, want default local docker provider", result.Snapshot.Workspace)
	}
	if result.Snapshot.Lease.Lease == nil || result.Snapshot.Lease.Revision == "" || result.Snapshot.Lease.Lease.OpenedGeneration != nil {
		t.Fatalf("local writer lease = %#v", result.Snapshot.Lease)
	}
	if result.Target.Relative != "MemoryD" || result.Target.Absolute != filepath.Join(root, "MemoryD") {
		t.Fatalf("target = %#v", result.Target)
	}
	if !reflect.DeepEqual(*environment.events, []string{"initialized", "target", "services", "up", "folder", "forward:registry", "forward:fileserver", "ssh"}) {
		t.Fatalf("events = %#v", *environment.events)
	}
	if len(result.Snapshot.Recovery.Forwarding) != 2 || result.Snapshot.Recovery.Forwarding[0].Name != "registry" || result.Snapshot.Recovery.Forwarding[1].Name != "fileserver" {
		t.Fatalf("forwarding = %#v", result.Snapshot.Recovery.Forwarding)
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

func TestOpenUsesRuntimeDevcontainerPathAsSingleSourceOfTruth(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	custom := filepath.Join(root, "custom", "devcontainer.json")
	if err := os.MkdirAll(filepath.Dir(custom), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte("{\"image\":\"example.invalid/camp@sha256:"+strings.Repeat("b", 64)+"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := environment.runtime
	runtime.DevcontainerPath = filepath.Join("custom", "devcontainer.json")
	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "runtime-devcontainer", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, Target: "", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(environment.devpod.ups) != 1 || environment.devpod.ups[0].DevcontainerPath != filepath.Join("custom", "devcontainer.json") {
		t.Fatalf("DevPod devcontainer path = %#v, want capsule-relative path", environment.devpod.ups)
	}
	if result.Snapshot.Recovery.Configuration.DevcontainerPath != custom {
		t.Fatalf("recovery devcontainer path = %q, want %q", result.Snapshot.Recovery.Configuration.DevcontainerPath, custom)
	}
}

func TestOpenStartsServicesBeforeDevPodUp(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "brain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.open.Run(context.Background(), OpenRequest{SessionID: "service-order", Capsule: "brain", ExplicitRoot: root, Runtime: environment.runtime, Backend: environment.backend}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*environment.events, ","); !strings.Contains(got, "target,services,up") {
		t.Fatalf("events = %q, want services before DevPod up", got)
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

func TestOpenRejectsIDEEntryBeforeJournalCreateOrEffects(t *testing.T) {
	t.Parallel()
	environment := newOpenTestEnvironment(t)
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "unsupported-ide", Capsule: "brain", Branch: "main", Mode: domain.SessionReadWrite,
		ExplicitRoot: root, Target: "MemoryD", EntryMode: domain.EntryIDE, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ErrOpenIDEUnsupported) {
		t.Fatalf("Open() error = %v, want ErrOpenIDEUnsupported", err)
	}
	if _, _, loadErr := environment.open.deps.Journal.Load(context.Background(), "unsupported-ide"); loadErr == nil {
		t.Fatal("unsupported IDE request created a journal")
	}
	if len(*environment.events) != 0 || len(environment.devpod.ups) != 0 || len(environment.devpod.ssh) != 0 {
		t.Fatalf("unsupported IDE request effects: events=%v ups=%d ssh=%d", *environment.events, len(environment.devpod.ups), len(environment.devpod.ssh))
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
	services  *openServices
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
	services := &openServices{events: &events}
	forwarders := &openForwarders{events: &events}
	leases := &localOpenLeases{}
	runtime := config.Runtime{Bootstrap: config.Bootstrap{Capsule: "brain", RegistryPort: 5000, FileserverPort: 8080}}
	return openTestEnvironment{
		ownership: ownership, runtime: runtime, backend: backend, events: &events, devpod: devpod, services: services,
		open: NewOpen(OpenDependencies{
			Journal: log, Paths: paths, Backend: backend, Ownership: ownership, Initializer: initializer,
			Services: services, Forwarders: forwarders, DevPod: devpod, Leases: leases, Target: resolver, Clock: fixedAppClock{now: time.Unix(100, 0).UTC()},
		}),
	}
}

type localOpenLeases struct{}

func (*localOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, ports.ErrNotFound
}
func (*localOpenLeases) Acquire(_ context.Context, capsule string, lineage domain.Lineage, owner coordination.LeaseOwner, observed *coordination.PointerRecord, now time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	if observed != nil {
		return coordination.LeaseToken{}, errors.New("local lease unexpectedly observed a pointer")
	}
	return coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: capsule, Lineage: lineage, SessionID: owner.SessionID, Machine: owner.Machine,
		CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(ttl),
	}, Revision: "local-lease-r1"}, nil
}
func (*localOpenLeases) AcquireBranchFrom(context.Context, string, domain.Lineage, coordination.LeaseOwner, coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, errors.New("local lease unexpectedly acquired from branch")
}

type openForwarders struct {
	events     *[]string
	records    map[string]domain.ForwardingRecord
	observeErr error
}

func (f *openForwarders) Start(_ context.Context, request domain.ForwardingRequest) (domain.ForwardingRecord, error) {
	*f.events = append(*f.events, "forward:"+request.Name)
	record := domain.ForwardingRecord{
		Name: request.Name, LocalEndpoint: request.LocalEndpoint, WorkspaceEndpoint: request.WorkspaceEndpoint,
		EvidencePath: request.EvidencePath, EvidenceDevice: 1, EvidenceInode: uint64(len(request.Name) + 1),
		Process:      domain.ProcessRecord{Identity: domain.ProcessIdentity{PID: len(request.Name) + 100, BootID: "boot", StartTicks: 10}},
		DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
	}
	if f.records == nil {
		f.records = make(map[string]domain.ForwardingRecord)
	}
	f.records[request.Name] = record
	return record, nil
}

func (f *openForwarders) Observe(_ context.Context, request domain.ForwardingRequest) (domain.ForwardingRecord, error) {
	*f.events = append(*f.events, "observe-forward:"+request.Name)
	if f.observeErr != nil {
		return domain.ForwardingRecord{}, f.observeErr
	}
	record, ok := f.records[request.Name]
	if !ok || record.EvidencePath != request.EvidencePath {
		return domain.ForwardingRecord{}, errors.New("exact durable forwarder evidence is unavailable")
	}
	return record, nil
}

func (f *openForwarders) Stop(context.Context, domain.ForwardingRecord) error { return nil }

type openServices struct {
	events   *[]string
	registry bool
}

func (s *openServices) Start(_ context.Context, snapshot domain.JournalSnapshot) (domain.JournalSnapshot, error) {
	*s.events = append(*s.events, "services")
	if s.registry {
		snapshot.Services = append(snapshot.Services, domain.ServiceUnitRecord{
			Name: "registry", DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
			Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: 5000},
			Child:   domain.ProcessRecord{Argv: []string{"hauler", "store", "serve", "registry", "--directory", "/tmp/camp-registry"}},
		})
	}
	return snapshot, nil
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
func (d *openDevPod) ListInContext(_ context.Context, devpodContext string) ([]devpodadapter.Workspace, error) {
	workspaces := make([]devpodadapter.Workspace, 0, len(d.ups))
	for _, up := range d.ups {
		workspaces = append(workspaces, devpodadapter.Workspace{
			ID: up.WorkspaceID, Provider: devpodadapter.WorkspaceProvider{Name: up.Provider},
			Source: devpodadapter.WorkspaceSource{LocalFolder: up.WorkspacePath}, Context: devpodContext,
		})
	}
	return workspaces, nil
}
func (d *openDevPod) StatusInContext(_ context.Context, devpodContext, workspaceID string) (devpodadapter.WorkspaceStatus, error) {
	for _, up := range d.ups {
		if up.WorkspaceID == workspaceID {
			return devpodadapter.WorkspaceStatus{ID: workspaceID, Context: devpodContext, Provider: up.Provider, State: devpodadapter.StateRunning}, nil
		}
	}
	return devpodadapter.WorkspaceStatus{ID: workspaceID, Context: devpodContext, State: devpodadapter.StateNotFound}, nil
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
func (d *openDevPod) SSHWithStart(ctx context.Context, options devpodadapter.SSHOptions, started func() error) (ports.Result, error) {
	if err := started(); err != nil {
		return ports.Result{}, err
	}
	return d.SSH(ctx, options)
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
