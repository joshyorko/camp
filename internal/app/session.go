package app

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
)

var (
	ErrNoActiveSession        = errors.New("no active Camp session")
	ErrAmbiguousActiveSession = errors.New("multiple active Camp sessions")
)

type sessionLister interface {
	List(context.Context) ([]domain.JournalSnapshot, error)
}

type SessionSelector struct {
	SessionID      string
	Capsule        string
	Branch         string
	CanonicalRoot  string
	DefaultCapsule string
	DefaultBranch  string
}

type SelectionPurpose string

const (
	SelectionActiveMutation SelectionPurpose = "active-mutation"
	SelectionHistory        SelectionPurpose = "history"
	SelectionRecovery       SelectionPurpose = "recovery"
)

type SelectionCode string

const (
	SelectionNotFound  SelectionCode = "session_not_found"
	SelectionAmbiguous SelectionCode = "session_ambiguous"
)

type SessionSelectionError struct {
	Code         SelectionCode
	Candidates   []string
	NextCommands []string
	cause        error
}

func (e *SessionSelectionError) Error() string {
	message := e.cause.Error()
	if len(e.Candidates) > 0 {
		message += ": " + strings.Join(e.Candidates, ", ")
	}
	if len(e.NextCommands) > 0 {
		message += "; next: " + strings.Join(e.NextCommands, " or ")
	}
	return message
}

func (e *SessionSelectionError) Unwrap() error { return e.cause }

func SelectActiveSession(ctx context.Context, sessions sessionLister, selector SessionSelector) (domain.JournalSnapshot, error) {
	return SelectSession(ctx, sessions, selector, SelectionActiveMutation)
}

// SelectReopenSession selects a closed historical session when one exists. A
// fresh controller has no journal history, so an implicit reopen may instead
// continue through manifest and backend resolution. Explicit session IDs
// remain journal-authoritative and therefore fail closed when absent.
func SelectReopenSession(ctx context.Context, sessions sessionLister, selector SessionSelector) (selected domain.JournalSnapshot, manifestFallback bool, err error) {
	selected, err = SelectSession(ctx, sessions, selector, SelectionHistory)
	if err == nil {
		return selected, false, nil
	}
	var selectionErr *SessionSelectionError
	if selector.SessionID == "" && errors.As(err, &selectionErr) && selectionErr.Code == SelectionNotFound {
		return domain.JournalSnapshot{}, true, nil
	}
	return domain.JournalSnapshot{}, false, err
}

func SelectSession(ctx context.Context, sessions sessionLister, selector SessionSelector, purpose SelectionPurpose) (domain.JournalSnapshot, error) {
	if sessions == nil {
		return domain.JournalSnapshot{}, errors.New("session lister is nil")
	}
	listed, err := sessions.List(ctx)
	if err != nil {
		return domain.JournalSnapshot{}, err
	}
	eligible := make([]domain.JournalSnapshot, 0, len(listed))
	for _, snapshot := range listed {
		if !sessionEligible(snapshot, purpose) {
			continue
		}
		eligible = append(eligible, snapshot)
	}
	matches := applySessionPrecedence(eligible, selector)
	if len(matches) == 0 {
		commands := []string{"camp list"}
		if selector.Capsule != "" {
			commands = append(commands, "camp open --camp "+selector.Capsule)
		}
		cause := error(ErrNoActiveSession)
		if purpose != SelectionActiveMutation {
			cause = errors.New("no matching Camp session")
		}
		return domain.JournalSnapshot{}, &SessionSelectionError{Code: SelectionNotFound, NextCommands: commands, cause: cause}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if purpose == SelectionHistory && selector.SessionID == "" {
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].UpdatedAt.After(matches[j].UpdatedAt)
		})
		return matches[0], nil
	}
	ids := make([]string, 0, len(matches))
	for _, snapshot := range matches {
		ids = append(ids, snapshot.SessionID)
	}
	sort.Strings(ids)
	commands := make([]string, 0, len(ids))
	for _, id := range ids {
		commands = append(commands, "camp status --session "+id)
	}
	cause := error(ErrAmbiguousActiveSession)
	if purpose != SelectionActiveMutation {
		cause = errors.New("multiple matching Camp sessions")
	}
	return domain.JournalSnapshot{}, &SessionSelectionError{Code: SelectionAmbiguous, Candidates: ids, NextCommands: commands, cause: cause}
}

func applySessionPrecedence(sessions []domain.JournalSnapshot, selector SessionSelector) []domain.JournalSnapshot {
	if selector.SessionID != "" {
		return filterSessions(sessions, func(snapshot domain.JournalSnapshot) bool { return snapshot.SessionID == selector.SessionID })
	}
	if selector.Capsule != "" || selector.Branch != "" {
		return filterSessions(sessions, func(snapshot domain.JournalSnapshot) bool {
			return (selector.Capsule == "" || snapshot.Capsule == selector.Capsule) &&
				(selector.Branch == "" || snapshot.Lineage.Branch == selector.Branch)
		})
	}
	if selector.CanonicalRoot != "" {
		return filterSessions(sessions, func(snapshot domain.JournalSnapshot) bool {
			return snapshot.Materialization.CanonicalPath == selector.CanonicalRoot
		})
	}
	if selector.DefaultCapsule != "" || selector.DefaultBranch != "" {
		return filterSessions(sessions, func(snapshot domain.JournalSnapshot) bool {
			return (selector.DefaultCapsule == "" || snapshot.Capsule == selector.DefaultCapsule) &&
				(selector.DefaultBranch == "" || snapshot.Lineage.Branch == selector.DefaultBranch)
		})
	}
	return sessions
}

func filterSessions(sessions []domain.JournalSnapshot, keep func(domain.JournalSnapshot) bool) []domain.JournalSnapshot {
	filtered := make([]domain.JournalSnapshot, 0, len(sessions))
	for _, snapshot := range sessions {
		if keep(snapshot) {
			filtered = append(filtered, snapshot)
		}
	}
	return filtered
}

func sessionEligible(snapshot domain.JournalSnapshot, purpose SelectionPurpose) bool {
	switch purpose {
	case SelectionActiveMutation:
		return activeSessionState(snapshot.State)
	case SelectionHistory:
		return snapshot.State == domain.SessionClosed
	case SelectionRecovery:
		return true
	default:
		return false
	}
}

func activeSessionState(state domain.SessionState) bool {
	return state == domain.SessionOpening || state == domain.SessionOpen || state == domain.SessionRecovering
}
