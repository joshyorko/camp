package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/config"
)

func TestRunProductionToolSetupInstallsLockedFixturesUnderXDGData(t *testing.T) {
	devpod := []byte("devpod fixture")
	hauler := setupTarGzip(t, map[string]setupArchiveEntry{
		"LICENSE":   {body: []byte("license"), mode: 0o644},
		"README.md": {body: []byte("readme"), mode: 0o644},
		"hauler":    {body: []byte("hauler fixture"), mode: 0o755},
	})
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.URL.Path {
		case "/devpod":
			_, _ = writer.Write(devpod)
		case "/hauler.tar.gz":
			_, _ = writer.Write(hauler)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	lock := fmt.Sprintf(`schemaVersion: 1
tools:
  devpod:
    repository: example/devpod
    version: v1
    commit: %s
    assets:
      linux:
        amd64: {url: %s/devpod, sha256: %s}
  hauler:
    repository: example/hauler
    version: v2
    commit: %s
    assets:
      linux:
        amd64: {url: %s/hauler.tar.gz, sha256: %s}
fixtures:
  room: {repository: example/room, version: v1, commit: %s}
`, strings.Repeat("1", 40), server.URL, setupDigest(devpod), strings.Repeat("2", 40), server.URL, setupDigest(hauler), strings.Repeat("3", 40))
	dataHome := filepath.Join(t.TempDir(), "data")
	var output bytes.Buffer

	err := runProductionToolSetup(context.Background(), ModeHuman, &output, []byte(lock), "/home/test", map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(t.TempDir(), "config"),
		"XDG_DATA_HOME":   dataHome,
		"XDG_CACHE_HOME":  filepath.Join(t.TempDir(), "cache"),
	}, "linux", "amd64", tooladapter.WithHTTPClient(server.Client()), tooladapter.WithAllowedHosts(host), tooladapter.WithLookPath(func(string) (string, error) {
		return "", fmt.Errorf("not on PATH")
	}))
	if err != nil {
		t.Fatalf("runProductionToolSetup: %v", err)
	}
	for _, name := range []string{"devpod", "hauler"} {
		if !strings.Contains(output.String(), filepath.Join(dataHome, "camp", "tools", name)) {
			t.Fatalf("output = %q, want managed %s path under XDG data", output.String(), name)
		}
	}
	output.Reset()
	if err := runProductionToolSetup(context.Background(), ModeHuman, &output, []byte(lock), "/home/test", map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(t.TempDir(), "config"),
		"XDG_DATA_HOME":   dataHome,
		"XDG_CACHE_HOME":  filepath.Join(t.TempDir(), "cache"),
	}, "linux", "amd64", tooladapter.WithHTTPClient(server.Client()), tooladapter.WithAllowedHosts(host), tooladapter.WithLookPath(func(string) (string, error) {
		return "", fmt.Errorf("not on PATH")
	})); err != nil {
		t.Fatalf("reuse runProductionToolSetup: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("download requests after reuse = %d, want 2", got)
	}
}

func TestRunManagedToolSetupReportsLockedIdentityAndPATH(t *testing.T) {
	ensurer := &recordingToolEnsurer{resolutions: map[string]tooladapter.Resolution{
		"devpod": {Path: "/camp/devpod/bin/devpod", Managed: true, Repository: "skevetter/devpod", Version: "v0.26.1", GOOS: "linux", Architecture: "amd64", AssetSHA256: strings.Repeat("a", 64), BinarySHA256: strings.Repeat("a", 64)},
		"hauler": {Path: "/camp/hauler/bin/hauler", Managed: true, Repository: "hauler-dev/hauler", Version: "v2.0.2", GOOS: "linux", Architecture: "amd64", AssetSHA256: strings.Repeat("b", 64), BinarySHA256: strings.Repeat("c", 64)},
	}}
	var output bytes.Buffer

	if err := runManagedToolSetup(context.Background(), ModeHuman, &output, ensurer, "linux", "amd64"); err != nil {
		t.Fatalf("runManagedToolSetup: %v", err)
	}
	if got := strings.Join(ensurer.calls, ","); got != "devpod:linux:amd64,hauler:linux:amd64" {
		t.Fatalf("Ensure calls = %q", got)
	}
	for _, want := range []string{
		"devpod v0.26.1 ready at /camp/devpod/bin/devpod",
		"hauler v2.0.2 ready at /camp/hauler/bin/hauler",
		"asset sha256 " + strings.Repeat("b", 64),
		"binary sha256 " + strings.Repeat("c", 64),
		`export PATH="/camp/devpod/bin:/camp/hauler/bin:$PATH"`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
}

func TestRunManagedToolSetupEmitsOnlyCompletedToolEvents(t *testing.T) {
	ensurer := &recordingToolEnsurer{resolutions: map[string]tooladapter.Resolution{
		"devpod": {Version: "v0.26.1"},
		"hauler": {Version: "v2.0.2"},
	}}
	var events []string
	if err := runManagedToolSetupWithEvents(context.Background(), ModeHuman, &bytes.Buffer{}, ensurer, "linux", "amd64", func(name string, resolution tooladapter.Resolution) error {
		events = append(events, name+" "+resolution.Version)
		if len(events) != len(ensurer.calls) {
			t.Fatalf("event emitted before Ensure completed: events=%v calls=%v", events, ensurer.calls)
		}
		return nil
	}); err != nil {
		t.Fatalf("runManagedToolSetupWithEvents: %v", err)
	}
	if got := strings.Join(events, ","); got != "devpod v0.26.1,hauler v2.0.2" {
		t.Fatalf("events = %q", got)
	}
}

func TestRunManagedToolSetupJSONUsesStableIdentityFields(t *testing.T) {
	ensurer := &recordingToolEnsurer{resolutions: map[string]tooladapter.Resolution{
		"devpod": {Path: "/usr/bin/devpod", Repository: "skevetter/devpod", Version: "v0.26.1", AssetSHA256: strings.Repeat("a", 64), BinarySHA256: strings.Repeat("a", 64)},
		"hauler": {Path: "/managed/hauler", Managed: true, Repository: "hauler-dev/hauler", Version: "v2.0.2", AssetSHA256: strings.Repeat("b", 64), BinarySHA256: strings.Repeat("c", 64)},
	}}
	var output bytes.Buffer

	if err := runManagedToolSetup(context.Background(), ModeJSON, &output, ensurer, "linux", "amd64"); err != nil {
		t.Fatalf("runManagedToolSetup: %v", err)
	}
	for _, want := range []string{`"kind":"setup"`, `"assetSha256":`, `"binarySha256":`, `"managed":true`, `"pathExport":"export PATH=\"/managed:$PATH\""`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), `"AssetSHA256"`) {
		t.Fatalf("output = %q, want stable lower-camel fields", output.String())
	}
}

