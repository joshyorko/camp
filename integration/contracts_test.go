package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/ports"
)

type execRunner struct{ environment map[string]string }

func (r execRunner) Run(ctx context.Context, command ports.Command) (ports.Result, error) {
	cmd := exec.CommandContext(ctx, command.Executable, command.Argv...)
	cmd.Dir = command.Directory
	cmd.Env = os.Environ()
	for key, value := range r.environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	for key, value := range command.Environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdin = command.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := ports.Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, err
}

func loadDistributionLock(t *testing.T) tools.Lock {
	t.Helper()
	lockFile, err := os.Open("../tools.lock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := tools.ParseLock(lockFile)
	_ = lockFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	return lock
}

func TestInstalledDevPodIdentity(t *testing.T) {
	lock := loadDistributionLock(t)

	devpodPath, err := exec.LookPath("devpod")
	if err != nil {
		t.Skip("devpod is not installed")
	}
	tool, asset, err := lock.Resolve("devpod", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(devpodPath, "version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != tool.Version {
		t.Fatalf("devpod version: output=%q error=%v", version, err)
	}
	file, err := os.Open(devpodPath)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	_ = file.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != asset.SHA256 {
		t.Fatalf("devpod sha256 = %s, want %s", got, asset.SHA256)
	}
}

func TestInstalledHaulerIdentity(t *testing.T) {
	lock := loadDistributionLock(t)

	haulerPath, err := exec.LookPath("hauler")
	if err != nil {
		t.Skip("hauler is not installed")
	}
	haulerTool, _, err := lock.Resolve("hauler", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	haulerVersion, err := exec.Command(haulerPath, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(haulerVersion), "GitVersion:    "+haulerTool.Version) || !strings.Contains(string(haulerVersion), "GitCommit:     "+haulerTool.Commit[:7]) {
		t.Fatalf("hauler version contract: output=%q error=%v", haulerVersion, err)
	}
}

func TestRealHaulerFileSaveLoadExtractRoundTrip(t *testing.T) {
	haulerPath, err := exec.LookPath("hauler")
	if err != nil {
		t.Skip("hauler is not installed")
	}
	root := t.TempDir()
	haulerHome := filepath.Join(root, "hauler-home")
	sourceStore := filepath.Join(root, "source-store")
	loadedStore := filepath.Join(root, "loaded-store")
	extracted := filepath.Join(root, "extracted")
	haul := filepath.Join(root, "capsule.tar.zst")
	fixture := filepath.Join(root, "fixture.txt")
	const content = "camp-hauler-contract\n"
	if err := os.WriteFile(fixture, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := execRunner{environment: map[string]string{"HOME": root}}
	add := ports.Command{Executable: haulerPath, Argv: []string{"--haulerdir", haulerHome, "store", "--store", sourceStore, "add", "file", fixture, "--name", "fixture.txt"}}
	if result, err := runner.Run(context.Background(), add); err != nil {
		t.Fatalf("add file: %v: %s", err, result.Stderr)
	}
	client := hauler.NewClient(haulerPath, runner)
	if result, err := client.Save(context.Background(), sourceStore, haul); err != nil {
		t.Fatalf("save: %v: %s", err, result.Stderr)
	}
	if result, err := client.Load(context.Background(), loadedStore, []string{haul}); err != nil {
		t.Fatalf("load: %v: %s", err, result.Stderr)
	}
	if result, err := client.Extract(context.Background(), loadedStore, "hauler/fixture.txt", extracted); err != nil {
		t.Fatalf("extract: %v: %s", err, result.Stderr)
	}
	got, err := os.ReadFile(filepath.Join(extracted, "fixture.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("extracted content = %q, want %q", got, content)
	}
}
