package campkit

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
