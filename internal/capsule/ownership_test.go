package capsule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnershipNeverDeletesAdoptedRootAndDeletesOnlyMatchingCreatedRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}

	adopted := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(adopted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adopted, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	adoptedRecord, err := ownership.Adopt(adopted)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := ownership.RemoveOwned(ctx, adoptedRecord)
	if err != nil || removed {
		t.Fatalf("RemoveOwned(adopted) = %v, %v; want preserved", removed, err)
	}
	if _, err := os.Stat(filepath.Join(adopted, "keep.txt")); err != nil {
		t.Fatalf("adopted source was changed: %v", err)
	}

	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	createdRecord, err := ownership.MarkCreated(created)
	if err != nil {
		t.Fatal(err)
	}
	removed, err = ownership.RemoveOwned(ctx, createdRecord)
	if err != nil || !removed {
		t.Fatalf("RemoveOwned(created) = %v, %v; want removed", removed, err)
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created root still exists: %v", err)
	}
}

func TestOwnershipFailsClosedOnMarkerOrIdentityMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(created)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(created, ".camp", "runtime", "ownership.json")
	if err := os.WriteFile(marker, []byte(`{"token":"attacker"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(ctx, record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned(marker mismatch) = %v, %v", removed, err)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("mismatched root was deleted: %v", err)
	}

	if err := os.RemoveAll(created); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(ctx, record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned(inode mismatch) = %v, %v", removed, err)
	}
}
