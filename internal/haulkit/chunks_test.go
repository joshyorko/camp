package haulkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSplitAndReassemblePreserveExactBytesAndOrder(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "kit.tar.zst")
	body := []byte("abcdefghij")
	if err := os.WriteFile(source, body, 0o600); err != nil {
		t.Fatal(err)
	}
	chunks, err := Split(context.Background(), source, filepath.Join(directory, "chunks"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 || chunks[0].Index != 0 || chunks[1].Size != 4 || chunks[2].Size != 2 {
		t.Fatalf("chunks = %#v", chunks)
	}
	output := filepath.Join(directory, "rebuilt.tar.zst")
	if err := Reassemble(context.Background(), filepath.Join(directory, "chunks"), chunks, output, 4); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("rebuilt bytes = %q, want %q", got, body)
	}
}

func TestReassembleRejectsHostileChunkSets(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "kit.tar.zst")
	if err := os.WriteFile(source, []byte("abcdefghij"), 0o600); err != nil {
		t.Fatal(err)
	}
	chunkDirectory := filepath.Join(directory, "chunks")
	chunks, err := Split(context.Background(), source, chunkDirectory, 4)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]ChunkIdentity) []ChunkIdentity
	}{
		{"missing", func(ids []ChunkIdentity) []ChunkIdentity { return append(ids[:1], ids[2:]...) }},
		{"duplicated", func(ids []ChunkIdentity) []ChunkIdentity { return append(ids, ids[2]) }},
		{"reordered", func(ids []ChunkIdentity) []ChunkIdentity { ids[0], ids[1] = ids[1], ids[0]; return ids }},
		{"oversized", func(ids []ChunkIdentity) []ChunkIdentity { ids[0].Size = 5; return ids }},
		{"traversal", func(ids []ChunkIdentity) []ChunkIdentity { ids[0].Name = "../escape"; return ids }},
		{"absolute", func(ids []ChunkIdentity) []ChunkIdentity { ids[0].Name = "/tmp/escape"; return ids }},
		{"corrupted", func(ids []ChunkIdentity) []ChunkIdentity { return ids }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "corrupted" {
				chunkPath := filepath.Join(chunkDirectory, chunks[0].Name)
				if err := os.Chmod(chunkPath, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(chunkPath, []byte("xxxx"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ids := append([]ChunkIdentity(nil), chunks...)
			err := Reassemble(context.Background(), chunkDirectory, test.mutate(ids), filepath.Join(directory, test.name+".tar.zst"), 4)
			if err == nil {
				t.Fatal("Reassemble() error = nil")
			}
			if _, statErr := os.Stat(filepath.Join(directory, test.name+".tar.zst")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial output remains: %v", statErr)
			}
		})
		if test.name == "corrupted" {
			break
		}
	}
}

func TestReassembleRejectsChunkSwappedToSameByteSymlinkAtOpenBoundary(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "kit.tar.zst")
	body := []byte("same-bytes")
	if err := os.WriteFile(source, body, 0o600); err != nil {
		t.Fatal(err)
	}
	chunkDirectory := filepath.Join(directory, "chunks")
	chunks, err := Split(context.Background(), source, chunkDirectory, 64)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "attacker-controlled")
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
	chunkPath := filepath.Join(chunkDirectory, chunks[0].Name)
	previous := beforeOpenRegular
	t.Cleanup(func() { beforeOpenRegular = previous })
	swapped := false
	beforeOpenRegular = func(path string) error {
		if path != chunkPath || swapped {
			return nil
		}
		swapped = true
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Symlink(target, path)
	}
	err = Reassemble(context.Background(), chunkDirectory, chunks, filepath.Join(directory, "rebuilt"), 64)
	if err == nil {
		t.Fatal("Reassemble() accepted symlink swapped at open boundary")
	}
}
