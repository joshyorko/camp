package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/ports"
)

func TestInstalledDevPodUploadsSingleImmutableKit(t *testing.T) {
	if os.Getenv("CAMP_TEST_DEVPOD_BOOTSTRAP") != "1" {
		t.Skip("set CAMP_TEST_DEVPOD_BOOTSTRAP=1 to run the real single-upload DevPod bootstrap contract")
	}
	if _, err := os.Stat("/usr/bin/docker"); err != nil {
		t.Fatalf("explicit DevPod bootstrap gate requires /usr/bin/docker: %v", err)
	}

	root := t.TempDir()
	capsuleRoot := filepath.Join(root, "capsule")
	bootstrapRoot := filepath.Join(root, "devpod-bootstrap")
	if err := os.MkdirAll(filepath.Join(capsuleRoot, ".devcontainer"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bootstrapRoot, ".camp-bootstrap"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capsuleRoot, ".devcontainer", "devcontainer.json"), []byte(`{"image":"busybox:1.36"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capsuleRoot, "kit-payload.bin"), deterministicKitPayload(2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	kitPath := filepath.Join(bootstrapRoot, "camp-hauler-kit.tar.zst")
	kit, err := archiveadapter.NewTarZstd().Create(context.Background(), capsuleRoot, kitPath)
	if err != nil {
		t.Fatalf("construct immutable kit archive: %v", err)
	}
	if kit.Size <= 1<<20 {
		t.Fatalf("kit archive size = %d, want a meaningful payload larger than metadata limit", kit.Size)
	}
	if err := os.Chmod(kitPath, 0o400); err != nil {
		t.Fatalf("make kit archive read-only before DevPod upload: %v", err)
	}
	bootstrapConfig := []byte(`{"image":"alpine:3.20","postCreateCommand":"sha256sum camp-hauler-kit.tar.zst > .camp-bootstrap/kit.sha256"}`)
	if err := os.WriteFile(filepath.Join(bootstrapRoot, ".camp-bootstrap", "devcontainer.json"), bootstrapConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	assertBootstrapFootprint(t, bootstrapRoot, kitPath)
	t.Cleanup(func() {
		if err := removeRootOwnedBootstrap(root, bootstrapRoot); err != nil {
			t.Errorf("remove exact disposable bootstrap source: %v", err)
		}
	})

	isolation := newDevPodTestIsolation(root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	version, err := runLifecycleCommand(ctx, isolation.Environment(), "devpod", "version")
	if err != nil {
		t.Fatalf("read installed DevPod version: %v\n%s", err, version)
	}
	if got := strings.TrimSpace(string(version)); got != "v0.26.1" {
		t.Fatalf("installed DevPod version = %q, want pinned v0.26.1", got)
	}
	if output, err := bootstrapDevPodDockerProvider(ctx, isolation); err != nil {
		t.Fatalf("bootstrap private Docker provider: %v\n%s", err, output)
	}
	workspaceID := "camp-bootstrap-" + strings.TrimPrefix(isolation.context, "camp-test-")
	created := false
	t.Cleanup(func() {
		if !created {
			return
		}
		if output, err := runDevPodCommand(context.Background(), isolation, "delete", "--ignore-not-found", workspaceID); err != nil {
			t.Errorf("delete exact test workspace %q: %v\n%s", workspaceID, err, output)
		}
	})

	runner := &countingDevPodRunner{delegate: execRunner{environment: environmentMap(isolation.Environment())}}
	client := devpod.NewClient("devpod", runner)
	result, err := client.Up(ctx, devpod.UpOptions{
		WorkspacePath: capsuleRoot, BootstrapPath: bootstrapRoot, SourceMode: devpod.SourceModeBootstrap,
		WorkspaceID: workspaceID, Context: isolation.context, Provider: "docker", DevcontainerPath: ".camp-bootstrap/devcontainer.json",
	})
	if err != nil {
		t.Fatalf("single bootstrap up: %v\nstdout:\n%s\nstderr:\n%s", err, result.Stdout, result.Stderr)
	}
	created = true
	assertSingleBootstrapUp(t, runner.upCommands, bootstrapRoot, capsuleRoot)

	remoteRoot, err := client.ResolveWorkspaceFolderInContext(ctx, isolation.context, workspaceID)
	if err != nil {
		t.Fatalf("resolve bootstrapped remote workspace: %v", err)
	}
	remoteKit := filepath.ToSlash(filepath.Join(remoteRoot, filepath.Base(kitPath)))
	receiptPath := filepath.ToSlash(filepath.Join(remoteRoot, ".camp-bootstrap", "kit.sha256"))
	receiptResult, err := client.Execute(ctx, devpod.WorkspaceCommand{
		Context: isolation.context, WorkspaceID: workspaceID,
		Argv: []string{"cat", receiptPath},
	})
	if err != nil {
		t.Fatalf("read post-create kit receipt through structured DevPod SSH: %v\n%s", err, receiptResult.Stderr)
	}
	receiptFields := strings.Fields(string(receiptResult.Stdout))
	if len(receiptFields) != 2 || receiptFields[0] != kit.SHA256 || receiptFields[1] != filepath.Base(kitPath) {
		t.Fatalf("post-create kit receipt = %q, want %s  %s", receiptResult.Stdout, kit.SHA256, filepath.Base(kitPath))
	}
	digestResult, err := client.Execute(ctx, devpod.WorkspaceCommand{
		Context: isolation.context, WorkspaceID: workspaceID,
		Argv: []string{"sha256sum", remoteKit},
	})
	if err != nil {
		t.Fatalf("hash remotely uploaded kit through structured DevPod SSH: %v\n%s", err, digestResult.Stderr)
	}
	fields := strings.Fields(string(digestResult.Stdout))
	if len(fields) != 2 || fields[0] != kit.SHA256 || fields[1] != remoteKit {
		t.Fatalf("remote kit digest output = %q, want %s  %s", digestResult.Stdout, kit.SHA256, remoteKit)
	}
	assertRecordedWorkspaceSource(t, ctx, isolation, workspaceID, bootstrapRoot, capsuleRoot)
}

type countingDevPodRunner struct {
	delegate   ports.Runner
	upCommands []ports.Command
}

func (r *countingDevPodRunner) Run(ctx context.Context, command ports.Command) (ports.Result, error) {
	if len(command.Argv) > 0 && command.Argv[0] == "up" {
		r.upCommands = append(r.upCommands, command)
	}
	return r.delegate.Run(ctx, command)
}

func deterministicKitPayload(size int) []byte {
	body := make([]byte, size)
	state := uint32(0x9e3779b9)
	for index := range body {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		body[index] = byte(state)
	}
	return body
}

func assertSingleBootstrapUp(t *testing.T, commands []ports.Command, bootstrapRoot, capsuleRoot string) {
	t.Helper()
	if len(commands) != 1 {
		t.Fatalf("DevPod up calls = %d, want exactly one: %#v", len(commands), commands)
	}
	wantTail := []string{"--devcontainer-path", ".camp-bootstrap/devcontainer.json", bootstrapRoot}
	argv := commands[0].Argv
	if len(argv) < len(wantTail) || !reflect.DeepEqual(argv[len(argv)-len(wantTail):], wantTail) {
		t.Fatalf("single DevPod up argv = %#v, want tail %#v", argv, wantTail)
	}
	for _, argument := range argv {
		if argument == capsuleRoot {
			t.Fatalf("single DevPod up exposed capsule root: %#v", argv)
		}
	}
}

func removeRootOwnedBootstrap(testRoot, bootstrapRoot string) error {
	if filepath.Dir(bootstrapRoot) != testRoot || filepath.Base(bootstrapRoot) != "devpod-bootstrap" {
		return fmt.Errorf("refusing unexpected bootstrap cleanup path %q", bootstrapRoot)
	}
	if err := os.RemoveAll(bootstrapRoot); err == nil {
		return nil
	}
	command := exec.Command(
		"/usr/bin/docker", "run", "--rm",
		"--volume", testRoot+":/cleanup",
		"alpine:3.20", "rm", "-rf", "/cleanup/devpod-bootstrap",
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("root-owned cleanup: %w: %s", err, output)
	}
	return nil
}

func environmentMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func assertBootstrapFootprint(t *testing.T, root, kitPath string) {
	t.Helper()
	var regularFiles, nonKitBytes int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		regularFiles++
		if path != kitPath {
			nonKitBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if regularFiles > 16 || nonKitBytes > 1<<20 {
		t.Fatalf("bootstrap footprint regular-files=%d non-kit-bytes=%d, want at most 16 files and 1 MiB metadata", regularFiles, nonKitBytes)
	}
}

func assertRecordedWorkspaceSource(t *testing.T, ctx context.Context, isolation devPodTestIsolation, workspaceID, bootstrapRoot, capsuleRoot string) {
	t.Helper()
	output, err := runDevPodCommand(ctx, isolation, "list", "--output", "json")
	if err != nil {
		t.Fatalf("list private DevPod workspaces: %v\n%s", err, output)
	}
	var workspaces []struct {
		ID     string `json:"id"`
		Source struct {
			LocalFolder string `json:"localFolder"`
		} `json:"source"`
	}
	if err := json.Unmarshal(output, &workspaces); err != nil {
		t.Fatalf("decode DevPod workspace list: %v\n%s", err, output)
	}
	for _, workspace := range workspaces {
		if workspace.ID != workspaceID {
			continue
		}
		if workspace.Source.LocalFolder != bootstrapRoot {
			t.Fatalf("recorded source = %q, want bootstrap root %q (capsule root %q)", workspace.Source.LocalFolder, bootstrapRoot, capsuleRoot)
		}
		return
	}
	t.Fatalf("exact owned workspace %q absent from private DevPod list: %s", workspaceID, fmt.Sprintf("%s", output))
}
