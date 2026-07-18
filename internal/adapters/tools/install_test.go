package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInstallerInstallsAndReusesPinnedRawBinary(t *testing.T) {
	contents := []byte("pinned devpod fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(contents)
	}))
	defer server.Close()

	lock := testToolLock("devpod", server.URL, digest(contents))
	root := t.TempDir()
	installer, err := NewInstaller(lock, root,
		WithHTTPClient(server.Client()),
		WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}

	first, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Managed || first.Repository != "example/devpod" || first.Version != "v1.2.3" || first.AssetSHA256 != digest(contents) {
		t.Fatalf("unexpected resolution: %#v", first)
	}
	assertRegularExecutable(t, first.Path, contents)

	server.Close()
	second, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatalf("reuse verified install without network: %v", err)
	}
	if second.Path != first.Path || !second.Managed {
		t.Fatalf("reuse = %#v, want managed %q", second, first.Path)
	}
}

func TestInstallerAcceptsOnlyDigestMatchingPATHBinary(t *testing.T) {
	contents := []byte("authoritative devpod fixture")
	pathDirectory := t.TempDir()
	candidate := filepath.Join(pathDirectory, "devpod")
	if err := os.WriteFile(candidate, contents, 0o755); err != nil {
		t.Fatal(err)
	}

	installer, err := NewInstaller(testToolLock("devpod", "https://github.com/example/devpod", digest(contents)), t.TempDir(),
		WithLookPath(func(string) (string, error) { return candidate, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Managed || resolution.Path != candidate {
		t.Fatalf("resolution = %#v, want accepted PATH binary", resolution)
	}

	spoof := filepath.Join(pathDirectory, "spoof")
	if err := os.WriteFile(spoof, []byte("v1.2.3"), 0o755); err != nil {
		t.Fatal(err)
	}
	installer, err = NewInstaller(testToolLock("devpod", "https://github.com/example/devpod", digest(contents)), t.TempDir(),
		WithLookPath(func(string) (string, error) { return spoof, nil }),
		WithHTTPClient(roundTripClient(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network sentinel")
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "download request failed") {
		t.Fatalf("spoofed PATH binary error = %v", err)
	}
}

func TestPastaProbeClassifiesExternalCapabilityAndRequiresFunctionalSurface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pasta")
	if err := os.WriteFile(path, []byte("pasta fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := PastaProbe{
		LookPath: func(name string) (string, error) {
			if name != "pasta" {
				t.Fatalf("LookPath(%q)", name)
			}
			return path, nil
		},
		Run: func(_ context.Context, gotPath string, args ...string) ([]byte, error) {
			if gotPath != path || len(args) != 1 || args[0] != "--help" {
				t.Fatalf("Run(%q, %q)", gotPath, args)
			}
			return []byte("--config-net --map-guest-addr --tcp-ports --udp-ports"), nil
		},
	}
	capability, err := probe.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capability.Kind != ExternalHostCapability || capability.Path != path {
		t.Fatalf("capability = %#v", capability)
	}

	probe.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte("usage"), nil }
	if _, err := probe.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "required option") {
		t.Fatalf("missing functional surface error = %v", err)
	}
}

func TestInstallerRejectsChecksumMismatchAndOversizedDownload(t *testing.T) {
	contents := bytes.Repeat([]byte("x"), 64)
	server := newAssetServer(t, contents)
	defer server.Close()
	for _, tt := range []struct {
		name    string
		digest  string
		limit   int64
		wantErr string
	}{
		{name: "checksum", digest: strings.Repeat("0", 64), limit: 128, wantErr: "checksum mismatch"},
		{name: "bounded download", digest: digest(contents), limit: 32, wantErr: "size limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installer, err := NewInstaller(testToolLock("devpod", server.URL+"/devpod", tt.digest), t.TempDir(),
				WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
				WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
				WithDownloadLimits(tt.limit, 128),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestInstallerRejectsRedirectToUnapprovedHost(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "https://unapproved.invalid/download?credential=secret", http.StatusFound)
	}))
	defer server.Close()
	installer, err := NewInstaller(testToolLock("devpod", server.URL+"/devpod", strings.Repeat("0", 64)), t.TempDir(),
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "download request failed") || strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect error = %q", err)
	}
}

func TestInstallerAllowsCredentialBearingQueryOnApprovedRedirectWithoutLoggingIt(t *testing.T) {
	contents := []byte("redirected binary")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/devpod" {
			http.Redirect(writer, request, "/signed?token=redirect-secret", http.StatusFound)
			return
		}
		_, _ = writer.Write(contents)
	}))
	defer server.Close()
	installer, err := NewInstaller(testToolLock("devpod", server.URL+"/devpod", digest(contents)), t.TempDir(),
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatalf("approved signed redirect: %v", err)
	}
	assertRegularExecutable(t, resolution.Path, contents)
}

