package hauler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/ports"
)

type generationRunner struct {
	commands []ports.Command
	stores   []string
	info     []byte
	version  []byte
	extract  []byte
	syncRuns []struct {
		result ports.Result
		err    error
	}
	syncCall int
}

func (r *generationRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.commands = append(r.commands, command)
	if len(command.Argv) == 1 && command.Argv[0] == "version" {
		return ports.Result{Stdout: r.version}, nil
	}
	if len(command.Argv) >= 3 && command.Argv[0] == "store" && command.Argv[1] == "--store" {
		r.stores = append(r.stores, command.Argv[2])
	}
	if len(command.Argv) >= 4 && command.Argv[3] == "sync" && r.syncCall < len(r.syncRuns) {
		run := r.syncRuns[r.syncCall]
		r.syncCall++
		return run.result, run.err
	}
	for index, argument := range command.Argv {
		if argument == "save" && index+2 < len(command.Argv) && command.Argv[index+1] == "--filename" {
			if err := os.WriteFile(command.Argv[index+2], []byte("verified-haul"), 0o600); err != nil {
				return ports.Result{}, err
			}
		}
		if argument == "extract" && index+3 < len(command.Argv) && command.Argv[index+2] == "--output" {
			if err := os.WriteFile(command.Argv[index+3], r.extract, 0o600); err != nil {
				return ports.Result{}, err
			}
		}
	}
	if strings.Contains(strings.Join(command.Argv, " "), " info ") {
		return ports.Result{Stdout: r.info}, nil
	}
	return ports.Result{}, nil
}

func TestGenerationAssemblerRetriesSyncWithAnotherFreshStore(t *testing.T) {
	t.Parallel()
	root, manifest, build, output, info := generationFixture(t)
	runner := &generationRunner{
		info: info,
		syncRuns: []struct {
			result ports.Result
			err    error
		}{
			{result: ports.Result{ExitCode: 1, Stderr: []byte("temporary oras failure")}, err: errors.New("exit status 1")},
			{},
		},
	}

	artifact, err := NewGenerationAssembler(NewClient("/opt/hauler", runner)).Assemble(context.Background(), manifest, build, output)
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if !artifact.Validated {
		t.Fatalf("artifact = %#v", artifact)
	}
	if len(runner.commands) != 5 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if runner.stores[0] == runner.stores[1] || runner.stores[0] == "" || runner.stores[1] == "" {
		t.Fatalf("sync retry reused a failed store: %#v", runner.stores)
	}
	if runner.commands[0].Directory != root || runner.commands[1].Directory != root {
		t.Fatalf("Hauler sync directories = %q, %q; want %q", runner.commands[0].Directory, runner.commands[1].Directory, root)
	}
}

func TestGenerationAssemblerReportsBothFailedSyncAttempts(t *testing.T) {
	t.Parallel()
	_, manifest, build, output, _ := generationFixture(t)
	runner := &generationRunner{syncRuns: []struct {
		result ports.Result
		err    error
	}{
		{result: ports.Result{ExitCode: 1, Stderr: []byte("first store failed")}, err: errors.New("exit status 1")},
		{result: ports.Result{ExitCode: 1, Stderr: []byte("second store failed")}, err: errors.New("exit status 1")},
	}}

	_, err := NewGenerationAssembler(NewClient("/opt/hauler", runner)).Assemble(context.Background(), manifest, build, output)
	if err == nil || !strings.Contains(err.Error(), "first store failed") || !strings.Contains(err.Error(), "second store failed") {
		t.Fatalf("Assemble() error = %v", err)
	}
	if len(runner.stores) != 2 || runner.stores[0] == runner.stores[1] {
		t.Fatalf("sync attempts did not use distinct fresh stores: %#v", runner.stores)
	}
}

func TestGenerationAssemblerUsesFreshStoresAndRealLoadInfoValidation(t *testing.T) {
	t.Parallel()
	root, manifest, build, output, info := generationFixture(t)
	runner := &generationRunner{info: info}
	assembler := NewGenerationAssembler(NewClient("/opt/hauler", runner))
	artifact, err := assembler.Assemble(context.Background(), manifest, build, output)
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if !artifact.Validated || artifact.Size != int64(len("verified-haul")) || len(artifact.SHA256) != 64 {
		t.Fatalf("artifact = %#v", artifact)
	}
	if len(runner.commands) != 4 { // sync, save, load, info
		t.Fatalf("commands = %#v", runner.commands)
	}
	if runner.stores[0] == runner.stores[2] || runner.stores[0] == "" || runner.stores[2] == "" {
		t.Fatalf("generation/validation stores were not fresh: %#v", runner.stores)
	}
	if runner.commands[0].Directory != root {
		t.Fatalf("Hauler sync directory = %q, want capsule root %q", runner.commands[0].Directory, root)
	}
}

