package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

func TestTimelineMarksUnboundHistoricalSessionAsUnknownBlueprint(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	timeline := NewTimeline(timelineSessionsStub{snapshots: []domain.JournalSnapshot{{SessionID: "old", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionClosed, CreatedAt: now, UpdatedAt: now}}}, nil)
	entries, err := timeline.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Blueprint != BlueprintUnknown || entries[0].SessionID != "old" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestTimelineClassifiesOnlyValidBindingsAsKnown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	snapshots := []domain.JournalSnapshot{
		{SessionID: "valid", UpdatedAt: now.Add(-time.Minute)},
		{SessionID: "malformed", UpdatedAt: now},
		{SessionID: "zero", UpdatedAt: now},
	}
	bindings := timelineBindingsStub{bindings: map[string]domain.ExecutionBinding{
		"valid": {
			SchemaVersion: domain.ExecutionBindingSchemaVersion,
			Blueprint: domain.BlueprintRef{
				SchemaVersion: domain.BlueprintRefSchemaVersion,
				Digest:        strings.Repeat("a", 64),
			},
		},
		"malformed": {
			SchemaVersion: domain.ExecutionBindingSchemaVersion,
			Blueprint: domain.BlueprintRef{
				SchemaVersion: domain.BlueprintRefSchemaVersion,
				Digest:        "bad",
			},
		},
		"zero": {},
	}}
	entries, err := NewTimeline(timelineSessionsStub{snapshots: snapshots}, bindings).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].SessionID != "malformed" || entries[1].SessionID != "zero" || entries[2].SessionID != "valid" {
		t.Fatalf("ordering = %#v", entries)
	}
	if entries[0].Blueprint != BlueprintUnknown || entries[0].Binding != nil || entries[1].Blueprint != BlueprintUnknown || entries[1].Binding != nil {
		t.Fatalf("malformed bindings classified as known: %#v", entries)
	}
	if entries[2].Blueprint != BlueprintKnown || entries[2].Binding == nil {
		t.Fatalf("valid binding classified as unknown: %#v", entries[2])
	}
}

func TestTimelineReturnsBindingReadErrors(t *testing.T) {
	t.Parallel()
	want := errors.New("read binding")
	_, err := NewTimeline(
		timelineSessionsStub{snapshots: []domain.JournalSnapshot{{SessionID: "one"}}},
		timelineBindingsStub{err: want},
	).List(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("List() error = %v, want %v", err, want)
	}
}

type timelineSessionsStub struct {
	snapshots []domain.JournalSnapshot
	err       error
}

func (s timelineSessionsStub) List(context.Context) ([]domain.JournalSnapshot, error) {
	return s.snapshots, s.err
}

type timelineBindingsStub struct {
	bindings map[string]domain.ExecutionBinding
	err      error
}

func (s timelineBindingsStub) Binding(_ context.Context, sessionID string) (domain.ExecutionBinding, bool, error) {
	if s.err != nil {
		return domain.ExecutionBinding{}, false, s.err
	}
	binding, found := s.bindings[sessionID]
	return binding, found, nil
}
