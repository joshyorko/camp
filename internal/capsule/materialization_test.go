package capsule

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMarkCreatedWithTokenIsRecoveryIdempotent(t *testing.T) {
	t.Parallel()
	dataHome := t.TempDir()
	root := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	first, err := ownership.MarkCreatedWithToken(root, token)
	if err != nil {
		t.Fatalf("first MarkCreatedWithToken() error = %v", err)
	}
	second, err := ownership.MarkCreatedWithToken(root, token)
	if err != nil {
		t.Fatalf("recovery MarkCreatedWithToken() error = %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("recovered materialization = %#v, want %#v", second, first)
	}
	removed, err := ownership.RemoveOwned(context.Background(), second)
	if err != nil || !removed {
		t.Fatalf("RemoveOwned() = %v, %v", removed, err)
	}
}