func TestInstallerAcceptsDigestMatchingArchiveBinaryFromPATH(t *testing.T) {
	binary := []byte("path hauler fixture")
	archive := tarGzipFixture(t, []tarFixture{{Name: "hauler", Mode: 0o755, Body: binary}})
	server := newAssetServer(t, archive)
	defer server.Close()
	candidate := filepath.Join(t.TempDir(), "hauler")
	if err := os.WriteFile(candidate, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	installer, err := NewInstaller(testToolLock("hauler", server.URL+"/download", digest(archive)), t.TempDir(),
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return candidate, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := installer.Ensure(context.Background(), "hauler", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Managed || resolution.Path != candidate || resolution.BinarySHA256 != digest(binary) {
		t.Fatalf("archive PATH resolution = %#v", resolution)
	}
}

func TestInstallerPrefersMatchingArchivePATHBinaryOverExistingManagedInstall(t *testing.T) {
	binary := []byte("preferred path hauler")
	archive := tarGzipFixture(t, []tarFixture{{Name: "hauler", Mode: 0o755, Body: binary}})
	server := newAssetServer(t, archive)
	root := t.TempDir()
	lock := testToolLock("hauler", server.URL+"/download", digest(archive))
	managedInstaller, err := NewInstaller(lock, root,
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedInstaller.Ensure(context.Background(), "hauler", "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	server.Close()
	candidate := filepath.Join(t.TempDir(), "hauler")
	if err := os.WriteFile(candidate, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	pathInstaller, err := NewInstaller(lock, root,
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return candidate, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := pathInstaller.Ensure(context.Background(), "hauler", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Managed || resolution.Path != candidate {
		t.Fatalf("resolution = %#v, want preferred PATH binary", resolution)
	}
}

func TestInstallerDoesNotTrustTamperedManagedBinary(t *testing.T) {
	contents := []byte("verified binary")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = writer.Write(contents)
	}))
	defer server.Close()
	installer, err := NewInstaller(testToolLock("devpod", server.URL+"/devpod", digest(contents)), t.TempDir(),
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.Path, []byte("tampered binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	assertRegularExecutable(t, second.Path, contents)
	if requests.Load() != 2 {
		t.Fatalf("download requests = %d, want repair download", requests.Load())
	}
}

func TestInstallerDoesNotTrustForgedRawBinaryIdentityRecord(t *testing.T) {
	contents := []byte("locked raw binary")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = writer.Write(contents)
	}))
	defer server.Close()
	installer, err := NewInstaller(testToolLock("devpod", server.URL+"/devpod", digest(contents)), t.TempDir(),
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	forged := []byte("forged raw binary")
	if err := os.WriteFile(first.Path, forged, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(filepath.Dir(first.Path), "identity.json")
	markerBody, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker installIdentity
	if err := json.Unmarshal(markerBody, &marker); err != nil {
		t.Fatal(err)
	}
	marker.BinarySHA256 = digest(forged)
	markerBody, err = json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, markerBody, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	assertRegularExecutable(t, second.Path, contents)
	if requests.Load() != 2 {
		t.Fatalf("download requests = %d, want authoritative repair", requests.Load())
	}
}

func TestInstallerBindsManagedArchiveBinaryToLockedSourceAsset(t *testing.T) {
	binary := []byte("locked archive binary")
	archive := tarGzipFixture(t, []tarFixture{{Name: "hauler", Mode: 0o755, Body: binary}})
	server := newAssetServer(t, archive)
	defer server.Close()
	installer, err := NewInstaller(testToolLock("hauler", server.URL+"/download", digest(archive)), t.TempDir(),
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := installer.Ensure(context.Background(), "hauler", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	forged := []byte("forged archive binary")
	if err := os.WriteFile(first.Path, forged, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(filepath.Dir(first.Path), "identity.json")
	markerBody, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker installIdentity
	if err := json.Unmarshal(markerBody, &marker); err != nil {
		t.Fatal(err)
	}
	marker.BinarySHA256 = digest(forged)
	markerBody, err = json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, markerBody, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := installer.Ensure(context.Background(), "hauler", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	assertRegularExecutable(t, second.Path, binary)
}

func TestInstallerRejectsSymlinkAndHardlinkPATHCandidates(t *testing.T) {
	contents := []byte("candidate")
	directory := t.TempDir()
	realPath := filepath.Join(directory, "real")
	if err := os.WriteFile(realPath, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "symlink")
	if err := os.Symlink(realPath, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(directory, "hardlink")
	if err := os.Link(realPath, hardlink); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{symlink, hardlink} {
		t.Run(filepath.Base(candidate), func(t *testing.T) {
			installer, err := NewInstaller(testToolLock("devpod", "https://github.com/example/devpod", digest(contents)), t.TempDir(),
				WithLookPath(func(string) (string, error) { return candidate, nil }),
				WithHTTPClient(roundTripClient(func(*http.Request) (*http.Response, error) { return nil, errors.New("network sentinel") })),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "download request failed") {
				t.Fatalf("unsafe PATH candidate error = %v", err)
			}
		})
	}
}

func TestInstallerValidatesAndInstallsReleaseShapedTarGzip(t *testing.T) {
	binary := []byte("pinned hauler fixture")
	archive := tarGzipFixture(t, []tarFixture{
		{Name: "LICENSE", Mode: 0o644, Body: []byte("license metadata")},
		{Name: "README.md", Mode: 0o644, Body: []byte("readme metadata")},
		{Name: "hauler", Mode: 0o755, Body: binary},
	})
	server := newAssetServer(t, archive)
	defer server.Close()
	installer, err := NewInstaller(testToolLock("hauler", server.URL+"/download", digest(archive)), t.TempDir(),
		WithHTTPClient(server.Client()),
		WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := installer.Ensure(context.Background(), "hauler", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	assertRegularExecutable(t, resolution.Path, binary)
	if resolution.BinarySHA256 != digest(binary) || resolution.AssetSHA256 != digest(archive) {
		t.Fatalf("resolution identity = %#v", resolution)
	}
}

func TestInstallerRejectsUnsafeTarGzipShapes(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarFixture
		limit   int64
	}{
		{name: "symlink", entries: []tarFixture{{Name: "hauler", Type: tar.TypeSymlink, Linkname: "/bin/true"}}},
		{name: "hardlink", entries: []tarFixture{{Name: "hauler", Type: tar.TypeLink, Linkname: "other"}}},
		{name: "traversal", entries: []tarFixture{{Name: "../hauler", Mode: 0o755, Body: []byte("x")}}},
		{name: "unexpected entry", entries: []tarFixture{{Name: "hauler", Mode: 0o755, Body: []byte("x")}, {Name: "extra", Mode: 0o644, Body: []byte("y")}}},
		{name: "duplicate executable", entries: []tarFixture{{Name: "hauler", Mode: 0o755, Body: []byte("x")}, {Name: "hauler", Mode: 0o755, Body: []byte("y")}}},
		{name: "duplicate metadata", entries: []tarFixture{{Name: "LICENSE", Mode: 0o644, Body: []byte("x")}, {Name: "LICENSE", Mode: 0o644, Body: []byte("y")}, {Name: "hauler", Mode: 0o755, Body: []byte("z")}}},
		{name: "executable metadata", entries: []tarFixture{{Name: "LICENSE", Mode: 0o755, Body: []byte("x")}, {Name: "hauler", Mode: 0o755, Body: []byte("z")}}},
		{name: "directory", entries: []tarFixture{{Name: "hauler", Type: tar.TypeDir, Mode: 0o755}}},
		{name: "decompression bomb", entries: []tarFixture{{Name: "hauler", Mode: 0o755, Body: bytes.Repeat([]byte("x"), 64)}}, limit: 32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := tarGzipFixture(t, tt.entries)
			server := newAssetServer(t, archive)
			defer server.Close()
			options := []InstallerOption{
				WithHTTPClient(server.Client()),
				WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
				WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
			}
			if tt.limit != 0 {
				options = append(options, WithDownloadLimits(int64(len(archive))+1, tt.limit))
			}
			installer, err := NewInstaller(testToolLock("hauler", server.URL+"/hauler.tar.gz", digest(archive)), t.TempDir(), options...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := installer.Ensure(context.Background(), "hauler", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "archive") {
				t.Fatalf("unsafe archive error = %v", err)
			}
		})
	}
}

func TestInstallerRejectsDownloadPolicyViolationsWithoutLeakingURL(t *testing.T) {
	contents := []byte("binary")
	tests := []struct {
		name string
		url  string
	}{
		{name: "plain HTTP", url: "http://example.invalid/devpod?token=super-secret"},
		{name: "credentials", url: "https://user:super-secret@github.com/devpod"},
		{name: "query", url: "https://github.com/devpod?token=super-secret"},
		{name: "unapproved host", url: "https://example.invalid/devpod#super-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installer, err := NewInstaller(testToolLock("devpod", tt.url, digest(contents)), t.TempDir(),
				WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = installer.Ensure(context.Background(), "devpod", "linux", "amd64")
			if err == nil || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), tt.url) {
				t.Fatalf("policy error = %q", err)
			}
		})
	}
}

func TestInstallerRecoversAfterEveryInterruptedInstallStage(t *testing.T) {
	contents := []byte("interruptible binary")
	stages := []InstallStage{StageDownload, StageVerify, StageChmod, StageFsync, StageRename}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				_, _ = writer.Write(contents)
			}))
			defer server.Close()
			root := t.TempDir()
			lock := testToolLock("devpod", server.URL+"/devpod", digest(contents))
			failing, err := NewInstaller(lock, root,
				WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
				WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
				WithInstallHook(stage, func() error { return errors.New("simulated process death") }),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := failing.Ensure(context.Background(), "devpod", "linux", "amd64"); err == nil {
				t.Fatal("interrupted install unexpectedly succeeded")
			}

			recovered, err := NewInstaller(lock, root,
				WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
				WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
			)
			if err != nil {
				t.Fatal(err)
			}
			resolution, err := recovered.Ensure(context.Background(), "devpod", "linux", "amd64")
			if err != nil {
				t.Fatal(err)
			}
			assertRegularExecutable(t, resolution.Path, contents)
			if stage == StageRename && requests.Load() != 1 {
				t.Fatalf("rename recovery downloaded %d times, want reuse of verified final", requests.Load())
			}
		})
	}
}

func TestInstallerRecoversAfterProcessDeathAtEveryInstallStage(t *testing.T) {
	contents := []byte("process-death binary")
	for _, stage := range []InstallStage{StageDownload, StageVerify, StageChmod, StageFsync, StageRename} {
		t.Run(string(stage), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				_, _ = writer.Write(contents)
			}))
			defer server.Close()
			root := t.TempDir()
			certificate := base64.StdEncoding.EncodeToString(server.Certificate().Raw)
			command := exec.Command(os.Args[0], "-test.run=^TestInstallerCrashHelper$", "-test.v")
			command.Env = append(os.Environ(),
				"CAMP_TEST_TOOL_CRASH_STAGE="+string(stage),
				"CAMP_TEST_TOOL_CRASH_ROOT="+root,
				"CAMP_TEST_TOOL_CRASH_URL="+server.URL+"/devpod",
				"CAMP_TEST_TOOL_CRASH_DIGEST="+digest(contents),
				"CAMP_TEST_TOOL_CRASH_CERT="+certificate,
			)
			output, err := command.CombinedOutput()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 91 {
				t.Fatalf("crash helper error = %v, output = %s", err, output)
			}

			installer, err := NewInstaller(testToolLock("devpod", server.URL+"/devpod", digest(contents)), root,
				WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
				WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
			)
			if err != nil {
				t.Fatal(err)
			}
			resolution, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
			if err != nil {
				t.Fatal(err)
			}
			assertRegularExecutable(t, resolution.Path, contents)
			wantRequests := int32(2)
			if stage == StageRename {
				wantRequests = 1
			}
			if requests.Load() != wantRequests {
				t.Fatalf("download requests = %d, want %d", requests.Load(), wantRequests)
			}
		})
	}
}

