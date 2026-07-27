package capsule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/remoteworker"
)

func TestRenderBootstrapExecutesHelperBeforeEveryLifecycleForm(t *testing.T) {
	for name, lifecycle := range map[string]json.RawMessage{
		"string": json.RawMessage(`"printf 'user\\n' >> \"$TRACE\""`),
		"argv":   json.RawMessage(`["/bin/sh","-c","printf 'user\\n' >> \"$TRACE\""]`),
		"named":  json.RawMessage(`{"b":["/bin/sh","-c","printf 'user-b\\n' >> \"$TRACE\""],"a":"printf 'user-a\\n' >> \"$TRACE\""}`),
	} {
		t.Run(name, func(t *testing.T) {
			fixture := bootstrapFixture(t, lifecycle)
			result, err := renderBootstrap(fixture.request, fixture.openHelper)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(result.Root)
			if err != nil {
				t.Fatal(err)
			}
			if got := entryNames(entries); strings.Join(got, ",") != ".camp-bootstrap,camp-hauler-kit.tar.zst" {
				t.Fatalf("bootstrap root entries = %v", got)
			}
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("TRACE", trace)
			document := readBootstrapDocument(t, result.DevcontainerPath)
			assertLifecycleForm(t, name, document["initializeCommand"])
			runLifecycle(t, result.Root, document["initializeCommand"])
			runLifecycle(t, result.Root, document["onCreateCommand"])
			runLifecycle(t, result.Root, document["postStartCommand"])
			body, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			assertLifecycleTrace(t, name, string(body))
		})
	}
}

func TestRenderBootstrapHelperFailurePreventsEveryUserHook(t *testing.T) {
	for name, lifecycle := range map[string]json.RawMessage{
		"string": json.RawMessage(`"printf 'user\\n' >> \"$TRACE\""`),
		"argv":   json.RawMessage(`["/bin/sh","-c","printf 'user\\n' >> \"$TRACE\""]`),
		"named":  json.RawMessage(`{"a":"printf 'user-a\\n' >> \"$TRACE\"","b":["/bin/sh","-c","printf 'user-b\\n' >> \"$TRACE\""]}`),
	} {
		t.Run(name, func(t *testing.T) {
			fixture := bootstrapFixture(t, lifecycle)
			result, err := renderBootstrap(fixture.request, fixture.openHelper)
			if err != nil {
				t.Fatal(err)
			}
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("TRACE", trace)
			t.Setenv("HELPER_FAIL", "1")
			document := readBootstrapDocument(t, result.DevcontainerPath)
			assertLifecycleForm(t, name, document["initializeCommand"])
			if err := executeLifecycle(result.Root, document["initializeCommand"]); err == nil {
				t.Fatal("lifecycle succeeded after helper failure")
			}
			if body, err := os.ReadFile(trace); err == nil && strings.Contains(string(body), "user") {
				t.Fatalf("user hook ran after helper failure: %q", body)
			}
		})
	}
}

