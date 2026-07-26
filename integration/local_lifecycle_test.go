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
	root := t.TempDir()
	source := filepath.Join(root, "mock Second Brain")
	backend := filepath.Join(root, "backend")
	controllerA := filepath.Join(root, "controller-a")
	controllerB := filepath.Join(root, "controller-b")
	devPod := newDevPodTestIsolation(root)
	bin := candidateBinary(t)
	writeLifecycleFixture(t, source)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	t.Cleanup(cancel)
	scenario := newLifecycleScenario(t, root, devPod, controllerA, controllerB)
	t.Cleanup(func() {
		scenario.Cleanup(t, bin)
	})

	envA := lifecycleEnvironment(controllerA, backend, devPod)
	scenario.RegisterController(controllerA, envA)
	t.Log("bootstrap the Docker provider inside the private DevPod context")
	mustBootstrapDevPodDockerProvider(t, ctx, devPod)
	scenario.CreateUnrelatedWorkspace(t, ctx)
	t.Log("initialize adopted fixture")
	mustRunLifecycle(t, ctx, envA, bin, "--json", "init", source, "--name", "local-lifecycle")
	t.Log("open adopted fixture through real DevPod")
	recovered := decodeOpenResult(t, mustRunLifecycleAt(t, ctx, envA, source, bin, "--json", "open"))
	workspaceA := recovered.WorkspaceID
	scenario.TrackController(t, controllerA)
	if workspaceA == "" || recovered.SessionID == "" || recovered.Target != "." {
		t.Fatalf("open = %#v", recovered)
	}
	workspaceRootA := "/workspaces/" + workspaceA
	endpoints := scenario.Endpoints(t, controllerA, recovered.SessionID)
	t.Log("mutate files and publish a named image through CAMP_REGISTRY")
	mutate := fmt.Sprintf("set -eu; cd %s; printf 'after-open\\n' >> 'Projects/Unicode space/λ-note.txt'; chmod 600 'Projects/Unicode space/λ-note.txt'; test \"$CAMP_REGISTRY\" = %s; test \"$CAMP_FILESERVER\" = %s; wget -qO- \"http://$CAMP_REGISTRY/v2/\" >/dev/null; wget -qO- \"http://$CAMP_FILESERVER/\" >/dev/null; engine=; attempts=0; while test -z \"$engine\"; do for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; attempts=$((attempts+1)); test $attempts -lt 60; sleep 1; done; \"$engine\" pull alpine:3.20; image_id=$(\"$engine\" create alpine:3.20); \"$engine\" commit \"$image_id\" %s/camp-acceptance:named; \"$engine\" rm \"$image_id\"; \"$engine\" push %s/camp-acceptance:named", shellQuote(workspaceRootA), shellQuote(endpoints.Registry), shellQuote(endpoints.Fileserver), endpoints.Registry, endpoints.Registry)
	mustRunDevPod(t, ctx, devPod, "ssh", workspaceA, "--command", mutate)

	t.Log("publish explicit sync generation")
	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "sync", "--camp", "local-lifecycle")); generation != 1 {
		t.Fatalf("sync generation = %d, want 1", generation)
	}
	edit := fmt.Sprintf("printf 'after-sync\\n' >> %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRootA, "Projects/Unicode space/λ-note.txt"))))
	mustRunDevPod(t, ctx, devPod, "ssh", workspaceA, "--command", edit)
	t.Log("publish final generation and close adopted workspace")
	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "close", "--camp", "local-lifecycle")); generation != 2 {
		t.Fatalf("close generation = %d, want 2", generation)
	}
	assertDevPodWorkspacesAbsent(t, ctx, devPod, workspaceA)
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("adopted source did not survive: %v", err)
	}

	envB := lifecycleEnvironment(controllerB, backend, devPod)
	scenario.RegisterController(controllerB, envB)
	t.Log("reopen from the file backend with a fresh XDG controller")
	reopened := decodeOpenResult(t, mustRunLifecycleAt(t, ctx, envB, source, bin, "--json", "reopen"))
	if reopened.Generation != 2 || reopened.WorkspaceID == "" || reopened.Materialization == "" {
		t.Fatalf("fresh-controller reopen = %#v", reopened)
	}
	scenario.TrackController(t, controllerB)
	workspaceRootB := "/workspaces/" + reopened.WorkspaceID
	t.Log("verify restored filesystem semantics and runnable named image")
	note := shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "Projects/Unicode space/λ-note.txt")))
	endpoints = scenario.Endpoints(t, controllerB, reopened.SessionID)
	verify := fmt.Sprintf("set -eux; grep -q before-open %s; grep -q after-open %s; grep -q after-sync %s; stat -c %%a %s | grep -qx 600; stat -c %%s %s | grep -qx %d; readlink %s | grep -qx README.md; find %s -xdev -samefile %s | grep -q README-hardlink.md; engine=; for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; test -n \"$engine\"; \"$engine\" image inspect %s/camp-acceptance:named >/dev/null; \"$engine\" run --rm %s/camp-acceptance:named true", note, note, note, note, shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "large.bin"))), lifecycleLargeSize, shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README-link.md"))), shellQuote(workspaceRootB), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README.md"))), endpoints.Registry, endpoints.Registry)
	mustRunDevPod(t, ctx, devPod, "ssh", reopened.WorkspaceID, "--command", verify)
	preserved := fmt.Sprintf("set -eu; stat -c %%a %s | grep -qx 755; stat -c %%a %s | grep -qx 600; grep -qx 'user-owned agent state' %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "bin/camp-fixture"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, ".claude/fixture.md"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, ".claude/fixture.md"))))
	mustRunDevPod(t, ctx, devPod, "ssh", reopened.WorkspaceID, "--command", preserved)
	t.Log("close fresh controller and verify teardown")
	mustRunLifecycle(t, ctx, envB, bin, "--json", "close", "--camp", "local-lifecycle")
	assertDevPodWorkspacesAbsent(t, ctx, devPod, workspaceA, reopened.WorkspaceID)
	if _, err := os.Stat(reopened.Materialization); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned materialization still exists: %v", err)
	}
	scenario.AssertEndpointsClosed(t)
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

