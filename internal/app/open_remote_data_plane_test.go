package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/remoteworker"
)

func TestRemoteDataPlanePreparerBuildsVerifiesThenRendersBootstrap(t *testing.T) {
	root := t.TempDir()
	devcontainer := filepath.Join(root, "devcontainer.json")
	if err := os.WriteFile(devcontainer, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var order []string
	hauler := &fakeRemoteHauler{order: &order}
	builder := &fakeRemoteKitBuilder{order: &order}
	verifier := &fakeRemoteKitVerifier{order: &order}
	preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
		Root: t.TempDir(), Archiver: fakeRemoteArchiver{order: &order}, Hauler: hauler,
		Builder: builder, Verifier: verifier, Images: fakeRemoteImageResolver{},
		Confinement: fakeRemoteConfinement{}, HaulerExecutable: "/managed/hauler", HaulerVersion: "v2.0.2",
	})
	preparer.render = func(request capsule.BootstrapRequest) (capsule.Bootstrap, error) {
		order = append(order, "render")
		if request.OuterImage != "sha256:"+strings.Repeat("c", 64) {
			t.Fatalf("outer image = %q", request.OuterImage)
		}
		if request.InitializeRequest.Expected.SourceImage != "example.test/workspace:v1@sha256:"+strings.Repeat("d", 64) {
			t.Fatalf("source image = %q", request.InitializeRequest.Expected.SourceImage)
		}
		if request.InitializeRequest.Expected.Kit.SHA256 != digestString([]byte("kit")) {
			t.Fatalf("expected kit = %#v", request.InitializeRequest.Expected.Kit)
		}
		return writeFakeRenderedBootstrap(request)
	}
	result, err := preparer.Prepare(context.Background(), RemoteDataPlaneRequest{
		SessionID: "session-1", AttemptID: "session-1-hauler-kit-v1", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Materialization: root, DevcontainerPath: devcontainer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "archive,add-file,add-image,build,verify,render" {
		t.Fatalf("preparation order = %v", order)
	}
	if result.Record.Mode != domain.DataPlaneHaulerKitV1 || result.Record.AttemptID != "session-1-hauler-kit-v1" ||
		result.Record.BootstrapRoot != result.BootstrapRoot || result.Record.OuterImage == "" {
		t.Fatalf("result = %#v", result)
	}
	if builder.request.CampVersion != "" || builder.request.PastaExecutable != "/usr/bin/pasta" ||
		builder.request.PastaVersion != "pasta 2026" || builder.request.HaulerVersion != "v2.0.2" {
		t.Fatalf("build request = %#v", builder.request)
	}
}

func TestRemoteDataPlanePreparerStopsBeforeRenderAfterBuildOrVerifyFailure(t *testing.T) {
	for _, stage := range []string{"build", "verify"} {
		t.Run(stage, func(t *testing.T) {
			var order []string
			builder := &fakeRemoteKitBuilder{order: &order}
			verifier := &fakeRemoteKitVerifier{order: &order}
			if stage == "build" {
				builder.err = errors.New("build failed")
			} else {
				verifier.err = errors.New("verify failed")
			}
			preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
				Root: t.TempDir(), Archiver: fakeRemoteArchiver{order: &order}, Hauler: &fakeRemoteHauler{order: &order},
				Builder: builder, Verifier: verifier, Images: fakeRemoteImageResolver{}, Confinement: fakeRemoteConfinement{},
				HaulerExecutable: "/managed/hauler", HaulerVersion: "v2.0.2",
			})
			preparer.render = func(capsule.BootstrapRequest) (capsule.Bootstrap, error) {
				t.Fatal("render called after failure")
				return capsule.Bootstrap{}, nil
			}
			root := t.TempDir()
			config := filepath.Join(root, "devcontainer.json")
			if err := os.WriteFile(config, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := preparer.Prepare(context.Background(), RemoteDataPlaneRequest{
				SessionID: "session-1", AttemptID: "session-1-hauler-kit-v1", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
				Materialization: root, DevcontainerPath: config,
			})
			if err == nil {
				t.Fatal("Prepare() error = nil")
			}
		})
	}
}