func TestInstallerCrashHelper(t *testing.T) {
	stage := InstallStage(os.Getenv("CAMP_TEST_TOOL_CRASH_STAGE"))
	if stage == "" {
		return
	}
	certificateDER, err := base64.StdEncoding.DecodeString(os.Getenv("CAMP_TEST_TOOL_CRASH_CERT"))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	assetURL := os.Getenv("CAMP_TEST_TOOL_CRASH_URL")
	parsedURL, err := url.Parse(assetURL)
	if err != nil {
		t.Fatal(err)
	}
	installer, err := NewInstaller(testToolLock("devpod", assetURL, os.Getenv("CAMP_TEST_TOOL_CRASH_DIGEST")), os.Getenv("CAMP_TEST_TOOL_CRASH_ROOT"),
		WithHTTPClient(client), WithAllowedHosts(parsedURL.Host),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		WithInstallHook(stage, func() error { os.Exit(91); return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash hook was not reached")
}

func TestInstallerSerializesConcurrentFirstUse(t *testing.T) {
	contents := []byte("concurrent binary")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = writer.Write(contents)
	}))
	defer server.Close()
	installer, err := NewInstaller(testToolLock("devpod", server.URL+"/devpod", digest(contents)), t.TempDir(),
		WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
		WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
	)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	paths := make(chan string, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			resolution, ensureErr := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
			if ensureErr != nil {
				errorsChannel <- ensureErr
				return
			}
			paths <- resolution.Path
		}()
	}
	group.Wait()
	close(paths)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
	var first string
	for path := range paths {
		if first == "" {
			first = path
		} else if path != first {
			t.Fatalf("concurrent paths differ: %q and %q", first, path)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("download requests = %d, want 1", requests.Load())
	}
}