type staticToolInspector struct {
	resolution tooladapter.Resolution
	err        error
}

func (i staticToolInspector) Inspect(context.Context, string, string, string) (tooladapter.Resolution, error) {
	return i.resolution, i.err
}

func TestDoctorManagedToolResolverMapsLockedIdentity(t *testing.T) {
	resolver := doctorManagedToolResolver{inspector: staticToolInspector{resolution: tooladapter.Resolution{
		Path: "/camp/devpod", Repository: "loft-sh/devpod", Version: "v0.26.1", BinarySHA256: strings.Repeat("a", 64), Managed: true,
	}}, goos: "linux", arch: "amd64"}
	identity, err := resolver.Inspect(context.Background(), "devpod")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Path != "/camp/devpod" || identity.Repository != "loft-sh/devpod" || !identity.Managed {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestResolveManagedToolPathsReturnsVerifiedExecutablesWithoutPATHMutation(t *testing.T) {
	ensurer := &recordingToolEnsurer{resolutions: map[string]tooladapter.Resolution{
		"devpod": {Path: "/managed/devpod", Managed: true},
		"hauler": {Path: "/managed/hauler", Managed: true},
	}}

	got, err := resolveManagedToolPaths(context.Background(), ensurer, "linux", "amd64")
	if err != nil {
		t.Fatalf("resolveManagedToolPaths: %v", err)
	}
	if got.devpod != "/managed/devpod" || got.hauler != "/managed/hauler" {
		t.Fatalf("managed paths = %+v", got)
	}
	if gotEnvironment := strings.Join(ensurer.calls, ","); gotEnvironment != "devpod:linux:amd64,hauler:linux:amd64" {
		t.Fatalf("Ensure calls = %q", gotEnvironment)
	}
}

func TestComposeProductionBootstrapsAndWiresManagedToolPaths(t *testing.T) {
	ensurer := &recordingToolEnsurer{resolutions: map[string]tooladapter.Resolution{
		"devpod": {Path: "/managed/devpod", Managed: true},
		"hauler": {Path: "/managed/hauler", Managed: true},
	}}
	dataRoot := filepath.Join(t.TempDir(), "data")

	composition, err := composeProductionWithSettings(context.Background(), productionSettings{
		paths:       config.XDGPaths{DataRoot: dataRoot},
		toolEnsurer: ensurer,
		goos:        "linux",
		arch:        "amd64",
	})
	if err != nil {
		t.Fatalf("composeProductionWithSettings: %v", err)
	}
	if composition.devpodExecutable != "/managed/devpod" || composition.haulerExecutable != "/managed/hauler" {
		t.Fatalf("composition tool paths = devpod %q hauler %q", composition.devpodExecutable, composition.haulerExecutable)
	}
}

type recordingToolEnsurer struct {
	resolutions map[string]tooladapter.Resolution
	calls       []string
}

func (r *recordingToolEnsurer) Ensure(_ context.Context, name, goos, arch string) (tooladapter.Resolution, error) {
	r.calls = append(r.calls, name+":"+goos+":"+arch)
	return r.resolutions[name], nil
}

type setupArchiveEntry struct {
	body []byte
	mode int64
}

func setupTarGzip(t *testing.T, entries map[string]setupArchiveEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range []string{"LICENSE", "README.md", "hauler"} {
		entry := entries[name]
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func setupDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
