package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/ports"
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
		if request.OuterImage != "example.test/workspace:v1@sha256:"+strings.Repeat("d", 64) {
			t.Fatalf("outer image = %q", request.OuterImage)
		}
		if request.InitializeRequest.Expected.Kit.SHA256 != strings.Repeat("a", 64) {
			t.Fatalf("expected kit = %#v", request.InitializeRequest.Expected.Kit)
		}
		return capsule.Bootstrap{Root: request.Root, DevcontainerPath: filepath.Join(request.Root, ".camp-bootstrap", "devcontainer.json")}, nil
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
		private := filepath.Join(request.Root, ".camp-bootstrap")
		if err := os.MkdirAll(private, 0o700); err != nil {
			return capsule.Bootstrap{}, err
		}
		body, err := json.Marshal(request.InitializeRequest)
		if err != nil {
			return capsule.Bootstrap{}, err
		}
		if err := os.WriteFile(filepath.Join(private, "initialize-request.json"), body, 0o600); err != nil {
			return capsule.Bootstrap{}, err
		}
		return capsule.Bootstrap{Root: request.Root}, nil
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

type fakeRemoteArchiver struct{ order *[]string }

func (f fakeRemoteArchiver) Create(_ context.Context, _, destination string) (archiveadapter.ArchiveInfo, error) {
	*f.order = append(*f.order, "archive")
	if err := os.WriteFile(destination, []byte("root"), 0o600); err != nil {
		return archiveadapter.ArchiveInfo{}, err
	}
	return archiveadapter.ArchiveInfo{Path: destination, SHA256: strings.Repeat("e", 64), Size: 4}, nil
}

type fakeRemoteHauler struct{ order *[]string }

func (f *fakeRemoteHauler) AddFile(context.Context, string, string, string) (ports.Result, error) {
	*f.order = append(*f.order, "add-file")
	return ports.Result{}, nil
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
			Camp:   haulkit.FileIdentity{Name: "camp", Version: "camp test", SHA256: strings.Repeat("c", 64), Size: 10},
			Hauler: haulkit.FileIdentity{Name: "hauler", Version: "v2.0.2", SHA256: strings.Repeat("b", 64), Size: 10},
			Pasta:  haulkit.FileIdentity{Name: "pasta", Version: "pasta 2026", SHA256: strings.Repeat("f", 64), Size: 10},
		},
		Archive: haulkit.ArchiveIdentity{SHA256: strings.Repeat("a", 64), Size: 3},
		Chunks:  []haulkit.ChunkIdentity{{Index: 0, Name: "chunk-000000", SHA256: strings.Repeat("8", 64), Size: 3}},
	}
	body, _ := haulkit.MarshalCanonical(document)
	_ = os.WriteFile(manifest, body, 0o600)
	_ = os.WriteFile(archive, []byte("kit"), 0o600)
	return haulkit.Artifact{ManifestPath: manifest, ArchivePath: archive, SHA256: strings.Repeat("a", 64), Size: 3}, nil
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
			Camp:   haulkit.FileIdentity{Name: "camp", Version: "camp test", SHA256: strings.Repeat("c", 64), Size: 10},
			Hauler: haulkit.FileIdentity{Name: "hauler", Version: "v2.0.2", SHA256: strings.Repeat("b", 64), Size: 10},
			Pasta:  haulkit.FileIdentity{Name: "pasta", Version: "pasta 2026", SHA256: strings.Repeat("f", 64), Size: 10},
		},
		Archive: haulkit.ArchiveIdentity{SHA256: strings.Repeat("a", 64), Size: 3},
	}}, nil
}

type fakeRemoteImageResolver struct{}

func (fakeRemoteImageResolver) Resolve(context.Context, string) (string, error) {
	return "sha256:" + strings.Repeat("d", 64), nil
}

type fakeRemoteConfinement struct{}

func (fakeRemoteConfinement) Resolve(context.Context) (ports.ConfinementCapability, error) {
	return ports.ConfinementCapability{Executable: "/usr/bin/pasta", Version: "pasta 2026"}, nil
}
