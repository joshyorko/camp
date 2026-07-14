package archive

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestTarZstdRoundTripIsDeterministicAndExcludesOnlyRootBuildRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{".camp/runtime", ".camp/buildish", "nested/.camp/build", "empty"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), []byte("hidden"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "nested", "file.txt"), filepath.Join(root, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/file.txt", filepath.Join(root, "symlink.txt")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".camp/build/excluded", ".camp/runtime/excluded", ".camp/buildish/included", "nested/.camp/build/included"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stableTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(filepath.Join(root, "nested", "file.txt"), stableTime, stableTime); err != nil {
		t.Fatal(err)
	}

	first := filepath.Join(t.TempDir(), "first.tar.zst")
	second := filepath.Join(t.TempDir(), "second.tar.zst")
	adapter := NewTarZstd()
	firstInfo, err := adapter.Create(ctx, root, first)
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	secondInfo, err := adapter.Create(ctx, root, second)
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	firstBytes, _ := os.ReadFile(first)
	secondBytes, _ := os.ReadFile(second)
	if firstInfo.SHA256 != secondInfo.SHA256 || string(firstBytes) != string(secondBytes) || firstInfo.Size != int64(len(firstBytes)) {
		t.Fatalf("archives are not deterministic: %#v %#v", firstInfo, secondInfo)
	}

	destination := filepath.Join(t.TempDir(), "extracted")
	if err := adapter.Extract(ctx, first, destination); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	for _, path := range []string{".hidden", ".camp/buildish/included", "nested/.camp/build/included", "symlink.txt", "hardlink.txt", "empty"} {
		if _, err := os.Lstat(filepath.Join(destination, path)); err != nil {
			t.Fatalf("included path %q missing: %v", path, err)
		}
	}
	for _, path := range []string{".camp/build", ".camp/runtime"} {
		if _, err := os.Lstat(filepath.Join(destination, path)); !os.IsNotExist(err) {
			t.Fatalf("excluded path %q exists: %v", path, err)
		}
	}
	left, _ := os.Stat(filepath.Join(destination, "nested", "file.txt"))
	right, _ := os.Stat(filepath.Join(destination, "hardlink.txt"))
	if !os.SameFile(left, right) {
		t.Fatal("hardlink identity was not preserved")
	}
	if target, _ := os.Readlink(filepath.Join(destination, "symlink.txt")); target != "nested/file.txt" {
		t.Fatalf("symlink target = %q", target)
	}
}

func TestExtractRejectsTraversalLinksDuplicatesAndSpecialFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers []tar.Header
	}{
		{"dotdot", []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"absolute", []tar.Header{{Name: "/absolute", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"backslash", []tar.Header{{Name: `a\evil`, Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"overlong", []tar.Header{{Name: strings.Repeat("a", 5000), Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"symlink escape", []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0o777}, {Name: "link/pwn", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"hardlink missing", []tar.Header{{Name: "hard", Typeflag: tar.TypeLink, Linkname: "missing", Mode: 0o644}}},
		{"duplicate", []tar.Header{{Name: "same", Typeflag: tar.TypeReg, Mode: 0o644}, {Name: "same", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"fifo", []tar.Header{{Name: "fifo", Typeflag: tar.TypeFifo, Mode: 0o644}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sandbox := t.TempDir()
			archivePath := filepath.Join(sandbox, "attack.tar.zst")
			writeArchive(t, archivePath, test.headers)
			outside := filepath.Join(sandbox, "outside")
			if err := os.WriteFile(outside, []byte("canary"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(sandbox, "destination")
			if err := NewTarZstd().Extract(context.Background(), archivePath, destination); !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("Extract() error = %v, want ErrUnsafeArchive", err)
			}
			if body, _ := os.ReadFile(outside); string(body) != "canary" {
				t.Fatalf("outside canary changed: %q", body)
			}
			if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatalf("destination committed after attack: %v", err)
			}
		})
	}
}

func TestCreateRejectsDeterministicSourceMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := NewTarZstd(WithAfterInventory(func() error {
		return os.WriteFile(path, []byte("after"), 0o644)
	}))
	_, err := adapter.Create(context.Background(), root, filepath.Join(t.TempDir(), "root.tar.zst"))
	if !errors.Is(err, ErrRootSnapshotStable) {
		t.Fatalf("Create() error = %v, want ErrRootSnapshotStable", err)
	}
}

func writeArchive(t *testing.T, path string, headers []tar.Header) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	for index := range headers {
		header := headers[index]
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			header.Size = int64(len("x"))
		}
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			_, _ = io.WriteString(writer, "x")
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
