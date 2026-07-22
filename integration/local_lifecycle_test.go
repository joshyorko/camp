package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLocalLifecycleVertical(t *testing.T) {
	if os.Getenv("CAMP_TEST_REAL_LIFECYCLE") != "1" {
		t.Skip("set CAMP_TEST_REAL_LIFECYCLE=1 to run the real DevPod/Hauler lifecycle")
	}
	for _, name := range []string{"go", "devpod", "hauler", "pasta", "docker"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required executable %q: %v", name, err)
		}
	}
	assertNoDevPodWorkspaces(t, context.Background())

	root := t.TempDir()
	source := filepath.Join(root, "mock Second Brain")
	backend := filepath.Join(root, "backend")
	controllerA := filepath.Join(root, "controller-a")
	controllerB := filepath.Join(root, "controller-b")
	bin := filepath.Join(root, "camp")
	registryPort, fileserverPort := reserveLoopbackPort(t), reserveLoopbackPort(t)
	writeLifecycleFixture(t, source)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", bin, "../cmd/camp")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build camp: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	t.Cleanup(cancel)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cleanupCancel()
		workspaces, _ := listDevPodWorkspaces(cleanupCtx)
		for _, workspace := range workspaces {
			_, _ = runLifecycleCommand(cleanupCtx, nil, "devpod", "delete", "--context", "default", "--ignore-not-found", workspace)
		}
	})

	envA := lifecycleEnvironment(controllerA, source, backend, registryPort, fileserverPort)
	t.Log("initialize adopted fixture")
	mustRunLifecycle(t, ctx, envA, bin, "--json", "init", source)
	t.Log("open adopted fixture through real DevPod")
	recovered := decodeOpenResult(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "open", "Projects/Unicode space"))
	workspaceA := recovered.WorkspaceID
	if workspaceA == "" || recovered.SessionID == "" || recovered.Target != "Projects/Unicode space" {
		t.Fatalf("open = %#v", recovered)
	}
	workspaceRootA := "/workspaces/" + workspaceA
	t.Log("mutate files and publish a named image through CAMP_REGISTRY")
	mutate := fmt.Sprintf("set -eu; cd %s; printf 'after-open\\n' >> 'Projects/Unicode space/λ-note.txt'; chmod 600 'Projects/Unicode space/λ-note.txt'; test \"$CAMP_REGISTRY\" = %s; test \"$CAMP_FILESERVER\" = %s; wget -qO- \"http://$CAMP_REGISTRY/v2/\" >/dev/null; wget -qO- \"http://$CAMP_FILESERVER/\" >/dev/null; engine=; attempts=0; while test -z \"$engine\"; do for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; attempts=$((attempts+1)); test $attempts -lt 60; sleep 1; done; \"$engine\" pull alpine:3.20; image_id=$(\"$engine\" create alpine:3.20); \"$engine\" commit \"$image_id\" %s/camp-acceptance:named; \"$engine\" rm \"$image_id\"; \"$engine\" push %s/camp-acceptance:named", shellQuote(workspaceRootA), shellQuote(loopbackEndpoint(registryPort)), shellQuote(loopbackEndpoint(fileserverPort)), loopbackEndpoint(registryPort), loopbackEndpoint(registryPort))
	mustRunLifecycle(t, ctx, nil, "devpod", "ssh", "--context", "default", workspaceA, "--command", mutate)

	t.Log("publish explicit sync generation")
	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "sync")); generation != 1 {
		t.Fatalf("sync generation = %d, want 1", generation)
	}
	edit := fmt.Sprintf("printf 'after-sync\\n' >> %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRootA, "Projects/Unicode space/λ-note.txt"))))
	mustRunLifecycle(t, ctx, nil, "devpod", "ssh", "--context", "default", workspaceA, "--command", edit)
	t.Log("publish final generation and close adopted workspace")
	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "close")); generation != 2 {
		t.Fatalf("close generation = %d, want 2", generation)
	}
	assertNoDevPodWorkspaces(t, ctx)
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("adopted source did not survive: %v", err)
	}

	envB := lifecycleEnvironment(controllerB, "", backend, registryPort, fileserverPort)
	t.Log("reopen from the file backend with a fresh XDG controller")
	reopened := decodeOpenResult(t, mustRunLifecycle(t, ctx, envB, bin, "--json", "reopen"))
	if reopened.Generation != 2 || reopened.WorkspaceID == "" || reopened.Materialization == "" {
		t.Fatalf("fresh-controller reopen = %#v", reopened)
	}
	workspaceRootB := "/workspaces/" + reopened.WorkspaceID
	t.Log("verify restored filesystem semantics and runnable named image")
	note := shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "Projects/Unicode space/λ-note.txt")))
	verify := fmt.Sprintf("set -eu; grep -q before-open %s; grep -q after-open %s; grep -q after-sync %s; stat -c %%a %s | grep -qx 600; stat -c %%s %s | grep -qx %d; readlink %s | grep -qx README.md; find %s -xdev -samefile %s | grep -q README-hardlink.md; engine=; for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; test -n \"$engine\"; \"$engine\" image inspect %s/camp-acceptance:named >/dev/null; \"$engine\" run --rm %s/camp-acceptance:named true", note, note, note, note, shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "large.bin"))), lifecycleLargeSize, shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README-link.md"))), shellQuote(workspaceRootB), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README.md"))), loopbackEndpoint(registryPort), loopbackEndpoint(registryPort))
	mustRunLifecycle(t, ctx, nil, "devpod", "ssh", "--context", "default", reopened.WorkspaceID, "--command", verify)
	t.Log("close fresh controller and verify teardown")
	mustRunLifecycle(t, ctx, envB, bin, "--json", "close")
	assertNoDevPodWorkspaces(t, ctx)
	if _, err := os.Stat(reopened.Materialization); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned materialization still exists: %v", err)
	}
	assertLoopbackPortClosed(t, registryPort)
	assertLoopbackPortClosed(t, fileserverPort)
}

