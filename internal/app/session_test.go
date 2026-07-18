package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

type fakeSessionLister struct {
	sessions []domain.JournalSnapshot
	err      error
}

func TestSelectActiveSessionUsesCanonicalPrecedence(t *testing.T) {
	t.Parallel()
	sessions := []domain.JournalSnapshot{
		{SessionID: "explicit", Capsule: "alpha", Lineage: domain.Lineage{Branch: "feature"}, State: domain.SessionOpen, Materialization: domain.Materialization{CanonicalPath: "/work/other"}},
		{SessionID: "cwd", Capsule: "beta", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen, Materialization: domain.Materialization{CanonicalPath: "/work/here"}},
		{SessionID: "default", Capsule: "alpha", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen, Materialization: domain.Materialization{CanonicalPath: "/work/default"}},
	}

	tests := []struct {
		name     string
		selector SessionSelector
		want     string
	}{
		{name: "explicit session wins", selector: SessionSelector{SessionID: "explicit", Capsule: "beta", CanonicalRoot: "/work/here", DefaultCapsule: "alpha", DefaultBranch: "main"}, want: "explicit"},
		{name: "explicit capsule and branch win over cwd", selector: SessionSelector{Capsule: "alpha", Branch: "feature", CanonicalRoot: "/work/here", DefaultCapsule: "alpha", DefaultBranch: "main"}, want: "explicit"},
		{name: "cwd wins over defaults", selector: SessionSelector{CanonicalRoot: "/work/here", DefaultCapsule: "alpha", DefaultBranch: "main"}, want: "cwd"},
		{name: "defaults are last", selector: SessionSelector{DefaultCapsule: "alpha", DefaultBranch: "main"}, want: "default"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected, err := SelectActiveSession(context.Background(), fakeSessionLister{sessions: sessions}, test.selector)
			if err != nil || selected.SessionID != test.want {
				t.Fatalf("selection = %q, %v, want %q", selected.SessionID, err, test.want)
			}
		})
	}
}

func TestSelectActiveSessionErrorsAreActionableAndDeterministic(t *testing.T) {
	t.Parallel()
	sessions := []domain.JournalSnapshot{
		{SessionID: "zeta", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen},
		{SessionID: "alpha", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionRecovering},
	}
	_, err := SelectActiveSession(context.Background(), fakeSessionLister{sessions: sessions}, SessionSelector{Capsule: "brain", Branch: "main"})
	var selectionErr *SessionSelectionError
	if !errors.As(err, &selectionErr) {
		t.Fatalf("error = %T %v, want SessionSelectionError", err, err)
	}
	if selectionErr.Code != SelectionAmbiguous || !reflect.DeepEqual(selectionErr.Candidates, []string{"alpha", "zeta"}) || !reflect.DeepEqual(selectionErr.NextCommands, []string{"camp status --session alpha", "camp status --session zeta"}) {
		t.Fatalf("selection error = %#v", selectionErr)
	}

	_, err = SelectActiveSession(context.Background(), fakeSessionLister{}, SessionSelector{Capsule: "missing"})
	if !errors.As(err, &selectionErr) || selectionErr.Code != SelectionNotFound || !reflect.DeepEqual(selectionErr.NextCommands, []string{"camp list", "camp open --capsule missing"}) {
		t.Fatalf("not-found error = %#v, %v", selectionErr, err)
	}
}

func TestSelectionPurposeSeparatesMutationHistoryAndRecovery(t *testing.T) {
	t.Parallel()
	closed := domain.JournalSnapshot{SessionID: "closed", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionClosed, Cleanup: domain.Cleanup{State: domain.CleanupFailed}}
	recovering := domain.JournalSnapshot{SessionID: "recovering", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionRecovering}
	lister := fakeSessionLister{sessions: []domain.JournalSnapshot{closed, recovering}}

	if _, err := SelectSession(context.Background(), lister, SessionSelector{SessionID: "closed"}, SelectionActiveMutation); !errors.Is(err, ErrNoActiveSession) {
		t.Fatalf("active closed error = %v", err)
	}
	if selected, err := SelectSession(context.Background(), lister, SessionSelector{SessionID: "closed"}, SelectionHistory); err != nil || selected.SessionID != "closed" {
		t.Fatalf("history selection = %#v, %v", selected, err)
	}
	if selected, err := SelectSession(context.Background(), lister, SessionSelector{SessionID: "closed"}, SelectionRecovery); err != nil || selected.SessionID != "closed" {
		t.Fatalf("cleanup recovery selection = %#v, %v", selected, err)
	}
	if selected, err := SelectSession(context.Background(), lister, SessionSelector{SessionID: "recovering"}, SelectionRecovery); err != nil || selected.SessionID != "recovering" {
		t.Fatalf("recovering selection = %#v, %v", selected, err)
	}
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
