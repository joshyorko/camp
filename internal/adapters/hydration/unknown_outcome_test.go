package hydration

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/ports"
)

func TestHydrateReplayAfterUnknownHaulerLoadOutcomeDoesNotLoadAgain(t *testing.T) {
	fixture := newHydrationFixture(t)
	crash := errors.New("injected crash after Hauler Load success")
	hauler := &unknownOutcomeHydrationHauler{
		delegate:            fixture.hauler,
		loadErrorAfterFirst: crash,
	}

	first := NewController(fixture.store, hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
		t.Fatalf("first Hydrate() error = %v, want injected crash", err)
	}

	second := NewController(fixture.store, hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	if _, err := second.Hydrate(context.Background(), fixture.request); err != nil {
		t.Fatalf("recovery Hydrate() error = %v", err)
	}
	if hauler.loadCalls != 1 {
		t.Fatalf("Hauler Load calls = %d, want 1 after unknown-outcome replay", hauler.loadCalls)
	}
}

func TestHydrateReplayAfterUnknownHaulerExtractOutcomeUsesPublishedArtifactWithoutSecondExtract(t *testing.T) {
	fixture := newHydrationFixture(t)
	crash := errors.New("injected crash after Hauler Extract success")
	hauler := &unknownOutcomeHydrationHauler{
		delegate:               fixture.hauler,
		extractErrorAfterFirst: crash,
	}

	first := NewController(fixture.store, hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
		t.Fatalf("first Hydrate() error = %v, want injected crash", err)
	}

	second := NewController(fixture.store, hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	if _, err := second.Hydrate(context.Background(), fixture.request); err != nil {
		t.Fatalf("recovery Hydrate() error = %v", err)
	}
	if hauler.extractCalls != 1 {
		t.Fatalf("Hauler Extract calls = %d, want 1 after unknown-outcome replay", hauler.extractCalls)
	}
}

type unknownOutcomeHydrationHauler struct {
	delegate               *hydrationHauler
	loadErrorAfterFirst    error
	extractErrorAfterFirst error
	loadCalls              int
	extractCalls           int
}

func (h *unknownOutcomeHydrationHauler) Load(ctx context.Context, store string, filenames []string) (ports.Result, error) {
	h.loadCalls++
	result, err := h.delegate.Load(ctx, store, filenames)
	if err == nil && h.loadCalls == 1 && h.loadErrorAfterFirst != nil {
		return result, h.loadErrorAfterFirst
	}
	return result, err
}

func (h *unknownOutcomeHydrationHauler) Extract(ctx context.Context, store, reference, output string) (ports.Result, error) {
	h.extractCalls++
	result, err := h.delegate.Extract(ctx, store, reference, output)
	if err == nil && h.extractCalls == 1 && h.extractErrorAfterFirst != nil {
		return result, h.extractErrorAfterFirst
	}
	return result, err
}