const lifecycleLargeSize = 3*1024*1024 + 1

type lifecycleOpenResult struct {
	SessionID, WorkspaceID, Target, Materialization string
	Generation                                      int
}

func decodeOpenResult(t *testing.T, output []byte) lifecycleOpenResult {
	t.Helper()
	var envelope struct {
		Result struct {
			Snapshot struct {
				SessionID        string `json:"sessionId"`
				OpenedGeneration *struct {
					Generation int `json:"generation"`
				} `json:"openedGeneration"`
				Workspace       struct{ ID, Target string } `json:"workspace"`
				Materialization struct {
					CanonicalPath string `json:"canonicalPath"`
				} `json:"materialization"`
			} `json:"Snapshot"`
			WorkspaceID string `json:"WorkspaceID"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode open: %v\n%s", err, output)
	}
	generation := 0
	if envelope.Result.Snapshot.OpenedGeneration != nil {
		generation = envelope.Result.Snapshot.OpenedGeneration.Generation
	}
	workspaceID := envelope.Result.WorkspaceID
	if workspaceID == "" {
		workspaceID = envelope.Result.Snapshot.Workspace.ID
	}
	return lifecycleOpenResult{envelope.Result.Snapshot.SessionID, workspaceID, envelope.Result.Snapshot.Workspace.Target, envelope.Result.Snapshot.Materialization.CanonicalPath, generation}
}

func decodeGeneration(t *testing.T, output []byte) int {
	t.Helper()
	var envelope struct {
		Result struct {
			Generation struct {
				Generation int `json:"generation"`
			} `json:"generation"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode generation: %v\n%s", err, output)
	}
	return envelope.Result.Generation.Generation
}

func lifecycleEnvironment(controller, source, backend string, registryPort, fileserverPort int) []string {
	env := []string{"XDG_CONFIG_HOME=" + filepath.Join(controller, "config"), "XDG_DATA_HOME=" + filepath.Join(controller, "data"), "XDG_CACHE_HOME=" + filepath.Join(controller, "cache"), "CAMP_BACKEND=file://" + backend, "CAMP_CAPSULE=default", "CAMP_REGISTRY_PORT=" + strconv.Itoa(registryPort), "CAMP_FILESERVER_PORT=" + strconv.Itoa(fileserverPort)}
	if source != "" {
		env = append(env, "CAMP_SOURCE="+source)
	}
	return env
}

func writeLifecycleFixture(t *testing.T, source string) {
	t.Helper()
	nested := filepath.Join(source, "Projects/Unicode space")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(source, "README.md")
	if err := os.WriteFile(readme, []byte("# Mock Second Brain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "λ-note.txt"), []byte("before-open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(source, "large.bin")
	if err := os.WriteFile(large, bytes.Repeat([]byte{'x'}, lifecycleLargeSize), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(readme, filepath.Join(source, "README-hardlink.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(source, "README-link.md")); err != nil {
		t.Fatal(err)
	}
}

func reserveLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func loopbackEndpoint(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }
func shellQuote(value string) string   { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func mustRunLifecycle(t *testing.T, ctx context.Context, environment []string, executable string, argv ...string) []byte {
	t.Helper()
	output, err := runLifecycleCommand(ctx, environment, executable, argv...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(argv, " "), err, output)
	}
	return output
}

func runLifecycleCommand(ctx context.Context, environment []string, executable string, argv ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, argv...)
	command.Env = append(os.Environ(), environment...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.Bytes(), err
}

func listDevPodWorkspaces(ctx context.Context) ([]string, error) {
	output, err := runLifecycleCommand(ctx, nil, "devpod", "list", "--context", "default", "--output", "json", "--skip-pro")
	if err != nil {
		return nil, fmt.Errorf("list DevPod: %w: %s", err, output)
	}
	var records []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &records); err != nil {
		return nil, fmt.Errorf("decode DevPod list: %w", err)
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids, nil
}

func assertNoDevPodWorkspaces(t *testing.T, ctx context.Context) {
	t.Helper()
	workspaces, err := listDevPodWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 0 {
		t.Fatalf("DevPod workspaces remain: %v", workspaces)
	}
}

func assertLoopbackPortClosed(t *testing.T, port int) {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", loopbackEndpoint(port), time.Second)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("listener remains on %s", loopbackEndpoint(port))
	}
}
