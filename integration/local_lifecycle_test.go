package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
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

	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestLocalLifecycleVertical(t *testing.T) {
	if os.Getenv("CAMP_TEST_REAL_LIFECYCLE") != "1" {
		t.Skip("set CAMP_TEST_REAL_LIFECYCLE=1 to run the real DevPod/Hauler lifecycle")
	}
	for _, name := range []string{"go", "devpod", "hauler", "pasta", "docker", "script"} {
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
		if t.Failed() {
			logLifecycleFailureEvidence(t, controllerA, controllerB)
		}
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
	t.Log("reenter the same open session and attach through bounded PTYs")
	reentryContext, cancelReentry := context.WithTimeout(ctx, 2*time.Minute)
	reentryOutput, err := runBoundedPTYCommand(
		reentryContext,
		envA,
		source,
		bin,
		[]string{"open", "--camp", "local-lifecycle"},
		"exit\n",
	)
	cancelReentry()
	if err != nil {
		t.Fatalf("bounded repeated open: %v\n%s", err, reentryOutput)
	}
	attachContext, cancelAttach := context.WithTimeout(ctx, 2*time.Minute)
	attachOutput, err := runBoundedPTYCommand(
		attachContext,
		envA,
		source,
		bin,
		[]string{"attach", "Projects/Unicode space", "--session", recovered.SessionID},
		"pwd\nprintf 'attached\\n' > attach-proof.txt\nexit\n",
	)
	cancelAttach()
	if err != nil {
		t.Fatalf("bounded attach: %v\n%s", err, attachOutput)
	}
	if !strings.Contains(string(attachOutput), filepath.ToSlash(filepath.Join(workspaceRootA, "Projects/Unicode space"))) {
		t.Fatalf("attach did not land at the requested target:\n%s", attachOutput)
	}
	scenario.TrackController(t, controllerA)
	assertDevPodWorkspacesExactly(t, ctx, devPod, scenario.unrelatedID, workspaceA)
	if repeated := scenario.Endpoints(t, controllerA, recovered.SessionID); repeated != endpoints {
		t.Fatalf("reentry endpoints = %#v, want original %#v", repeated, endpoints)
	}

	t.Log("mutate files and build an explicitly tagged fixture image through CAMP_REGISTRY")
	mutate := fmt.Sprintf("set -eu; cd %s; grep -qx attached 'Projects/Unicode space/attach-proof.txt'; printf 'after-open\\n' >> 'Projects/Unicode space/λ-note.txt'; chmod 600 'Projects/Unicode space/λ-note.txt'; test \"$CAMP_REGISTRY\" = %s; test \"$CAMP_FILESERVER\" = %s; curl -fsS \"http://$CAMP_REGISTRY/v2/\" >/dev/null; curl -fsS \"http://$CAMP_FILESERVER/\" >/dev/null; engine=; attempts=0; while test -z \"$engine\"; do for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; attempts=$((attempts+1)); test $attempts -lt 60; sleep 1; done; reference=\"$CAMP_REGISTRY/camp/acceptance:named\"; \"$engine\" build --tag \"$reference\" image-fixture; \"$engine\" push \"$reference\"", shellQuote(workspaceRootA), shellQuote(endpoints.Registry), shellQuote(endpoints.Fileserver))
	mustRunDevPod(t, ctx, devPod, "ssh", workspaceA, "--command", mutate)
	namedImageDigest := registryPlatformManifestDigest(t, ctx, endpoints.Registry, "camp/acceptance", "named")
	evictedImageIDPathA := filepath.ToSlash(filepath.Join(workspaceRootA, "Projects/Unicode space/evicted-image-id.txt"))
	mustRunDevPod(t, ctx, devPod, "ssh", workspaceA, "--command", namedImageEvictionCommand(evictedImageIDPathA))

	t.Log("publish explicit sync generation")
	syncReceipt := decodeCheckpointReceipt(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "sync", "--camp", "local-lifecycle"))
	if !syncReceipt.Published || syncReceipt.Generation.Generation != 1 || syncReceipt.RecoveryCommand != "camp recover "+recovered.SessionID {
		t.Fatalf("sync receipt = %#v", syncReceipt)
	}
	syncPublication, err := readFilePublicationEvidence(ctx, backend, "local-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if syncPublication.Pointer.Generation != syncReceipt.Generation || syncPublication.Pointer.Parent != nil ||
		syncPublication.Generation.Metadata.Verified != (domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true}) {
		t.Fatalf("sync publication evidence = %#v, receipt = %#v", syncPublication, syncReceipt)
	}
	edit := fmt.Sprintf("printf 'after-sync\\n' >> %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRootA, "Projects/Unicode space/λ-note.txt"))))
	mustRunDevPod(t, ctx, devPod, "ssh", workspaceA, "--command", edit)
	t.Log("publish final generation and close adopted workspace")
	closeReceipt := decodeCloseReceipt(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "close", "--camp", "local-lifecycle"))
	if !closeReceipt.PublicationSucceeded || !closeReceipt.CleanupSucceeded ||
		closeReceipt.Generation.Generation != 2 || closeReceipt.RecoveryCommand != "camp recover "+recovered.SessionID {
		t.Fatalf("close receipt = %#v", closeReceipt)
	}
	closePublication, err := readFilePublicationEvidence(ctx, backend, "local-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if closePublication.Pointer.Generation != closeReceipt.Generation ||
		closePublication.Pointer.Parent == nil || *closePublication.Pointer.Parent != syncReceipt.Generation ||
		closePublication.PointerSHA256 == syncPublication.PointerSHA256 {
		t.Fatalf("close pointer did not advance from sync: sync=%#v close=%#v", syncPublication, closePublication)
	}
	retainedSync, err := readFileGenerationEvidence(ctx, backend, "local-lifecycle", syncReceipt.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if retainedSync.ArchiveSHA256 != syncPublication.Generation.ArchiveSHA256 ||
		retainedSync.ArchiveSize != syncPublication.Generation.ArchiveSize ||
		retainedSync.SidecarSHA256 != syncPublication.Generation.SidecarSHA256 {
		t.Fatalf("sync generation changed after close: before=%#v after=%#v", syncPublication.Generation, retainedSync)
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
	t.Log("verify restored filesystem semantics and digest-pinned runnable image")
	note := shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "Projects/Unicode space/λ-note.txt")))
	endpoints = scenario.Endpoints(t, controllerB, reopened.SessionID)
	largeSum := sha256.Sum256(bytes.Repeat([]byte{'x'}, lifecycleLargeSize))
	evictedImageIDPathB := filepath.ToSlash(filepath.Join(workspaceRootB, "Projects/Unicode space/evicted-image-id.txt"))
	verify := fmt.Sprintf("set -eux; test \"$(cat %s)\" = '# Mock Second Brain'; stat -c %%a %s | grep -qx 644; grep -q before-open %s; grep -q after-open %s; grep -q after-sync %s; stat -c %%a %s | grep -qx 600; grep -qx attached %s; stat -c %%s %s | grep -qx %d; stat -c %%a %s | grep -qx 640; printf '%%s  %%s\\n' %s %s | sha256sum -c -; readlink %s | grep -qx README.md; test \"$(stat -c %%d:%%i %s)\" = \"$(stat -c %%d:%%i %s)\"; engine=; for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; test -n \"$engine\"; %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README.md"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README.md"))), note, note, note, note, shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "Projects/Unicode space/attach-proof.txt"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "large.bin"))), lifecycleLargeSize, shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "large.bin"))), shellQuote(fmt.Sprintf("%x", largeSum)), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "large.bin"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README-link.md"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README.md"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "README-hardlink.md"))), namedImageReopenProofCommand(namedImageDigest, evictedImageIDPathB))
	mustRunDevPod(t, ctx, devPod, "ssh", reopened.WorkspaceID, "--command", verify)
	preserved := fmt.Sprintf("set -eu; stat -c %%a %s | grep -qx 755; stat -c %%a %s | grep -qx 600; grep -qx 'user-owned agent state' %s; stat -c %%a %s | grep -qx 755; %s | grep -qx camp-a2-oci-ok", shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "bin/camp-fixture"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, ".claude/fixture.md"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, ".claude/fixture.md"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "image-fixture/run.sh"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRootB, "image-fixture/run.sh"))))
	mustRunDevPod(t, ctx, devPod, "ssh", reopened.WorkspaceID, "--command", preserved)
	t.Log("close fresh controller and verify teardown")
	finalClose := decodeCloseReceipt(t, mustRunLifecycle(t, ctx, envB, bin, "--json", "close", "--camp", "local-lifecycle"))
	if !finalClose.PublicationSucceeded || !finalClose.CleanupSucceeded || finalClose.Generation.Generation != 3 ||
		finalClose.RecoveryCommand != "camp recover "+reopened.SessionID {
		t.Fatalf("fresh-controller close receipt = %#v", finalClose)
	}
	finalPublication, err := readFilePublicationEvidence(ctx, backend, "local-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if finalPublication.Pointer.Generation != finalClose.Generation ||
		finalPublication.Pointer.Parent == nil || *finalPublication.Pointer.Parent != closeReceipt.Generation {
		t.Fatalf("fresh-controller pointer did not advance: %#v", finalPublication)
	}
	assertDevPodWorkspacesAbsent(t, ctx, devPod, workspaceA, reopened.WorkspaceID)
	assertDevPodWorkspacesExactly(t, ctx, devPod, scenario.unrelatedID)
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

type lifecycleCheckpointReceipt struct {
	Published       bool
	Generation      domain.GenerationRef
	RecoveryCommand string
}

type lifecycleCloseReceipt struct {
	PublicationSucceeded bool
	CleanupSucceeded     bool
	Generation           domain.GenerationRef
	RecoveryCommand      string
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

func decodeCheckpointReceipt(t *testing.T, output []byte) lifecycleCheckpointReceipt {
	t.Helper()
	var envelope struct {
		Result struct {
			Published       bool                 `json:"published"`
			Generation      domain.GenerationRef `json:"generation"`
			RecoveryCommand string               `json:"recoveryCommand"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode checkpoint receipt: %v\n%s", err, output)
	}
	return lifecycleCheckpointReceipt{
		Published:       envelope.Result.Published,
		Generation:      envelope.Result.Generation,
		RecoveryCommand: envelope.Result.RecoveryCommand,
	}
}

func decodeCloseReceipt(t *testing.T, output []byte) lifecycleCloseReceipt {
	t.Helper()
	var envelope struct {
		Result struct {
			PublicationSucceeded bool                 `json:"publicationSucceeded"`
			CleanupSucceeded     bool                 `json:"cleanupSucceeded"`
			Generation           domain.GenerationRef `json:"generation"`
			RecoveryCommand      string               `json:"recoveryCommand"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode close receipt: %v\n%s", err, output)
	}
	return lifecycleCloseReceipt{
		PublicationSucceeded: envelope.Result.PublicationSucceeded,
		CleanupSucceeded:     envelope.Result.CleanupSucceeded,
		Generation:           envelope.Result.Generation,
		RecoveryCommand:      envelope.Result.RecoveryCommand,
	}
}

func lifecycleEnvironment(controller, backend string, devPod devPodTestIsolation) []string {
	env := []string{"XDG_CONFIG_HOME=" + filepath.Join(controller, "config"), "XDG_DATA_HOME=" + filepath.Join(controller, "data"), "XDG_STATE_HOME=" + filepath.Join(controller, "state"), "XDG_CACHE_HOME=" + filepath.Join(controller, "cache"), "XDG_RUNTIME_DIR=" + scenarioRuntimeDirectory(controller), "CAMP_BACKEND=file://" + backend, "CAMP_DEVPOD_PROVIDER=docker"}
	env = append(env, devPod.Environment()...)
	return env
}

func scenarioRuntimeDirectory(controller string) string { return filepath.Join(controller, "runtime") }

func writeLifecycleFixture(t *testing.T, source string) {
	t.Helper()
	nested := filepath.Join(source, "Projects/Unicode space")
	scripts := filepath.Join(source, "bin")
	claude := filepath.Join(source, ".claude")
	imageFixture := filepath.Join(source, "image-fixture")
	devcontainer := filepath.Join(source, ".devcontainer")
	for _, directory := range []string{nested, scripts, claude, imageFixture, devcontainer} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleImage := lockedLifecycleImage(t)
	lifecycleDevcontainer, err := json.Marshal(map[string]any{
		"build": map[string]any{
			"dockerfile": "Dockerfile",
			"args":       map[string]string{"BASE_IMAGE": lifecycleImage},
		},
		"privileged": true,
		"remoteUser": "podman",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devcontainer, "devcontainer.json"), lifecycleDevcontainer, 0o644); err != nil {
		t.Fatal(err)
	}
	lifecycleDockerfile := "ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\nRUN printf 'podman:100000:65536\\n' > /etc/subuid && printf 'podman:100000:65536\\n' > /etc/subgid\n"
	if err := os.WriteFile(filepath.Join(devcontainer, "Dockerfile"), []byte(lifecycleDockerfile), 0o644); err != nil {
		t.Fatal(err)
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
	if err := os.WriteFile(filepath.Join(imageFixture, "Dockerfile"), []byte("FROM alpine:3.20\nCOPY run.sh /usr/local/bin/camp-a2-oci\nENTRYPOINT [\"/usr/local/bin/camp-a2-oci\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageFixture, "run.sh"), []byte("#!/bin/sh\nprintf 'camp-a2-oci-ok\\n'\n"), 0o755); err != nil {
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

func TestWriteLifecycleFixturePinsLightweightContainerEngineDevcontainer(t *testing.T) {
	source := t.TempDir()
	writeLifecycleFixture(t, source)

	document, err := os.ReadFile(filepath.Join(source, ".devcontainer", "devcontainer.json"))
	if err != nil {
		t.Fatalf("read lifecycle devcontainer fixture: %v", err)
	}
	var config struct {
		Build struct {
			Dockerfile string            `json:"dockerfile"`
			Args       map[string]string `json:"args"`
		} `json:"build"`
		Privileged      bool   `json:"privileged"`
		RemoteUser      string `json:"remoteUser"`
		OverrideCommand *bool  `json:"overrideCommand"`
	}
	if err := json.Unmarshal(document, &config); err != nil {
		t.Fatalf("decode lifecycle devcontainer fixture: %v", err)
	}
	image := lockedLifecycleImage(t)
	if config.Build.Dockerfile != "Dockerfile" || config.Build.Args["BASE_IMAGE"] != image ||
		!config.Privileged || config.RemoteUser != "podman" || config.OverrideCommand != nil {
		t.Fatalf("lifecycle devcontainer fixture = %#v, want pinned privileged non-root container-engine image", config)
	}
	dockerfile, err := os.ReadFile(filepath.Join(source, ".devcontainer", "Dockerfile"))
	if err != nil {
		t.Fatalf("read lifecycle Dockerfile: %v", err)
	}
	if want := "ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\nRUN printf 'podman:100000:65536\\n' > /etc/subuid && printf 'podman:100000:65536\\n' > /etc/subgid\n"; string(dockerfile) != want {
		t.Fatalf("lifecycle Dockerfile = %q, want %q", dockerfile, want)
	}
}

func lockedLifecycleImage(t *testing.T) string {
	t.Helper()
	file, err := os.Open("../tools.lock.yaml")
	if err != nil {
		t.Fatalf("open lifecycle fixture lock: %v", err)
	}
	defer file.Close()
	lock, err := tooladapter.ParseLock(file)
	if err != nil {
		t.Fatalf("parse lifecycle fixture lock: %v", err)
	}
	return lock.Fixtures.Lifecycle.Image
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func TestRunLifecycleOpenRetriesOnlyExactAmbiguousOutcomeOnce(t *testing.T) {
	root := t.TempDir()
	countPath := filepath.Join(root, "count")
	executable := filepath.Join(root, "camp")
	writeTestExecutable(t, executable, `#!/bin/sh
count=0
if test -f "$CAMP_TEST_COUNT"; then count=$(cat "$CAMP_TEST_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" > "$CAMP_TEST_COUNT"
if test "$count" -eq 1; then
  printf '%s\n' 'object mutation outcome is ambiguous'
  exit 1
fi
printf '%s\n' '{"workspaceId":"camp-test"}'
`)

	output, err := runLifecycleOpenWithAmbiguousRetryAt(
		context.Background(),
		[]string{"CAMP_TEST_COUNT=" + countPath},
		"",
		executable,
		"--json",
		"open",
	)
	if err != nil || !bytes.Contains(output, []byte(`"workspaceId":"camp-test"`)) {
		t.Fatalf("ambiguous retry = %q, %v", output, err)
	}
	count, err := os.ReadFile(countPath)
	if err != nil || strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("attempt count = %q, %v; want 2", count, err)
	}

	arbitraryCountPath := filepath.Join(root, "arbitrary-count")
	arbitraryExecutable := filepath.Join(root, "camp-arbitrary")
	writeTestExecutable(t, arbitraryExecutable, `#!/bin/sh
count=0
if test -f "$CAMP_TEST_COUNT"; then count=$(cat "$CAMP_TEST_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" > "$CAMP_TEST_COUNT"
printf '%s\n' 'provider authentication failed'
exit 1
`)
	if _, err := runLifecycleOpenWithAmbiguousRetryAt(
		context.Background(),
		[]string{"CAMP_TEST_COUNT=" + arbitraryCountPath},
		"",
		arbitraryExecutable,
		"--json",
		"open",
	); err == nil {
		t.Fatal("arbitrary open failure error = nil")
	}
	arbitraryCount, err := os.ReadFile(arbitraryCountPath)
	if err != nil || strings.TrimSpace(string(arbitraryCount)) != "1" {
		t.Fatalf("arbitrary failure attempt count = %q, %v; want 1", arbitraryCount, err)
	}

	if !bytes.Contains([]byte(ports.ErrAmbiguous.Error()), []byte("object mutation outcome is ambiguous")) {
		t.Fatalf("stable ambiguous sentinel changed: %q", ports.ErrAmbiguous)
	}
}

func TestRunLifecycleOpenRetainsFirstAmbiguousOutputWhenRetryFails(t *testing.T) {
	root := t.TempDir()
	countPath := filepath.Join(root, "count")
	executable := filepath.Join(root, "camp")
	writeTestExecutable(t, executable, `#!/bin/sh
count=0
if test -f "$CAMP_TEST_COUNT"; then count=$(cat "$CAMP_TEST_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" > "$CAMP_TEST_COUNT"
if test "$count" -eq 1; then
  printf '%s\n' 'first DevPod diagnostic: object mutation outcome is ambiguous'
else
  printf '%s\n' 'second recovery failure'
fi
exit 1
`)
	output, err := runLifecycleOpenWithAmbiguousRetryAt(
		context.Background(),
		[]string{"CAMP_TEST_COUNT=" + countPath},
		"",
		executable,
		"--json",
		"open",
	)
	if err == nil || !bytes.Contains(output, []byte("first DevPod diagnostic")) || !bytes.Contains(output, []byte("second recovery failure")) {
		t.Fatalf("retry output = %q, error = %v; want both attempt diagnostics", output, err)
	}
}

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
	output, err := runLifecycleOpenWithAmbiguousRetryAt(ctx, environment, directory, executable, argv...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(argv, " "), err, output)
	}
	return output
}

func runLifecycleOpenWithAmbiguousRetryAt(ctx context.Context, environment []string, directory, executable string, argv ...string) ([]byte, error) {
	output, err := runLifecycleCommandAt(ctx, environment, directory, executable, argv...)
	if err == nil || !bytes.Contains(output, []byte(ports.ErrAmbiguous.Error())) {
		return output, err
	}
	retryOutput, retryErr := runLifecycleCommandAt(ctx, environment, directory, executable, argv...)
	if retryErr == nil {
		return retryOutput, nil
	}
	combined := append(append([]byte(nil), output...), []byte("\nretry output:\n")...)
	combined = append(combined, retryOutput...)
	return combined, retryErr
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

func assertDevPodWorkspacesExactly(t *testing.T, ctx context.Context, isolation devPodTestIsolation, expected ...string) {
	t.Helper()
	workspaces, err := listDevPodWorkspaces(ctx, isolation)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]int, len(expected))
	for _, id := range expected {
		want[id]++
	}
	got := make(map[string]int, len(workspaces))
	for _, id := range workspaces {
		got[id]++
	}
	if len(got) != len(want) {
		t.Fatalf("private DevPod workspaces = %v, want exactly %v", workspaces, expected)
	}
	for id, count := range want {
		if got[id] != count {
			t.Fatalf("private DevPod workspaces = %v, want exactly %v", workspaces, expected)
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

func logLifecycleFailureEvidence(t *testing.T, controllers ...string) {
	t.Helper()
	for _, controller := range controllers {
		for _, pattern := range []string{
			filepath.Join(controller, "runtime", "camp", "*", "*.log"),
			filepath.Join(controller, "data", "camp", "sessions", "*", "snapshot.json"),
		} {
			paths, err := filepath.Glob(pattern)
			if err != nil {
				t.Logf("glob lifecycle failure evidence %q: %v", pattern, err)
				continue
			}
			for _, path := range paths {
				body, err := os.ReadFile(path)
				if err != nil {
					t.Logf("read lifecycle failure evidence %q: %v", path, err)
					continue
				}
				if len(body) > 64<<10 {
					body = body[len(body)-(64<<10):]
				}
				t.Logf("lifecycle failure evidence %s:\n%s", path, body)
			}
		}
	}
}
