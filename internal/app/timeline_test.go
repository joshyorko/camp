package app

import (
	"context"
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

type timelineSessionsStub struct {
	snapshots []domain.JournalSnapshot
	err       error
}

func (s timelineSessionsStub) List(context.Context) ([]domain.JournalSnapshot, error) {
	return s.snapshots, s.err
}
