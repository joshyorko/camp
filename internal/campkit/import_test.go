package campkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVerifyStopsBeforeReadingWhenContextIsCanceled(t *testing.T) {
	archiveBody, _, _ := verifiedKitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Verify(ctx, bytes.NewReader(archiveBody), DefaultArchiveLimits(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify() error = %v, want context.Canceled", err)
	}
}

func TestVerifyStopsWhenContextIsCanceledDuringRead(t *testing.T) {
	archiveBody, _, _ := verifiedKitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &cancelAfterReadReader{reader: bytes.NewReader(archiveBody), cancel: cancel}
	_, err := Verify(ctx, reader, DefaultArchiveLimits(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify() error = %v, want context.Canceled", err)
	}
}

type cancelAfterReadReader struct {
	reader io.Reader
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterReadReader) Read(p []byte) (int, error) {
	if len(p) > 1024 {
		p = p[:1024]
	}
	r.reads++
	n, err := r.reader.Read(p)
	if r.reads == 2 {
		r.cancel()
	}
	return n, err
}

func TestImportFileVerifiesBeforePublishingNewDestination(t *testing.T) {
	archiveBody, manifest, _ := verifiedKitFixture(t)
	archive := filepath.Join(t.TempDir(), "kit.campkit")
	if err := os.WriteFile(archive, archiveBody, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "camp")
	result, err := ImportFile(context.Background(), archive, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Manifest.Generation, manifest.Generation) {
		t.Fatalf("imported generation = %#v, want %#v", result.Manifest.Generation, manifest.Generation)
	}
	body, err := os.ReadFile(filepath.Join(destination, "payloads", "generation", "archive.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("imported payload is empty")
	}
	if _, err := ImportFile(context.Background(), archive, destination, nil); err == nil {
		t.Fatal("second import overwrote an existing destination")
	}
}

func TestImportExtractsTheVerifiedSnapshot(t *testing.T) {
	archiveBody, manifest, _ := verifiedKitFixture(t)
	archive := filepath.Join(t.TempDir(), "kit.campkit")
	if err := os.WriteFile(archive, archiveBody, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "camp")
	oldHook := importBeforeExtract
	importBeforeExtract = func(input, _ string) error {
		return os.WriteFile(input, []byte("corrupt replacement"), 0o600)
	}
	t.Cleanup(func() { importBeforeExtract = oldHook })
	result, err := ImportFile(context.Background(), archive, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Manifest.Generation, manifest.Generation) {
		t.Fatalf("imported generation = %#v, want %#v", result.Manifest.Generation, manifest.Generation)
	}
}

func TestImportDoesNotRemoveAReplacedStagingDirectory(t *testing.T) {
	archiveBody, _, _ := verifiedKitFixture(t)
	archive := filepath.Join(t.TempDir(), "kit.campkit")
	if err := os.WriteFile(archive, archiveBody, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "camp")
	oldHook := importBeforeExtract
	importBeforeExtract = func(_, staging string) error {
		moved := staging + ".moved"
		if err := os.Rename(staging, moved); err != nil {
			return err
		}
		if err := os.Mkdir(staging, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(staging, "sentinel"), []byte("keep"), 0o600); err != nil {
			return err
		}
		return errors.New("injected extraction failure")
	}
	t.Cleanup(func() { importBeforeExtract = oldHook })
	if _, err := ImportFile(context.Background(), archive, destination, nil); err == nil {
		t.Fatal("ImportFile() succeeded, want injected failure")
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".campkit-import-") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(filepath.Dir(destination), entry.Name(), "sentinel"))
		if readErr == nil && string(body) == "keep" {
			return
		}
	}
	t.Fatal("replacement staging directory was removed")
}
