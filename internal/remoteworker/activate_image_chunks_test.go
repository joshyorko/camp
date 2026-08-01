package remoteworker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/haulkit"
)

func TestReassembleBootstrapKitPublishesOnlyVerifiedArchive(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "source.tar.zst")
	body := []byte("verified bounded bootstrap chunks")
	if err := os.WriteFile(archive, body, 0o600); err != nil {
		t.Fatal(err)
	}
	chunksDir := filepath.Join(root, "chunks")
	chunks, err := haulkit.Split(context.Background(), archive, chunksDir, 8)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	expected := FileIdentity{Name: "camp-hauler-kit.tar.zst", SHA256: fmt.Sprintf("%x", digest), Size: int64(len(body))}
	output := filepath.Join(root, expected.Name)

	if err := reassembleBootstrapKit(context.Background(), chunksDir, chunks, output, expected); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("reassembled archive = %q", got)
	}

	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	chunkPath := filepath.Join(chunksDir, chunks[0].Name)
	if err := os.Remove(chunkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chunkPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reassembleBootstrapKit(context.Background(), chunksDir, chunks, output, expected); err == nil {
		t.Fatal("reassembleBootstrapKit() accepted a tampered chunk")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("failed reassembly published output: %v", err)
	}
}