func generationFixture(t *testing.T) (root, manifest, build, output string, info []byte) {
	t.Helper()
	root = t.TempDir()
	camp := filepath.Join(root, ".camp")
	build = filepath.Join(camp, "build")
	if err := os.MkdirAll(build, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "brain.tar.zst"), []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest = filepath.Join(camp, "hauler-manifest.yaml")
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifestBody := `apiVersion: content.hauler.cattle.io/v1
kind: Files
metadata:
  name: camp-brain
spec:
  files:
    - path: .camp/build/brain.tar.zst
      name: brain.tar.zst
---
apiVersion: content.hauler.cattle.io/v1
kind: Images
metadata:
  name: camp-brain-images
spec:
  images:
    - name: 127.0.0.1:5000/camp/app@` + digest + `
      platform: linux/amd64
`
	if err := os.WriteFile(manifest, []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	output = filepath.Join(build, "generation.tar.zst")
	info = []byte(`[
  {"Reference":"hauler/brain.tar.zst:latest","Type":"file","Platform":"-","Digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
  {"Reference":"127.0.0.1:5000/camp/app:captured","Type":"image","Platform":"linux/amd64","Digest":"` + digest + `"}
]`)
	return root, manifest, build, output, info
}

func TestGenerationAssemblerRejectsInfoMissingExpectedDigestAndRemovesInvalidHaul(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	camp := filepath.Join(root, ".camp")
	build := filepath.Join(camp, "build")
	if err := os.MkdirAll(build, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "brain.tar.zst"), []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(camp, "hauler-manifest.yaml")
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	body := `apiVersion: content.hauler.cattle.io/v1
kind: Files
metadata: {name: camp-brain}
spec:
  files: [{path: .camp/build/brain.tar.zst, name: brain.tar.zst}]
---
apiVersion: content.hauler.cattle.io/v1
kind: Images
metadata: {name: camp-brain-images}
spec:
  images: [{name: 127.0.0.1:5000/camp/app@` + digest + `, platform: linux/amd64}]
`
	if err := os.WriteFile(manifest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(build, "generation.tar.zst")
	runner := &generationRunner{info: []byte(`[{"Reference":"hauler/brain.tar.zst:latest","Type":"file","Platform":"-","Digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`)}
	_, err := NewGenerationAssembler(NewClient("/opt/hauler", runner)).Assemble(context.Background(), manifest, build, output)
	if err == nil || !strings.Contains(err.Error(), "expected image digest") {
		t.Fatalf("Assemble() error = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("invalid generation remains at %q: %v", output, statErr)
	}
}

func TestValidateGenerationInfoAcceptsPlatformResolvedDigestPinnedReference(t *testing.T) {
	t.Parallel()
	indexDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	childDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err := validateGenerationInfo(
		generationExpectations{Images: []expectedGenerationImage{{Digest: indexDigest, Platform: "linux/amd64"}}},
		[]generationInfoEntry{{Reference: "127.0.0.1:5000/camp/app@" + indexDigest, Type: "image", Platform: "linux/amd64", Digest: childDigest}},
	)
	if err != nil {
		t.Fatalf("validateGenerationInfo() error = %v", err)
	}
}

func TestClientValidatesStoreIntoStableHaulKitIdentity(t *testing.T) {
	t.Parallel()
	runner := &generationRunner{version: []byte("v2.0.2\n"), info: []byte(`[
	  {"Reference":"z/image:tag","Type":"image","Platform":"linux/amd64","Digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	  {"Reference":"hauler/root.tar.zst:latest","Type":"file","Platform":"-","Digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	]`)}
	client := NewClientWithVersion("/opt/hauler", "v2.0.2", runner)
	first, err := client.ValidateStore(context.Background(), "/tmp/fresh-store")
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.ValidateStore(context.Background(), "/tmp/fresh-store")
	if err != nil {
		t.Fatal(err)
	}
	if first.HaulerVersion != "v2.0.2" || len(first.Entries) != 2 || first.Entries[0].Reference != "hauler/root.tar.zst:latest" {
		t.Fatalf("identity = %#v", first)
	}
	if first.IndexSHA256 == "" || first.IndexSHA256 != second.IndexSHA256 {
		t.Fatalf("unstable index identity: %#v != %#v", first, second)
	}
	if err := haulkit.Validate(haulkit.Manifest{
		SchemaVersion: haulkit.ManifestSchemaVersion,
		Kind:          "camp-hauler-kit",
		SessionID:     "session",
		Capsule:       "capsule",
		Lineage:       domain.Lineage{Branch: "main"},
		Architecture:  "linux/amd64",
		Store:         first,
		Root:          haulkit.RootIdentity{Reference: "hauler/root.tar.zst:latest", SHA256: strings.Repeat("a", 64), Size: 1},
		Tools: haulkit.ToolIdentities{
			Camp:   haulkit.FileIdentity{Name: "camp", Version: "dev", SHA256: strings.Repeat("a", 64), Size: 1},
			Hauler: haulkit.FileIdentity{Name: "hauler", Version: "v2.0.2", SHA256: strings.Repeat("a", 64), Size: 1},
			Pasta:  haulkit.FileIdentity{Name: "pasta", Version: "1", SHA256: strings.Repeat("a", 64), Size: 1},
		},
		Archive: haulkit.ArchiveIdentity{SHA256: strings.Repeat("a", 64), Size: 1},
		Chunks:  []haulkit.ChunkIdentity{{Index: 0, Name: "part", SHA256: strings.Repeat("a", 64), Size: 1}},
	}); err != nil {
		t.Fatalf("HaulKit rejected validated store identity: %v", err)
	}
}

func TestClientPreparesStoreThroughOfficialSaveLoadAndObservedVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &generationRunner{
		version: []byte("v2.0.2\n"),
		info:    []byte(`[{"Reference":"hauler/root.tar.zst:latest","Type":"file","Platform":"-","Digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`),
	}
	client := NewClientWithVersion("/opt/hauler", "v2.0.2", runner)
	identity, err := client.PrepareStore(context.Background(), source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if identity.HaulerVersion != "v2.0.2" {
		t.Fatalf("identity = %#v", identity)
	}
	if len(runner.commands) != 4 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if got := runner.commands[0].Argv; !reflect.DeepEqual(got[:4], []string{"store", "--store", source, "save"}) {
		t.Fatalf("save argv = %#v", got)
	}
	if got := runner.commands[1].Argv; !reflect.DeepEqual(got[:4], []string{"store", "--store", destination, "load"}) {
		t.Fatalf("load argv = %#v", got)
	}
}

func TestClientRejectsConfiguredVersionNotObservedFromExecutable(t *testing.T) {
	t.Parallel()
	runner := &generationRunner{
		version: []byte("v9.9.9\n"),
		info:    []byte(`[{"Reference":"hauler/root.tar.zst:latest","Type":"file","Platform":"-","Digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`),
	}
	_, err := NewClientWithVersion("/opt/hauler", "v2.0.2", runner).ValidateStore(context.Background(), "/tmp/store")
	if err == nil {
		t.Fatal("ValidateStore() accepted configured version not observed from executable")
	}
}

func TestClientObservesCanonicalRootBytesWhenInfoOmitsSize(t *testing.T) {
	t.Parallel()
	rootBytes := []byte("root-archive-bytes")
	sum := sha256.Sum256(rootBytes)
	digest := hex.EncodeToString(sum[:])
	runner := &generationRunner{
		version: []byte("v2.0.2\n"),
		extract: rootBytes,
		info:    []byte(`[{"Reference":"hauler/root.tar.zst:latest","Type":"file","Platform":"-","Digest":"sha256:` + digest + `"}]`),
	}
	identity, err := NewClientWithVersion("/opt/hauler", "v2.0.2", runner).
		ObserveRoot(context.Background(), "/tmp/store", "root.tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Reference != "hauler/root.tar.zst:latest" || identity.SHA256 != digest || identity.Size != int64(len(rootBytes)) {
		t.Fatalf("root identity = %#v", identity)
	}
}