func TestNamedLifecycleCrashAndStaleGateFilesCannotAuthorizeLaterInvocation(t *testing.T) {
	lifecycle := json.RawMessage(`{"a":"printf 'user-a-%s\\n' \"$INVOCATION\" >> \"$TRACE\"","b":["/bin/sh","-c","printf 'user-b-%s\\n' \"$INVOCATION\" >> \"$TRACE\""]}`)
	fixture := bootstrapFixture(t, lifecycle)
	result, err := renderBootstrap(fixture.request, fixture.openHelper)
	if err != nil {
		t.Fatal(err)
	}
	document := readBootstrapDocument(t, result.DevcontainerPath)
	trace := filepath.Join(t.TempDir(), "trace")

	if err := executeLifecycleWithEnv(result.Root, document["postStartCommand"], map[string]string{
		"TRACE": trace, "INVOCATION": "crashed", "HELPER_FAIL": "1",
	}); err == nil {
		t.Fatal("crashed owner invocation succeeded")
	}
	gateDirectory := filepath.Join(result.Root, ".camp-bootstrap")
	for name, body := range map[string]string{
		"services-request.gate.current":                                    strings.Repeat("1", 32) + "\n",
		"services-request.gate." + strings.Repeat("1", 32) + ".intent":     "pending\n",
		"services-request.gate." + strings.Repeat("1", 32) + ".wait.stale": "waiting\n",
	} {
		if err := os.WriteFile(filepath.Join(gateDirectory, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := executeLifecycleWithEnv(result.Root, document["postStartCommand"], map[string]string{
		"TRACE": trace, "INVOCATION": "live",
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Contains(got, "user-a-crashed") || strings.Contains(got, "user-b-crashed") {
		t.Fatalf("crashed invocation ran user commands: %q", got)
	}
	for _, want := range []string{"helper-startServices-live", "user-a-live", "user-b-live"} {
		if strings.Count(got, want+"\n") != 1 {
			t.Fatalf("%s count differs in %q", want, got)
		}
	}
}

func TestNamedLifecycleOverlappingInvocationsKeepUserCommandsWithTheirOwner(t *testing.T) {
	lifecycle := json.RawMessage(`{"a":"printf 'user-a-%s\\n' \"$INVOCATION\" >> \"$TRACE\"","b":["/bin/sh","-c","printf 'user-b-%s\\n' \"$INVOCATION\" >> \"$TRACE\""]}`)
	fixture := bootstrapFixture(t, lifecycle)
	result, err := renderBootstrap(fixture.request, fixture.openHelper)
	if err != nil {
		t.Fatal(err)
	}
	document := readBootstrapDocument(t, result.DevcontainerPath)
	trace := filepath.Join(t.TempDir(), "trace")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, invocation := range []string{"first", "second"} {
		go func() {
			<-start
			results <- executeLifecycleWithEnv(result.Root, document["postStartCommand"], map[string]string{
				"TRACE": trace, "INVOCATION": invocation,
			})
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, invocation := range []string{"first", "second"} {
		for _, event := range []string{"helper-startServices-", "user-a-", "user-b-"} {
			if strings.Count(got, event+invocation+"\n") != 1 {
				t.Fatalf("%s%s count differs in %q", event, invocation, got)
			}
		}
	}
}

func TestNamedLifecycleRunsCommandsConcurrentlyAndAggregatesFailure(t *testing.T) {
	lifecycle := json.RawMessage(`{` +
		`"a":"touch \"$SYNC/a\"; while [ ! -e \"$SYNC/b\" ]; do sleep 0.01; done; printf 'a\\n' >> \"$TRACE\"; exit 7",` +
		`"b":["/bin/sh","-c","touch \"$SYNC/b\"; while [ ! -e \"$SYNC/a\" ]; do sleep 0.01; done; printf 'b\\n' >> \"$TRACE\""]}`)
	fixture := bootstrapFixture(t, lifecycle)
	result, err := renderBootstrap(fixture.request, fixture.openHelper)
	if err != nil {
		t.Fatal(err)
	}
	document := readBootstrapDocument(t, result.DevcontainerPath)
	root := t.TempDir()
	trace := filepath.Join(root, "trace")
	if err := executeLifecycleWithEnv(result.Root, document["postStartCommand"], map[string]string{
		"TRACE": trace, "SYNC": root,
	}); err == nil {
		t.Fatal("named lifecycle ignored a user command failure")
	}
	body, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Count(got, "a\n") != 1 || strings.Count(got, "b\n") != 1 {
		t.Fatalf("named lifecycle did not finish every concurrent command: %q", got)
	}
}

func TestRenderBootstrapFailsClosedWithoutChangingOriginal(t *testing.T) {
	for name, config := range map[string]string{
		"build only": `{"build":{"dockerfile":"Dockerfile"}}`,
		"null hook":  `{"image":"example/original@sha256:` + strings.Repeat("a", 64) + `","onCreateCommand":null}`,
		"mixed argv": `{"image":"example/original@sha256:` + strings.Repeat("a", 64) + `","onCreateCommand":["echo",1]}`,
		"duplicate named command": `{"image":"example/original@sha256:` + strings.Repeat("a", 64) +
			`","onCreateCommand":{"same":"echo one","same":"echo two"}}`,
		"duplicate lifecycle field": `{"image":"example/original@sha256:` + strings.Repeat("a", 64) +
			`","onCreateCommand":"echo one","onCreateCommand":"echo two"}`,
		"recursive duplicate field": `{"image":"example/original@sha256:` + strings.Repeat("a", 64) +
			`","customizations":{"vscode":{"setting":1,"setting":2}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := bootstrapFixtureWithConfig(t, config)
			before, err := os.ReadFile(fixture.request.DevcontainerPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := renderBootstrap(fixture.request, fixture.openHelper); err == nil {
				t.Fatal("renderBootstrap() error = nil")
			}
			after, err := os.ReadFile(fixture.request.DevcontainerPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("renderer changed original devcontainer document")
			}
			if _, err := os.Stat(fixture.request.Root); !os.IsNotExist(err) {
				t.Fatalf("bootstrap root exists after failure: %v", err)
			}
		})
	}
}

func TestRenderBootstrapPublishesKitFromVerifiedDescriptor(t *testing.T) {
	for name, mutate := range map[string]func(string) error{
		"replace path": func(path string) error {
			replacement := filepath.Join(filepath.Dir(path), "replacement.tar.zst")
			if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
				return err
			}
			return os.Rename(replacement, path)
		},
		"mutate opened inode": func(path string) error {
			return os.WriteFile(path, []byte("replacement"), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := bootstrapFixture(t, json.RawMessage(`"true"`))
			original, err := os.ReadFile(fixture.request.KitArchivePath)
			if err != nil {
				t.Fatal(err)
			}
			openHelper := fixture.openHelper
			fixture.openHelper = func() (*os.File, error) {
				if err := mutate(fixture.request.KitArchivePath); err != nil {
					return nil, err
				}
				return openHelper()
			}
			result, renderErr := renderBootstrap(fixture.request, fixture.openHelper)
			if renderErr != nil {
				if _, err := os.Stat(fixture.request.Root); !os.IsNotExist(err) {
					t.Fatalf("failed render published root: %v", err)
				}
				return
			}
			published, err := os.ReadFile(filepath.Join(result.Root, "camp-hauler-kit.tar.zst"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(published, original) {
				t.Fatalf("published unverified kit bytes %q", published)
			}
		})
	}
}

func TestRenderBootstrapRequiresOneRequestScope(t *testing.T) {
	for name, mutate := range map[string]func(*remoteworker.Request){
		"schema":         func(request *remoteworker.Request) { request.SchemaVersion++ },
		"session":        func(request *remoteworker.Request) { request.SessionID = "other" },
		"workspace root": func(request *remoteworker.Request) { request.WorkspaceRoot = "/workspaces/other" },
		"runtime root":   func(request *remoteworker.Request) { request.RuntimeRoot = "/var/lib/camp/other" },
		"manifest path":  func(request *remoteworker.Request) { request.ManifestPath = "/var/lib/camp/other/manifest.json" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := bootstrapFixture(t, json.RawMessage(`"true"`))
			mutate(&fixture.request.HydrateRequest)
			if _, err := renderBootstrap(fixture.request, fixture.openHelper); err == nil {
				t.Fatal("renderBootstrap() error = nil")
			}
			if _, err := os.Stat(fixture.request.Root); !os.IsNotExist(err) {
				t.Fatalf("bootstrap root exists after failure: %v", err)
			}
		})
	}
}

func TestRenderBootstrapRollsBackPublishedInodeWhenParentSyncFails(t *testing.T) {
	fixture := bootstrapFixture(t, json.RawMessage(`"true"`))
	parent := filepath.Dir(fixture.request.Root)
	previous := bootstrapDirectorySync
	t.Cleanup(func() { bootstrapDirectorySync = previous })
	injected := false
	bootstrapDirectorySync = func(directory *os.File) error {
		if directory.Name() == parent && !injected {
			if _, err := os.Lstat(fixture.request.Root); err == nil {
				injected = true
				return errors.New("injected parent sync failure")
			}
		}
		return directory.Sync()
	}
	if _, err := renderBootstrap(fixture.request, fixture.openHelper); err == nil {
		t.Fatal("renderBootstrap() error = nil")
	}
	if !injected {
		t.Fatal("parent sync failure was not injected after publication")
	}
	if _, err := os.Lstat(fixture.request.Root); !os.IsNotExist(err) {
		t.Fatalf("published bootstrap survived failed parent sync: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(parent, ".camp-bootstrap-stage-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("rollback retained stages: %v", matches)
	}
}

func TestRenderBootstrapRollbackPreservesReplacementTarget(t *testing.T) {
	fixture := bootstrapFixture(t, json.RawMessage(`"true"`))
	parent := filepath.Dir(fixture.request.Root)
	displaced := filepath.Join(parent, "displaced-bootstrap")
	previous := bootstrapDirectorySync
	t.Cleanup(func() { bootstrapDirectorySync = previous })
	injected := false
	bootstrapDirectorySync = func(directory *os.File) error {
		if directory.Name() == parent && !injected {
			if _, err := os.Lstat(fixture.request.Root); err == nil {
				injected = true
				if err := os.Rename(fixture.request.Root, displaced); err != nil {
					return err
				}
				if err := os.Mkdir(fixture.request.Root, 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(fixture.request.Root, "replacement"), []byte("keep"), 0o600); err != nil {
					return err
				}
				return errors.New("injected parent sync failure after replacement")
			}
		}
		return directory.Sync()
	}
	if _, err := renderBootstrap(fixture.request, fixture.openHelper); err == nil {
		t.Fatal("renderBootstrap() error = nil")
	}
	if !injected {
		t.Fatal("replacement race was not injected")
	}
	body, err := os.ReadFile(filepath.Join(fixture.request.Root, "replacement"))
	if err != nil || string(body) != "keep" {
		t.Fatalf("replacement target was not preserved: body=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(displaced, ".camp-bootstrap", "devcontainer.json")); err != nil {
		t.Fatalf("displaced published inode was not preserved for recovery: %v", err)
	}
}

func TestRenderBootstrapRejectsSymlinkedSourceAncestor(t *testing.T) {
	fixture := bootstrapFixture(t, json.RawMessage(`"true"`))
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "source")
	if err := os.Symlink(filepath.Dir(fixture.request.KitArchivePath), link); err != nil {
		t.Fatal(err)
	}
	fixture.request.KitArchivePath = filepath.Join(link, filepath.Base(fixture.request.KitArchivePath))
	if _, err := renderBootstrap(fixture.request, fixture.openHelper); err == nil {
		t.Fatal("renderBootstrap() error = nil")
	}
}

type bootstrapTestFixture struct {
	request    BootstrapRequest
	openHelper func() (*os.File, error)
}

func bootstrapFixture(t *testing.T, lifecycle json.RawMessage) bootstrapTestFixture {
	t.Helper()
	digest := strings.Repeat("a", 64)
	config := `{"name":"fixture","image":"example/original@sha256:` + digest + `",` +
		`"initializeCommand":` + string(lifecycle) + `,"onCreateCommand":` + string(lifecycle) +
		`,"postStartCommand":` + string(lifecycle) + `}`
	return bootstrapFixtureWithConfig(t, config)
}

func bootstrapFixtureWithConfig(t *testing.T, config string) bootstrapTestFixture {
	t.Helper()
	parent := t.TempDir()
	devcontainer := filepath.Join(parent, "devcontainer.json")
	helper := filepath.Join(parent, "camp")
	kit := filepath.Join(parent, "kit.tar.zst")
	manifest := filepath.Join(parent, "manifest.json")
	if err := os.WriteFile(devcontainer, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	helperBody := []byte(`#!/bin/sh
set -eu
case "$1" in
__remote-worker)
	op=$(sed -n 's/.*"operation":"\([^"]*\)".*/\1/p')
	[ "${HELPER_FAIL:-}" != 1 ] || exit 42
	sleep 0.1
	if [ -n "${INVOCATION:-}" ]; then
		printf 'helper-%s-%s\n' "$op" "$INVOCATION" >> "$TRACE"
	else
		printf 'helper-%s\n' "$op" >> "$TRACE"
	fi
	;;
*)
	exit 2
	;;
esac
`)
	if err := os.WriteFile(helper, helperBody, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kit, []byte("kit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := remoteworker.ExpectedIdentity{
		Architecture: "linux/" + runtime.GOARCH,
		Helper:       fileIdentity(t, "camp", helper),
		Kit:          fileIdentity(t, "camp-hauler-kit.tar.zst", kit),
		Manifest:     fileIdentity(t, "manifest.json", manifest),
		Image:        "example/final@sha256:" + strings.Repeat("b", 64),
	}
	requestFor := func(operation remoteworker.Operation) remoteworker.Request {
		return remoteworker.Request{
			SchemaVersion: remoteworker.ProtocolSchemaVersion,
			Operation:     operation,
			SessionID:     "session-1",
			WorkspaceRoot: "/workspaces/capsule",
			RuntimeRoot:   "/var/lib/camp/session-1",
			ManifestPath:  "/var/lib/camp/session-1/manifest.json",
			Expected:      expected,
		}
	}
	return bootstrapTestFixture{
		request: BootstrapRequest{
			Root:              filepath.Join(parent, "bootstrap"),
			DevcontainerPath:  devcontainer,
			KitArchivePath:    kit,
			ManifestPath:      manifest,
			OuterImage:        expected.Image,
			InitializeRequest: requestFor(remoteworker.OperationActivateImage),
			HydrateRequest:    requestFor(remoteworker.OperationHydrate),
			ServicesRequest:   requestFor(remoteworker.OperationStartServices),
		},
		openHelper: func() (*os.File, error) { return os.Open(helper) },
	}
}

func fileIdentity(t *testing.T, name, path string) remoteworker.FileIdentity {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return remoteworker.FileIdentity{Name: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
}

func readBootstrapDocument(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func runLifecycle(t *testing.T, root string, raw json.RawMessage) {
	t.Helper()
	if err := executeLifecycle(root, raw); err != nil {
		t.Fatal(err)
	}
}

func executeLifecycle(root string, raw json.RawMessage) error {
	return executeLifecycleWithEnv(root, raw, nil)
}

func executeLifecycleWithEnv(root string, raw json.RawMessage, environment map[string]string) error {
	var command any
	if err := json.Unmarshal(raw, &command); err != nil {
		return err
	}
	switch value := command.(type) {
	case map[string]any:
		processes := make([]*exec.Cmd, 0, len(value))
		for _, nested := range value {
			process, err := lifecycleProcess(root, nested)
			if err != nil {
				return err
			}
			process.Env = appendEnvironment(process.Env, environment)
			processes = append(processes, process)
		}
		for _, process := range processes {
			if err := process.Start(); err != nil {
				return err
			}
		}
		for _, process := range processes {
			if err := process.Wait(); err != nil {
				return err
			}
		}
	default:
		process, err := lifecycleProcess(root, command)
		if err != nil {
			return err
		}
		process.Env = appendEnvironment(process.Env, environment)
		if output, err := process.CombinedOutput(); err != nil {
			return fmt.Errorf("execute lifecycle: %w: %s", err, output)
		}
	}
	return nil
}

func appendEnvironment(base []string, additions map[string]string) []string {
	for key, value := range additions {
		base = append(base, key+"="+value)
	}
	return base
}

func lifecycleProcess(root string, command any) (*exec.Cmd, error) {
	var process *exec.Cmd
	switch value := command.(type) {
	case string:
		process = exec.Command("/bin/sh", "-c", value)
	case []any:
		argv := make([]string, len(value))
		for index := range value {
			argv[index] = value[index].(string)
		}
		process = exec.Command(argv[0], argv[1:]...)
	default:
		return nil, fmt.Errorf("unsupported generated command %#v", command)
	}
	process.Dir = root
	process.Env = os.Environ()
	return process, nil
}

func assertLifecycleForm(t *testing.T, form string, raw json.RawMessage) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	switch form {
	case "string":
		command, ok := value.(string)
		if !ok {
			t.Fatalf("string lifecycle became %T", value)
		}
		if strings.Contains(command, "/bin/sh -c") {
			t.Fatalf("string lifecycle gained a nested shell: %q", command)
		}
	case "argv":
		if _, ok := value.([]any); !ok {
			t.Fatalf("argv lifecycle became %T", value)
		}
	case "named":
		named, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("named lifecycle became %T", value)
		}
		if len(named) != 1 || named["00-camp-bootstrap"] == nil {
			t.Fatalf("named lifecycle entries = %#v", named)
		}
	}
}

func assertLifecycleTrace(t *testing.T, form, trace string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(trace, "\n"), "\n")
	operations := []string{"activateImage", "hydrate", "startServices"}
	index := 0
	for _, operation := range operations {
		helperCount := 1
		wantUsers := map[string]int{"user": 1}
		if form == "named" {
			wantUsers = map[string]int{"user-a": 1, "user-b": 1}
		}
		seenHelpers := 0
		seenUsers := 0
		for range helperCount + len(wantUsers) {
			if index >= len(lines) {
				t.Fatalf("%s lifecycle ended during %s: %q", form, operation, trace)
			}
			line := lines[index]
			switch {
			case line == "helper-"+operation:
				seenHelpers++
			case wantUsers[line] > 0:
				seenUsers++
				wantUsers[line]--
			default:
				t.Fatalf("%s lifecycle did not preserve %s commands: %q", form, operation, trace)
			}
			if seenHelpers == 0 && seenUsers > 0 {
				t.Fatalf("%s lifecycle ran a user before its helper for %s: %q", form, operation, trace)
			}
			index++
		}
		if seenHelpers != helperCount || seenUsers != len(wantUsers) {
			t.Fatalf("%s lifecycle counts differ for %s: %q", form, operation, trace)
		}
	}
	if index != len(lines) {
		t.Fatalf("%s lifecycle executed extra commands: %q", form, trace)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index := range entries {
		names[index] = entries[index].Name()
	}
	return names
}