func lifecycleEnvironment(controller, backend string, devPod devPodTestIsolation) []string {
	env := []string{"XDG_CONFIG_HOME=" + filepath.Join(controller, "config"), "XDG_DATA_HOME=" + filepath.Join(controller, "data"), "XDG_STATE_HOME=" + filepath.Join(controller, "state"), "XDG_CACHE_HOME=" + filepath.Join(controller, "cache"), "CAMP_BACKEND=file://" + backend, "CAMP_DEVPOD_PROVIDER=docker"}
	env = append(env, devPod.Environment()...)
	return env
}

func writeLifecycleFixture(t *testing.T, source string) {
	t.Helper()
	nested := filepath.Join(source, "Projects/Unicode space")
	scripts := filepath.Join(source, "bin")
	claude := filepath.Join(source, ".claude")
	for _, directory := range []string{nested, scripts, claude} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(scripts, "camp-fixture"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(source, "README.md")
	if err := os.WriteFile(readme, []byte("# Mock Second Brain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "λ-note.txt"), []byte("before-open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "fixture.md"), []byte("user-owned agent state\n"), 0o600); err != nil {
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

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func mustRunLifecycle(t *testing.T, ctx context.Context, environment []string, executable string, argv ...string) []byte {
	t.Helper()
	output, err := runLifecycleCommandAt(ctx, environment, "", executable, argv...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(argv, " "), err, output)
	}
	return output
}

func mustRunLifecycleAt(t *testing.T, ctx context.Context, environment []string, directory, executable string, argv ...string) []byte {
	t.Helper()
	output, err := runLifecycleCommandAt(ctx, environment, directory, executable, argv...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(argv, " "), err, output)
	}
	return output
}

func runLifecycleCommand(ctx context.Context, environment []string, executable string, argv ...string) ([]byte, error) {
	return runLifecycleCommandAt(ctx, environment, "", executable, argv...)
}

func runLifecycleCommandAt(ctx context.Context, environment []string, directory, executable string, argv ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, argv...)
	command.Env = mergeCommandEnvironment(os.Environ(), environment)
	if directory != "" {
		command.Dir = directory
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.Bytes(), err
}

func runDevPodCommand(ctx context.Context, isolation devPodTestIsolation, command string, argv ...string) ([]byte, error) {
	return runLifecycleCommand(ctx, isolation.Environment(), "devpod", isolation.CommandArgs(command, argv...)...)
}

func bootstrapDevPodDockerProvider(ctx context.Context, isolation devPodTestIsolation) ([]byte, error) {
	return runLifecycleCommand(
		ctx,
		isolation.Environment(),
		"devpod",
		"provider",
		"add",
		"docker",
		"--context",
		isolation.context,
		"--use",
		"--silent",
	)
}

func mustBootstrapDevPodDockerProvider(t *testing.T, ctx context.Context, isolation devPodTestIsolation) {
	t.Helper()
	output, err := bootstrapDevPodDockerProvider(ctx, isolation)
	if err != nil {
		t.Fatalf("bootstrap private DevPod Docker provider: %v\n%s", err, output)
	}
}

func mustRunDevPod(t *testing.T, ctx context.Context, isolation devPodTestIsolation, command string, argv ...string) []byte {
	t.Helper()
	output, err := runDevPodCommand(ctx, isolation, command, argv...)
	if err != nil {
		t.Fatalf("devpod %s: %v\n%s", strings.Join(isolation.CommandArgs(command, argv...), " "), err, output)
	}
	return output
}

func listDevPodWorkspaces(ctx context.Context, isolation devPodTestIsolation) ([]string, error) {
	output, err := runDevPodCommand(ctx, isolation, "list", "--output", "json", "--skip-pro")
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

func assertDevPodWorkspacesAbsent(t *testing.T, ctx context.Context, isolation devPodTestIsolation, expectedAbsent ...string) {
	t.Helper()
	workspaces, err := listDevPodWorkspaces(ctx, isolation)
	if err != nil {
		t.Fatal(err)
	}
	present := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		present[workspace] = struct{}{}
	}
	for _, workspace := range expectedAbsent {
		if _, ok := present[workspace]; ok {
			t.Fatalf("test-owned DevPod workspace %q remains in %v", workspace, workspaces)
		}
	}
}

func assertEndpointClosed(t *testing.T, endpoint string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("listener remains on %s", endpoint)
	}
}
