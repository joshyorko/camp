package hauler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type generationRunner struct {
	commands []ports.Command
	stores   []string
	info     []byte
}

func (r *generationRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.commands = append(r.commands, command)
	if len(command.Argv) >= 3 && command.Argv[0] == "store" && command.Argv[1] == "--store" {
		r.stores = append(r.stores, command.Argv[2])
	}
	for index, argument := range command.Argv {
		if argument == "save" && index+2 < len(command.Argv) && command.Argv[index+1] == "--filename" {
			if err := os.WriteFile(command.Argv[index+2], []byte("verified-haul"), 0o600); err != nil {
				return ports.Result{}, err
			}
		}
	}
	if strings.Contains(strings.Join(command.Argv, " "), " info ") {
		return ports.Result{Stdout: r.info}, nil
	}
	return ports.Result{}, nil
}

func TestGenerationAssemblerUsesFreshStoresAndRealLoadInfoValidation(t *testing.T) {
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
	runner := &generationRunner{info: []byte(`[
  {"Reference":"hauler/brain.tar.zst:latest","Type":"file","Platform":"-","Digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
  {"Reference":"127.0.0.1:5000/camp/app:captured","Type":"image","Platform":"linux/amd64","Digest":"` + digest + `"}
]`)}
	assembler := NewGenerationAssembler(NewClient("/opt/hauler", runner))
	output := filepath.Join(build, "generation.tar.zst")
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
