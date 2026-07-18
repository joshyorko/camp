package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestServiceLogReaderReturnsBoundedTailFromAnchoredServiceRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "session-a", "registry.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewServiceLogReader(root, 8)
	if err != nil {
		t.Fatal(err)
	}

	chunk, err := reader.ReadTail(context.Background(), domain.ServiceUnitRecord{Name: "registry", LogPath: path}, 8)
	if err != nil {
		t.Fatalf("ReadTail() error = %v", err)
	}
	if string(chunk.Bytes) != "\nsecond\n" || !chunk.Truncated {
		t.Fatalf("ReadTail() = %#v", chunk)
	}
}

func TestServiceLogReaderRejectsOutsideAndSymlinkPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewServiceLogReader(root, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadTail(context.Background(), domain.ServiceUnitRecord{Name: "registry", LogPath: outside}, 8); !errors.Is(err, ErrLogOwnership) {
		t.Fatalf("ReadTail(outside) error = %v", err)
	}
	symlink := filepath.Join(root, "registry.log")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadTail(context.Background(), domain.ServiceUnitRecord{Name: "registry", LogPath: symlink}, 8); !errors.Is(err, ErrLogOwnership) {
		t.Fatalf("ReadTail(symlink) error = %v", err)
	}
}