func TestInstallerDestinationBindsRepositoryVersionArchitectureAndDigest(t *testing.T) {
	contents := []byte("shared asset bytes")
	server := newAssetServer(t, contents)
	defer server.Close()
	root := t.TempDir()
	install := func(lock Lock) Resolution {
		t.Helper()
		installer, err := NewInstaller(lock, root,
			WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
			WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		)
		if err != nil {
			t.Fatal(err)
		}
		resolution, err := installer.Ensure(context.Background(), "devpod", "linux", "amd64")
		if err != nil {
			t.Fatal(err)
		}
		return resolution
	}
	firstLock := testToolLock("devpod", server.URL+"/devpod", digest(contents))
	secondLock := testToolLock("devpod", server.URL+"/devpod", digest(contents))
	secondTool := secondLock.Tools["devpod"]
	secondTool.Repository = "other/devpod"
	secondTool.Version = "v2.0.0"
	secondLock.Tools["devpod"] = secondTool
	first := install(firstLock)
	second := install(secondLock)
	if first.Path == second.Path {
		t.Fatalf("distinct locked identities collided at %q", first.Path)
	}
	assertRegularExecutable(t, first.Path, contents)
	assertRegularExecutable(t, second.Path, contents)
}

func TestInstallerCoversLinuxAMD64AndARM64Assets(t *testing.T) {
	contents := []byte("multi-architecture binary")
	server := newAssetServer(t, contents)
	defer server.Close()
	lock := testToolLock("devpod", server.URL+"/devpod", digest(contents))
	tool := lock.Tools["devpod"]
	tool.Assets["linux"]["arm64"] = Asset{URL: server.URL + "/devpod-arm64", SHA256: digest(contents)}
	lock.Tools["devpod"] = tool
	root := t.TempDir()
	paths := make(map[string]string)
	for _, arch := range []string{"amd64", "arm64"} {
		installer, err := NewInstaller(lock, root,
			WithHTTPClient(server.Client()), WithAllowedHosts(strings.TrimPrefix(server.URL, "https://")),
			WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		)
		if err != nil {
			t.Fatal(err)
		}
		resolution, err := installer.Ensure(context.Background(), "devpod", "linux", arch)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Architecture != arch {
			t.Fatalf("resolution architecture = %q, want %q", resolution.Architecture, arch)
		}
		assertRegularExecutable(t, resolution.Path, contents)
		paths[arch] = resolution.Path
	}
	if paths["amd64"] == paths["arm64"] {
		t.Fatalf("architecture destinations collided at %q", paths["amd64"])
	}
}

