package app

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

type fakeSessionLister struct {
	sessions []domain.JournalSnapshot
	err      error
}

func (l fakeSessionLister) List(context.Context) ([]domain.JournalSnapshot, error) {
	return l.sessions, l.err
}

func TestSelectActiveSessionEnforcesZeroOneManyPolicy(t *testing.T) {
	t.Parallel()
	closed := domain.JournalSnapshot{SessionID: "closed", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionClosed}
	first := domain.JournalSnapshot{SessionID: "first", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen}
	second := domain.JournalSnapshot{SessionID: "second", Capsule: "other", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionRecovering}

	if _, err := SelectActiveSession(context.Background(), fakeSessionLister{sessions: []domain.JournalSnapshot{closed}}, SessionSelector{}); !errors.Is(err, ErrNoActiveSession) {
		t.Fatalf("zero-active error = %v, want ErrNoActiveSession", err)
	}
	selected, err := SelectActiveSession(context.Background(), fakeSessionLister{sessions: []domain.JournalSnapshot{closed, first}}, SessionSelector{})
	if err != nil || selected.SessionID != first.SessionID {
		t.Fatalf("one-active selection = %#v, %v", selected, err)
	}
	if _, err := SelectActiveSession(context.Background(), fakeSessionLister{sessions: []domain.JournalSnapshot{first, second}}, SessionSelector{}); !errors.Is(err, ErrAmbiguousActiveSession) {
		t.Fatalf("many-active error = %v, want ErrAmbiguousActiveSession", err)
	}
	selected, err = SelectActiveSession(context.Background(), fakeSessionLister{sessions: []domain.JournalSnapshot{first, second}}, SessionSelector{Capsule: "other", Branch: "main"})
	if err != nil || selected.SessionID != second.SessionID {
		t.Fatalf("filtered re-entry selection = %#v, %v", selected, err)
	}
}