func TestRemoteDataPlanePreparerReusesVerifiedCompletedAttempt(t *testing.T) {
	var order []string
	preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
		Root: t.TempDir(), Archiver: fakeRemoteArchiver{order: &order}, Hauler: &fakeRemoteHauler{order: &order},
		Builder: &fakeRemoteKitBuilder{order: &order}, Verifier: &fakeRemoteKitVerifier{order: &order},
		Images: fakeRemoteImageResolver{}, Confinement: fakeRemoteConfinement{},
		HaulerExecutable: "/managed/hauler", HaulerVersion: "v2.0.2",
	})
	preparer.render = func(request capsule.BootstrapRequest) (capsule.Bootstrap, error) {
		return writeFakeRenderedBootstrap(request)
	}
	root := t.TempDir()
	config := filepath.Join(root, "devcontainer.json")
	if err := os.WriteFile(config, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := RemoteDataPlaneRequest{
		SessionID: "session-1", AttemptID: "session-1-hauler-kit-v1", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Materialization: root, DevcontainerPath: config,
	}
	first, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	order = nil
	second, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Record != second.Record || strings.Join(order, ",") != "verify" {
		t.Fatalf("reused attempt = first:%#v second:%#v order:%v", first.Record, second.Record, order)
	}
}

func TestRemoteDataPlanePreparerRebuildsOwnedPartialUnderSameAttemptIdentity(t *testing.T) {
	var order []string
	root := t.TempDir()
	const attemptID = "session-1-hauler-kit-v1"
	if err := createOwnedAttempt(root, attemptID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, attemptID, "partial"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
		Root: root, Archiver: fakeRemoteArchiver{order: &order}, Hauler: &fakeRemoteHauler{order: &order},
		Builder: &fakeRemoteKitBuilder{order: &order}, Verifier: &fakeRemoteKitVerifier{order: &order},
		Images: fakeRemoteImageResolver{}, Confinement: fakeRemoteConfinement{},
		HaulerExecutable: "/managed/hauler", HaulerVersion: "v2.0.2",
	})
	preparer.render = writeFakeRenderedBootstrap
	materialization := t.TempDir()
	config := filepath.Join(materialization, "devcontainer.json")
	if err := os.WriteFile(config, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := preparer.Prepare(context.Background(), RemoteDataPlaneRequest{
		SessionID: "session-1", AttemptID: attemptID, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Materialization: materialization, DevcontainerPath: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.AttemptID != attemptID || filepath.Dir(filepath.Dir(result.BootstrapRoot)) != root {
		t.Fatalf("rebuilt attempt = %#v", result)
	}
	if strings.Join(order, ",") != "archive,add-file,add-image,build,verify" {
		t.Fatalf("partial recovery order = %v", order)
	}
}

func TestRemoteDataPlanePreparerPreservesUnownedPartialAttempt(t *testing.T) {
	root := t.TempDir()
	const attemptID = "session-1-hauler-kit-v1"
	attempt := filepath.Join(root, attemptID)
	if err := os.Mkdir(attempt, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(attempt, "operator-data")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	var order []string
	preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
		Root: root, Archiver: fakeRemoteArchiver{order: &order}, Hauler: &fakeRemoteHauler{order: &order},
		Builder: &fakeRemoteKitBuilder{order: &order}, Verifier: &fakeRemoteKitVerifier{order: &order},
		Images: fakeRemoteImageResolver{}, Confinement: fakeRemoteConfinement{},
		HaulerExecutable: "/managed/hauler", HaulerVersion: "v2.0.2",
	})
	materialization := t.TempDir()
	config := filepath.Join(materialization, "devcontainer.json")
	if err := os.WriteFile(config, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := preparer.Prepare(context.Background(), RemoteDataPlaneRequest{
		SessionID: "session-1", AttemptID: attemptID, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Materialization: materialization, DevcontainerPath: config,
	}); err == nil {
		t.Fatal("Prepare() removed an unowned partial attempt")
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "preserve" {
		t.Fatalf("unowned sentinel = %q, %v", body, err)
	}
}

func TestRemoteDataPlanePreparerRejectsTamperedCompletedBootstrapWithoutRebuild(t *testing.T) {
	var order []string
	preparer, request := fakeCompletedPreparer(t, &order)
	first, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.BootstrapRoot, "camp-hauler-kit.tar.zst"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	order = nil
	if _, err := preparer.Prepare(context.Background(), request); err == nil {
		t.Fatal("Prepare() accepted tampered completed bootstrap")
	}
	if strings.Contains(strings.Join(order, ","), "archive") {
		t.Fatalf("tampered completed attempt was rebuilt: %v", order)
	}
}

func TestRemoteDataPlanePreparerExitFailureDoesNotFormatNilAsWrappedError(t *testing.T) {
	var order []string
	hauler := &fakeRemoteHauler{order: &order, addFileResult: ports.Result{ExitCode: 23}}
	preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
		Root: t.TempDir(), Archiver: fakeRemoteArchiver{order: &order}, Hauler: hauler,
		Builder: &fakeRemoteKitBuilder{order: &order}, Verifier: &fakeRemoteKitVerifier{order: &order},
		Images: fakeRemoteImageResolver{}, Confinement: fakeRemoteConfinement{},
		HaulerExecutable: "/managed/hauler", HaulerVersion: "v2.0.2",
	})
	materialization := t.TempDir()
	config := filepath.Join(materialization, "devcontainer.json")
	if err := os.WriteFile(config, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := preparer.Prepare(context.Background(), RemoteDataPlaneRequest{
		SessionID: "session-1", AttemptID: "session-1-hauler-kit-v1", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Materialization: materialization, DevcontainerPath: config,
	})
	if err == nil || strings.Contains(err.Error(), "%!w") || !strings.Contains(err.Error(), "exited 23") {
		t.Fatalf("Prepare() error = %q", err)
	}
}

func TestProductionRemoteDataPlaneSeamBuildsVerifiesRendersAndGeneratesBootstrapArgv(t *testing.T) {
	root := t.TempDir()
	materialization := filepath.Join(root, "materialization")
	if err := os.Mkdir(materialization, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(materialization, "README.md"), []byte("production seam\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	devcontainer := filepath.Join(materialization, "devcontainer.json")
	if err := os.WriteFile(devcontainer, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	haulerExecutable := filepath.Join(root, "hauler")
	pastaExecutable := filepath.Join(root, "pasta")
	for _, path := range []string{haulerExecutable, pastaExecutable} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store := &productionSeamHauler{}
	observer := productionSeamRuntimeObserver{}
	preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
		Root: filepath.Join(root, "attempts"), Archiver: archiveadapter.NewTarZstd(), Hauler: store,
		Builder: haulkit.NewBuilderWithRuntimeObserver(store, observer), Verifier: haulkit.NewVerifier(store),
		Images: fakeRemoteImageResolver{}, Confinement: productionSeamConfinement{path: pastaExecutable},
		HaulerExecutable: haulerExecutable, HaulerVersion: "v2.0.2",
	})
	prepared, err := preparer.Prepare(context.Background(), RemoteDataPlaneRequest{
		SessionID: "production-seam", AttemptID: "production-seam-hauler-kit-v1", Capsule: "brain",
		Lineage: domain.Lineage{Branch: "main"}, Materialization: materialization, DevcontainerPath: devcontainer,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingProductionSeamRunner{}
	client := devpodadapter.NewClient("/managed/devpod", runner)
	if _, err := client.Up(context.Background(), devpodadapter.UpOptions{
		WorkspacePath: materialization, BootstrapPath: prepared.BootstrapRoot, SourceMode: devpodadapter.SourceModeBootstrap,
		WorkspaceID: "camp-brain", Context: "remote", Provider: "ssh", DevcontainerPath: ".camp-bootstrap/devcontainer.json",
	}); err != nil {
		t.Fatal(err)
	}
	if runner.command.Executable != "/managed/devpod" || len(runner.command.Argv) == 0 ||
		runner.command.Argv[len(runner.command.Argv)-1] != prepared.BootstrapRoot {
		t.Fatalf("DevPod command = %#v", runner.command)
	}
	if strings.Contains(strings.Join(runner.command.Argv, "\x00"), materialization) {
		t.Fatalf("DevPod argv leaked capsule materialization: %#v", runner.command.Argv)
	}
}

type fakeRemoteArchiver struct{ order *[]string }

func (f fakeRemoteArchiver) Create(_ context.Context, _, destination string) (archiveadapter.ArchiveInfo, error) {
	*f.order = append(*f.order, "archive")
	if err := os.WriteFile(destination, []byte("root"), 0o600); err != nil {
		return archiveadapter.ArchiveInfo{}, err
	}
	return archiveadapter.ArchiveInfo{Path: destination, SHA256: strings.Repeat("e", 64), Size: 4}, nil
}

type fakeRemoteHauler struct {
	order         *[]string
	addFileResult ports.Result
}

func (f *fakeRemoteHauler) AddFile(context.Context, string, string, string) (ports.Result, error) {
	*f.order = append(*f.order, "add-file")
	return f.addFileResult, nil
}
func (f *fakeRemoteHauler) AddImage(context.Context, string, hauleradapter.AddImageOptions) (ports.Result, error) {
	*f.order = append(*f.order, "add-image")
	return ports.Result{}, nil
}
func (f *fakeRemoteHauler) ValidateStore(context.Context, string) (haulkit.StoreIdentity, error) {
	return haulkit.StoreIdentity{}, nil
}

type fakeRemoteKitBuilder struct {
	order   *[]string
	request haulkit.BuildRequest
	err     error
}

func (f *fakeRemoteKitBuilder) Build(_ context.Context, request haulkit.BuildRequest) (haulkit.Artifact, error) {
	*f.order = append(*f.order, "build")
	f.request = request
	if f.err != nil {
		return haulkit.Artifact{}, f.err
	}
	manifest := filepath.Join(request.OutputDirectory, "camp-hauler-kit.json")
	archive := filepath.Join(request.OutputDirectory, "camp-hauler-kit.tar.zst")
	document := haulkit.Manifest{
		SchemaVersion: haulkit.ManifestSchemaVersion, Kind: "camp-hauler-kit", SessionID: request.SessionID,
		Capsule: request.Capsule, Lineage: request.Lineage, Architecture: request.Architecture,
		Store: haulkit.StoreIdentity{HaulerVersion: "v2.0.2", IndexSHA256: strings.Repeat("9", 64), Entries: []haulkit.StoreEntry{
			{Reference: "hauler/brain.tar.zst:latest", Type: "file", Digest: strings.Repeat("e", 64), Size: 4},
		}},
		Root: haulkit.RootIdentity{Reference: "hauler/brain.tar.zst:latest", SHA256: strings.Repeat("e", 64), Size: 4},
		Tools: haulkit.ToolIdentities{
			Camp:   haulkit.FileIdentity{Name: "camp", Version: "camp test", SHA256: digestString([]byte("helper")), Size: int64(len("helper"))},
			Hauler: haulkit.FileIdentity{Name: "hauler", Version: "v2.0.2", SHA256: strings.Repeat("b", 64), Size: 10},
			Pasta:  haulkit.FileIdentity{Name: "pasta", Version: "pasta 2026", SHA256: strings.Repeat("f", 64), Size: 10},
		},
		Archive: haulkit.ArchiveIdentity{SHA256: digestString([]byte("kit")), Size: 3},
		Chunks:  []haulkit.ChunkIdentity{{Index: 0, Name: "chunk-000000", SHA256: strings.Repeat("8", 64), Size: 3}},
	}
	body, _ := haulkit.MarshalCanonical(document)
	_ = os.WriteFile(manifest, body, 0o600)
	_ = os.WriteFile(archive, []byte("kit"), 0o600)
	return haulkit.Artifact{ManifestPath: manifest, ArchivePath: archive, SHA256: digestString([]byte("kit")), Size: 3}, nil
}

type fakeRemoteKitVerifier struct {
	order *[]string
	err   error
}

func (f *fakeRemoteKitVerifier) Verify(_ context.Context, request haulkit.VerifyRequest) (haulkit.VerifiedKit, error) {
	*f.order = append(*f.order, "verify")
	if f.err != nil {
		return haulkit.VerifiedKit{}, f.err
	}
	return haulkit.VerifiedKit{Manifest: haulkit.Manifest{
		Architecture: request.Architecture,
		Root:         haulkit.RootIdentity{Reference: "hauler/brain.tar.zst:latest", SHA256: strings.Repeat("e", 64), Size: 4},
		Tools: haulkit.ToolIdentities{
			Camp:   haulkit.FileIdentity{Name: "camp", Version: "camp test", SHA256: digestString([]byte("helper")), Size: int64(len("helper"))},
			Hauler: haulkit.FileIdentity{Name: "hauler", Version: "v2.0.2", SHA256: strings.Repeat("b", 64), Size: 10},
			Pasta:  haulkit.FileIdentity{Name: "pasta", Version: "pasta 2026", SHA256: strings.Repeat("f", 64), Size: 10},
		},
		Archive: haulkit.ArchiveIdentity{SHA256: digestString([]byte("kit")), Size: 3},
	}}, nil
}

type fakeRemoteImageResolver struct{}

func (fakeRemoteImageResolver) Resolve(context.Context, string) (string, error) {
	return "sha256:" + strings.Repeat("d", 64), nil
}

func (fakeRemoteImageResolver) ResolveConfigDigest(context.Context, string) (string, error) {
	return "sha256:" + strings.Repeat("c", 64), nil
}

type fakeRemoteConfinement struct{}

func (fakeRemoteConfinement) Resolve(context.Context) (ports.ConfinementCapability, error) {
	return ports.ConfinementCapability{Executable: "/usr/bin/pasta", Version: "pasta 2026"}, nil
}

func writeFakeRenderedBootstrap(request capsule.BootstrapRequest) (capsule.Bootstrap, error) {
	private := filepath.Join(request.Root, ".camp-bootstrap")
	if err := os.MkdirAll(private, 0o700); err != nil {
		return capsule.Bootstrap{}, err
	}
	if err := os.WriteFile(filepath.Join(request.Root, "camp-hauler-kit.tar.zst"), []byte("kit"), 0o600); err != nil {
		return capsule.Bootstrap{}, err
	}
	if err := os.WriteFile(filepath.Join(private, "camp-bootstrap"), []byte("helper"), 0o700); err != nil {
		return capsule.Bootstrap{}, err
	}
	manifest, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return capsule.Bootstrap{}, err
	}
	if err := os.WriteFile(filepath.Join(private, request.InitializeRequest.Expected.Manifest.Name), manifest, 0o600); err != nil {
		return capsule.Bootstrap{}, err
	}
	for name, worker := range map[string]remoteworker.Request{
		"initialize-request.json": request.InitializeRequest,
		"hydrate-request.json":    request.HydrateRequest,
		"services-request.json":   request.ServicesRequest,
	} {
		body, err := json.Marshal(worker)
		if err != nil {
			return capsule.Bootstrap{}, err
		}
		if err := os.WriteFile(filepath.Join(private, name), body, 0o600); err != nil {
			return capsule.Bootstrap{}, err
		}
	}
	document := fmt.Sprintf(`{"image":%q,"initializeCommand":%q,"onCreateCommand":%q,"postStartCommand":%q}`,
		request.OuterImage,
		".camp-bootstrap/camp-bootstrap __remote-worker < .camp-bootstrap/initialize-request.json",
		".camp-bootstrap/camp-bootstrap __remote-worker < .camp-bootstrap/hydrate-request.json",
		".camp-bootstrap/camp-bootstrap __remote-worker < .camp-bootstrap/services-request.json")
	path := filepath.Join(private, "devcontainer.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		return capsule.Bootstrap{}, err
	}
	return capsule.Bootstrap{Root: request.Root, DevcontainerPath: path}, nil
}

func digestString(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func fakeCompletedPreparer(t *testing.T, order *[]string) (*RemoteDataPlanePreparer, RemoteDataPlaneRequest) {
	t.Helper()
	preparer := NewRemoteDataPlanePreparer(RemoteDataPlaneDependencies{
		Root: t.TempDir(), Archiver: fakeRemoteArchiver{order: order}, Hauler: &fakeRemoteHauler{order: order},
		Builder: &fakeRemoteKitBuilder{order: order}, Verifier: &fakeRemoteKitVerifier{order: order},
		Images: fakeRemoteImageResolver{}, Confinement: fakeRemoteConfinement{},
		HaulerExecutable: "/managed/hauler", HaulerVersion: "v2.0.2",
	})
	preparer.render = writeFakeRenderedBootstrap
	root := t.TempDir()
	config := filepath.Join(root, "devcontainer.json")
	if err := os.WriteFile(config, []byte(`{"image":"example.test/workspace:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return preparer, RemoteDataPlaneRequest{
		SessionID: "session-1", AttemptID: "session-1-hauler-kit-v1", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Materialization: root, DevcontainerPath: config,
	}
}

type productionSeamHauler struct{}

func (productionSeamHauler) AddFile(_ context.Context, store, source, _ string) (ports.Result, error) {
	body, err := os.ReadFile(source)
	if err != nil {
		return ports.Result{}, err
	}
	return ports.Result{}, os.WriteFile(filepath.Join(store, "root.bin"), body, 0o600)
}

func (productionSeamHauler) AddImage(_ context.Context, store string, _ hauleradapter.AddImageOptions) (ports.Result, error) {
	return ports.Result{}, os.WriteFile(filepath.Join(store, "image.bin"), []byte("image"), 0o600)
}

func (productionSeamHauler) ValidateStore(_ context.Context, store string) (haulkit.StoreIdentity, error) {
	root, err := os.ReadFile(filepath.Join(store, "root.bin"))
	if err != nil {
		return haulkit.StoreIdentity{}, err
	}
	image, err := os.ReadFile(filepath.Join(store, "image.bin"))
	if err != nil {
		return haulkit.StoreIdentity{}, err
	}
	entries := []haulkit.StoreEntry{
		{Reference: "hauler/brain.tar.zst:latest", Type: "file", Digest: digestString(root), Size: int64(len(root))},
		{Reference: "example.test/workspace", Type: "image", Platform: "linux/" + runtime.GOARCH, Digest: digestString(image), Size: int64(len(image))},
	}
	return haulkit.StoreIdentity{HaulerVersion: "v2.0.2", IndexSHA256: digestString([]byte(fmt.Sprint(entries))), Entries: entries}, nil
}

func (productionSeamHauler) PrepareStore(_ context.Context, source, destination string) (haulkit.StoreIdentity, error) {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return haulkit.StoreIdentity{}, err
	}
	for _, name := range []string{"root.bin", "image.bin"} {
		body, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return haulkit.StoreIdentity{}, err
		}
		if err := os.WriteFile(filepath.Join(destination, name), body, 0o600); err != nil {
			return haulkit.StoreIdentity{}, err
		}
	}
	return productionSeamHauler{}.ValidateStore(context.Background(), destination)
}

func (productionSeamHauler) ObserveRoot(_ context.Context, store, reference string) (haulkit.RootIdentity, error) {
	body, err := os.ReadFile(filepath.Join(store, "root.bin"))
	if err != nil {
		return haulkit.RootIdentity{}, err
	}
	return haulkit.RootIdentity{Reference: reference, SHA256: digestString(body), Size: int64(len(body))}, nil
}

type productionSeamRuntimeObserver struct{}

func (productionSeamRuntimeObserver) OpenRunningExecutable() (*os.File, error) {
	return os.Open("/proc/self/exe")
}

func (productionSeamRuntimeObserver) Probe(_ context.Context, _ string, kind string) (string, error) {
	switch kind {
	case "camp":
		return "camp production-seam", nil
	case "hauler":
		return "hauler v2.0.2", nil
	case "pasta":
		return "pasta 2026", nil
	default:
		return "", errors.New("unknown tool")
	}
}

type productionSeamConfinement struct{ path string }

func (c productionSeamConfinement) Resolve(context.Context) (ports.ConfinementCapability, error) {
	return ports.ConfinementCapability{Executable: c.path, Version: "pasta 2026"}, nil
}

type recordingProductionSeamRunner struct{ command ports.Command }

func (r *recordingProductionSeamRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.command = command
	return ports.Result{}, nil
}
