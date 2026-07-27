package capsule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fixedDigestResolver struct {
	digest string
	calls  int
}

func (r *fixedDigestResolver) Resolve(context.Context, string) (string, error) {
	r.calls++
	return r.digest, nil
}

func TestInitializerWritesStableCapsuleDocumentsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &fixedDigestResolver{digest: "sha256:4d6143465165b95a184ea96053567a4573f22a5690156813145ebe13a321e972"}
	created := time.Unix(100, 0).UTC()
	initializer := NewInitializer(fixedClock{now: created}, resolver)
	result, err := initializer.Initialize(context.Background(), root, "second-brain")
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if result.Metadata.ID != "second-brain" || result.Metadata.CreatedAt != created || result.Lock.Room.Digest != resolver.digest {
		t.Fatalf("result = %#v", result)
	}
	for _, name := range []string{"capsule.yaml", "lock.yaml", "images.json", "hauler-manifest.yaml"} {
		if info, err := os.Stat(filepath.Join(root, ".camp", name)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("stable document %s = %v, %v", name, info, err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(root, ".camp", "capsule.yaml"))
	second, err := initializer.Initialize(context.Background(), root, "second-brain")
	if err != nil || second.Metadata.CreatedAt != created {
		t.Fatalf("second Initialize() = %#v, %v", second, err)
	}
	after, _ := os.ReadFile(filepath.Join(root, ".camp", "capsule.yaml"))
	if string(before) != string(after) || resolver.calls != 1 {
		t.Fatalf("idempotent initialization drifted; resolver calls=%d", resolver.calls)
	}
}

func TestInitializerRejectsExistingDifferentCapsuleWithoutOverwrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".camp", "capsule.yaml")
	if err := os.WriteFile(path, []byte("schemaVersion: 1\nid: other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	initializer := NewInitializer(fixedClock{now: time.Now()}, &fixedDigestResolver{digest: "sha256:abc"})
	if _, err := initializer.Initialize(context.Background(), root, "second-brain"); !errors.Is(err, ErrInitializationConflict) {
		t.Fatalf("Initialize() error = %v, want ErrInitializationConflict", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "schemaVersion: 1\nid: other\n" {
		t.Fatalf("existing metadata overwritten: %q", body)
	}
}

var _ = domain.SchemaVersion

type digestRunner struct{ command ports.Command }

func (r *digestRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.command = command
	return ports.Result{Stdout: []byte(`[{"Descriptor":{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}]`)}, nil
}

type configDigestRunner struct{ command ports.Command }

func (r *configDigestRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.command = command
	return ports.Result{Stdout: []byte(`{"schemaVersion":2,"config":{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}`)}, nil
}

func TestCommandDigestResolverUsesDockerManifestWithoutShell(t *testing.T) {
	t.Parallel()
	runner := &digestRunner{}
	digest, err := NewCommandDigestResolver("/usr/bin/docker", runner).Resolve(context.Background(), roomImage)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || runner.command.Executable != "/usr/bin/docker" || len(runner.command.Argv) != 4 || runner.command.Argv[3] != roomImage {
		t.Fatalf("digest=%q command=%#v", digest, runner.command)
	}
}

func TestCommandDigestResolverReturnsImmutableLocalImageIDFromManifestConfig(t *testing.T) {
	t.Parallel()
	runner := &configDigestRunner{}
	imageID, err := NewCommandDigestResolver("/usr/bin/docker", runner).ResolveConfigDigest(context.Background(), roomImage+"@sha256:"+string(make([]byte, 64)))
	if err != nil {
		t.Fatal(err)
	}
	if imageID != "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" ||
		runner.command.Executable != "/usr/bin/docker" ||
		len(runner.command.Argv) != 3 || runner.command.Argv[0] != "manifest" ||
		runner.command.Argv[1] != "inspect" {
		t.Fatalf("imageID=%q command=%#v", imageID, runner.command)
	}
}
