package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/devpod"
)

func TestInstalledDevPodBootstrapSourceSurvivesRecreate(t *testing.T) {
	if os.Getenv("CAMP_TEST_DEVPOD_BOOTSTRAP") != "1" {
		t.Skip("set CAMP_TEST_DEVPOD_BOOTSTRAP=1 to run the real two-phase DevPod bootstrap contract")
	}
	if _, err := os.Stat("/usr/bin/docker"); err != nil {
		t.Skipf("docker is unavailable: %v", err)
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
	if err := os.WriteFile(filepath.Join(bootstrapRoot, ".camp-bootstrap", "devcontainer.json"), []byte(`{"image":"alpine:3.20"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	assertBootstrapFootprint(t, bootstrapRoot)
	t.Cleanup(func() {
		if err := removeRootOwnedBootstrap(root, bootstrapRoot); err != nil {
			t.Errorf("remove exact disposable bootstrap source: %v", err)
		}
	})

	isolation := newDevPodTestIsolation(root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
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

	client := devpod.NewClient("devpod", execRunner{environment: environmentMap(isolation.Environment())})
	if _, err := client.Up(ctx, devpod.UpOptions{
		WorkspacePath: capsuleRoot, BootstrapPath: bootstrapRoot, SourceMode: devpod.SourceModeBootstrap,
		WorkspaceID: workspaceID, Context: isolation.context, Provider: "docker", DevcontainerPath: ".camp-bootstrap/devcontainer.json",
	}); err != nil {
		t.Fatalf("first bootstrap up: %v", err)
	}
	created = true

	remoteRoot, err := client.ResolveWorkspaceFolderInContext(ctx, isolation.context, workspaceID)
	if err != nil {
		t.Fatalf("resolve bootstrapped remote workspace: %v", err)
	}
	hydrateRemoteWorkspace(t, ctx, client, isolation.context, workspaceID, remoteRoot, "nested")
	logLocalBootstrapOwnership(t, bootstrapRoot)
	result, err := client.Up(ctx, devpod.UpOptions{
		WorkspacePath: capsuleRoot, BootstrapPath: bootstrapRoot, SourceMode: devpod.SourceModeBootstrap,
		WorkspaceID: workspaceID, Context: isolation.context, Provider: "docker", Recreate: true, DevcontainerPath: ".devcontainer/devcontainer.json",
	})
	if err != nil {
		t.Fatalf("recreate hydrated nested devcontainer: %v\nstdout:\n%s\nstderr:\n%s", err, result.Stdout, result.Stderr)
	}
	assertRemoteHydration(t, ctx, client, isolation.context, workspaceID, remoteRoot, "nested")
	hydrateRemoteWorkspace(t, ctx, client, isolation.context, workspaceID, remoteRoot, "root")
	result, err = client.Up(ctx, devpod.UpOptions{
		WorkspacePath: capsuleRoot, BootstrapPath: bootstrapRoot, SourceMode: devpod.SourceModeBootstrap,
		WorkspaceID: workspaceID, Context: isolation.context, Provider: "docker", Recreate: true, DevcontainerPath: ".devcontainer.json",
	})
	if err != nil {
		t.Fatalf("recreate hydrated root devcontainer: %v\nstdout:\n%s\nstderr:\n%s", err, result.Stdout, result.Stderr)
	}
	assertRemoteHydration(t, ctx, client, isolation.context, workspaceID, remoteRoot, "root")
	assertRecordedWorkspaceSource(t, ctx, isolation, workspaceID, bootstrapRoot, capsuleRoot)
}

func hydrateRemoteWorkspace(t *testing.T, ctx context.Context, client *devpod.Client, devpodContext, workspaceID, remoteRoot, selectedConfig string) {
	t.Helper()
	script := `set -eu
root=$1
case "$2" in
nested)
	mkdir -p "$root/.devcontainer"
	printf '%s\n' 'FROM alpine:3.20' > "$root/.devcontainer/Dockerfile"
	printf '%s\n' '{"build":{"dockerfile":"Dockerfile","context":".."}}' > "$root/.devcontainer/devcontainer.json"
	;;
root)
	printf '%s\n' 'FROM alpine:3.20' > "$root/Dockerfile"
	printf '%s\n' '{"build":{"dockerfile":"Dockerfile","context":"."}}' > "$root/.devcontainer.json"
	;;
*)
	exit 64
	;;
esac
printf '%s\n' 'must survive recreate' > "$root/hydrated.txt"`
	if _, err := client.Execute(ctx, devpod.WorkspaceCommand{
		Context: devpodContext, WorkspaceID: workspaceID,
		Argv: []string{"sh", "-c", script, "camp-hydrate", remoteRoot, selectedConfig},
	}); err != nil {
		t.Fatalf("hydrate %s config through remote workspace: %v", selectedConfig, err)
	}
}

func assertRemoteHydration(t *testing.T, ctx context.Context, client *devpod.Client, devpodContext, workspaceID, remoteRoot, selectedConfig string) {
	t.Helper()
	script := `set -eu
root=$1
test "$(cat "$root/hydrated.txt")" = "must survive recreate"
case "$2" in
nested)
	test -f "$root/.devcontainer/devcontainer.json"
	test -f "$root/.devcontainer/Dockerfile"
	;;
root)
	test -f "$root/.devcontainer.json"
	test -f "$root/Dockerfile"
	;;
*)
	exit 64
	;;
esac`
	if _, err := client.Execute(ctx, devpod.WorkspaceCommand{
		Context: devpodContext, WorkspaceID: workspaceID,
		Argv: []string{"sh", "-c", script, "camp-verify", remoteRoot, selectedConfig},
	}); err != nil {
		t.Fatalf("%s remote hydration did not survive recreate: %v", selectedConfig, err)
	}
}

func logLocalBootstrapOwnership(t *testing.T, bootstrapRoot string) {
	t.Helper()
	info, err := os.Stat(bootstrapRoot)
	if err != nil {
		t.Logf("local bootstrap source stat after remote hydration: %v", err)
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Logf("local bootstrap source after remote hydration: mode=%s owner=unknown", info.Mode())
		return
	}
	t.Logf("local bootstrap source after remote hydration: mode=%#o uid=%d gid=%d", info.Mode().Perm(), stat.Uid, stat.Gid)
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

func assertBootstrapFootprint(t *testing.T, root string) {
	t.Helper()
	var count, size int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count > 16 || size > 1<<20 {
		t.Fatalf("bootstrap footprint files=%d bytes=%d, want at most 16 files and 1 MiB", count, size)
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