func TestInstallerCleanInstallsRealPinnedTools(t *testing.T) {
	if os.Getenv("CAMP_TEST_REAL_TOOLS") != "1" {
		t.Skip("set CAMP_TEST_REAL_TOOLS=1 to download and execute pinned release assets")
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skipf("real pinned tools are not executable on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	file, err := os.Open("../../../tools.lock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock, err := ParseLock(file)
	if err != nil {
		t.Fatal(err)
	}
	installer, err := NewInstaller(lock, t.TempDir(), WithLookPath(func(string) (string, error) {
		return "", os.ErrNotExist
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"devpod", "hauler"} {
		t.Run(name, func(t *testing.T) {
			resolution, ensureErr := installer.Ensure(context.Background(), name, runtime.GOOS, runtime.GOARCH)
			if ensureErr != nil {
				t.Fatal(ensureErr)
			}
			if !resolution.Managed || resolution.Architecture != runtime.GOARCH {
				t.Fatalf("real clean install resolution = %#v", resolution)
			}
			output, commandErr := exec.Command(resolution.Path, "version").CombinedOutput()
			if commandErr != nil {
				t.Fatalf("execute real pinned %s: %v: %s", name, commandErr, output)
			}
			if !strings.Contains(string(output), resolution.Version) {
				t.Fatalf("real pinned %s version output %q does not contain %q", name, output, resolution.Version)
			}
		})
	}
}

type tarFixture struct {
	Name     string
	Mode     int64
	Type     byte
	Linkname string
	Body     []byte
}

func tarGzipFixture(t *testing.T, entries []tarFixture) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.Type
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.Name, Mode: entry.Mode, Typeflag: typeflag, Linkname: entry.Linkname, Size: int64(len(entry.Body))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.Body) != 0 {
			if _, err := tarWriter.Write(entry.Body); err != nil {
				t.Fatal(err)
			}
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

func newAssetServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(body)
	}))
}

func testToolLock(name, assetURL, assetDigest string) Lock {
	return Lock{
		SchemaVersion: 1,
		Tools: map[string]Tool{
			name: {
				Repository: "example/" + name,
				Version:    "v1.2.3",
				Commit:     strings.Repeat("a", 40),
				Assets: map[string]map[string]Asset{
					"linux": {"amd64": {URL: assetURL, SHA256: assetDigest}},
				},
			},
		},
		Fixtures: Fixtures{Room: Fixture{Repository: "example/room", Version: "v1", Commit: strings.Repeat("b", 40)}},
	}
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func assertRegularExecutable(t *testing.T, path string, want []byte) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want regular 0755", info.Mode())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func roundTripClient(function roundTripFunc) *http.Client {
	return &http.Client{Transport: function}
}
