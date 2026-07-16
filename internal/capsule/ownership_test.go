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

func TestOwnershipRevalidateAdoptedRejectsReplacedRootIdentity(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.Adopt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ownership.Revalidate(record); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("Revalidate(replaced adopted root) error = %v, want ErrOwnershipMismatch", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("Revalidate mutated replacement root: info=%v error=%v", info, err)
	}
}

func TestOwnershipRevalidateCreatedAcceptsExactOwnedMarker(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "brain", "main", "session-a")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ownership.Revalidate(record); err != nil {
		t.Fatalf("Revalidate(created) error = %v", err)
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

func TestOwnershipRejectsSymlinkedMarkerParents(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(created, ".camp")); err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ownership.MarkCreatedWithToken(created, token); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("MarkCreatedWithToken() error = %v, want ErrOwnershipMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "runtime", "ownership.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

func TestOwnershipRemovalRejectsSymlinkedMarkerParents(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	record, err := ownership.MarkCreatedWithToken(created, token)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(created, ".camp", "runtime", "ownership.json"))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(created, ".camp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(created, ".camp")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "runtime", "ownership.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(context.Background(), record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned() = %v, %v, want ownership mismatch", removed, err)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("mismatched root was removed: %v", err)
	}
}

func TestOwnershipRemovalRejectsSymlinkedMarkerFile(t *testing.T) {
	t.Parallel()
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
	markerPath := filepath.Join(created, ".camp", "runtime", "ownership.json")
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "ownership.json")
	if err := os.WriteFile(outside, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, markerPath); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(context.Background(), record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned() = %v, %v, want ownership mismatch", removed, err)
	}
}
