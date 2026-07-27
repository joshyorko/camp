package capsule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
			runLifecycle(t, result.Root, document["initializeCommand"])
			runLifecycle(t, result.Root, document["onCreateCommand"])
			runLifecycle(t, result.Root, document["postStartCommand"])
			body, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			want := "helper-activateImage\nuser\nhelper-hydrate\nuser\nhelper-startServices\nuser\n"
			if name == "named" {
				want = "helper-activateImage\nuser-a\nuser-b\nhelper-hydrate\nuser-a\nuser-b\nhelper-startServices\nuser-a\nuser-b\n"
			}
			got := string(body)
			if got != want {
				t.Fatalf("lifecycle trace = %q, want %q", got, want)
			}
		})
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
	helperBody := []byte("#!/bin/sh\nset -eu\nop=$(sed -n 's/.*\"operation\":\"\\([^\"]*\\)\".*/\\1/p')\nprintf 'helper-%s\\n' \"$op\" >> \"$TRACE\"\n")
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
	var commands map[string]json.RawMessage
	if err := json.Unmarshal(raw, &commands); err != nil {
		t.Fatal(err)
	}
	for _, key := range sortedKeys(commands) {
		var command any
		if err := json.Unmarshal(commands[key], &command); err != nil {
			t.Fatal(err)
		}
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
			t.Fatalf("unsupported generated command %#v", command)
		}
		process.Dir = root
		process.Env = os.Environ()
		if output, err := process.CombinedOutput(); err != nil {
			t.Fatalf("execute %s: %v: %s", key, err, output)
		}
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index := range entries {
		names[index] = entries[index].Name()
	}
	return names
}
