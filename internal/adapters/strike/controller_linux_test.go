//go:build linux

package strike

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/app"
)

func TestControllerArchivesVerifiedChildrenAndPreservesTools(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "camp")
	for _, name := range []string{"sessions", "backend", "tools"} {
		if err := os.MkdirAll(filepath.Join(data, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	controller := NewController(func() time.Time { return time.Unix(100, 0).UTC() })
	archive, err := controller.Archive(context.Background(), app.StrikePlan{DataRoot: data, Targets: []string{filepath.Join(data, "sessions"), filepath.Join(data, "backend")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "tools")); err != nil {
		t.Fatal("tools were not preserved")
	}
	if _, err := os.Stat(filepath.Join(archive, "sessions")); err != nil {
		t.Fatal("sessions were not archived")
	}
	if _, err := os.Stat(filepath.Join(archive, "manifest.json")); err != nil {
		t.Fatal("archive manifest missing")
	}
}

func TestControllerRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "camp")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(data, "sessions")); err != nil {
		t.Fatal(err)
	}
	_, err := NewController(time.Now).Archive(context.Background(), app.StrikePlan{DataRoot: data, Targets: []string{filepath.Join(data, "sessions")}})
	if err == nil {
		t.Fatal("symlink target was accepted")
	}
}
