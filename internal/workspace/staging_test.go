package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMirrorStagingCreatesAndDiscardsAttemptOwnedDirectory(t *testing.T) {
	root := t.TempDir()
	staging := NewMirrorStaging(root)
	destination, err := staging.Fresh(context.Background(), "/adopted/source", "session-checkpoint-1-rsync")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(destination) != root {
		t.Fatalf("destination = %q", destination)
	}
	if info, err := os.Stat(destination); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("stat destination: info=%v err=%v", info, err)
	}
	if err := staging.Discard(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination survived discard: %v", err)
	}
}

func TestMirrorStagingRejectsUnsafeAttemptIdentity(t *testing.T) {
	if _, err := NewMirrorStaging(t.TempDir()).Fresh(context.Background(), "/source", "../escape"); err == nil {
		t.Fatal("unsafe attempt identity accepted")
	}
}
