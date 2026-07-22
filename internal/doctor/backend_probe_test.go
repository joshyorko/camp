package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/ports"
)

func TestBackendTransactionProbeProvesCASConflictReadbackAndCleanup(t *testing.T) {
	store, err := filebackend.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result := (BackendTransactionProbe{Store: store, NewSuffix: func() (string, error) { return "unique", nil }}).Probe(context.Background())
	if result.Status != StatusHealthy || result.Code != "backend_transaction_verified" || result.Evidence["operations"] != "create,readback,replace,conflict,readback,cleanup" {
		t.Fatalf("result = %#v", result)
	}
	items, _, err := store.List(context.Background(), "camp-doctor/", "")
	if err != nil || len(items) != 0 {
		t.Fatalf("probe leftovers = %#v, error = %v", items, err)
	}
}

type deleteFailingStore struct{ ports.ObjectStore }

func (deleteFailingStore) DeleteConditional(context.Context, string, ports.Revision) error {
	return errors.New("credential=secret")
}

func TestBackendTransactionProbeBlocksWhenIdentitySafeCleanupFails(t *testing.T) {
	store, err := filebackend.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result := (BackendTransactionProbe{Store: deleteFailingStore{store}, NewSuffix: func() (string, error) { return "unique", nil }}).Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "backend_cleanup_failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBackendTransactionProbeBlocksWhenResourceIdentityChangesBeforeCleanup(t *testing.T) {
	store, err := filebackend.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &identityChangingStore{ObjectStore: store}
	result := (BackendTransactionProbe{Store: wrapped, NewSuffix: func() (string, error) { return "unique", nil }}).Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "backend_cleanup_identity_mismatch" {
		t.Fatalf("result = %#v", result)
	}
}

type identityChangingStore struct {
	ports.ObjectStore
	replaced bool
}

func (s *identityChangingStore) DeleteConditional(ctx context.Context, key string, revision ports.Revision) error {
	if !s.replaced {
		s.replaced = true
		_, _ = s.ObjectStore.PutConditional(ctx, key, []byte("foreign"), ports.WriteCondition{MatchRevision: revision})
	}
	return s.ObjectStore.DeleteConditional(ctx, key, revision)
}
