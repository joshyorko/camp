package app

import (
	"context"
	"errors"
	"fmt"
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
	if sessions == nil {
		return domain.JournalSnapshot{}, false, errors.New("session lister is nil")
	}
	listed, err := sessions.List(ctx)
	if err != nil {
		return domain.JournalSnapshot{}, false, err
	}
	for _, snapshot := range listed {
		if err := validateReopenHistorySnapshot(snapshot); err != nil {
			return domain.JournalSnapshot{}, false, err
		}
	}
	if len(listed) == 0 && selector.SessionID == "" {
		return domain.JournalSnapshot{}, true, nil
	}
	selected, err = selectSession(listed, selector, SelectionHistory)
	return selected, false, err
}

func SelectSession(ctx context.Context, sessions sessionLister, selector SessionSelector, purpose SelectionPurpose) (domain.JournalSnapshot, error) {
	if sessions == nil {
		return domain.JournalSnapshot{}, errors.New("session lister is nil")
	}
	listed, err := sessions.List(ctx)
	if err != nil {
		return domain.JournalSnapshot{}, err
	}
	return selectSession(listed, selector, purpose)
}

func selectSession(listed []domain.JournalSnapshot, selector SessionSelector, purpose SelectionPurpose) (domain.JournalSnapshot, error) {
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
		if !matches[0].UpdatedAt.Equal(matches[1].UpdatedAt) {
			return matches[0], nil
		}
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

func validateReopenHistorySnapshot(snapshot domain.JournalSnapshot) error {
	if snapshot.SchemaVersion != domain.SchemaVersion ||
		snapshot.SessionID == "" || strings.ContainsAny(snapshot.SessionID, "/\\\x00") ||
		snapshot.Capsule == "" || strings.ContainsAny(snapshot.Capsule, "/\\\x00") ||
		snapshot.Lineage.Branch == "" {
		return fmt.Errorf("journal snapshot %q has invalid identity", snapshot.SessionID)
	}
	if snapshot.Mode != domain.SessionReadWrite && snapshot.Mode != domain.SessionReadOnly {
		return fmt.Errorf("journal snapshot %q has invalid mode %q", snapshot.SessionID, snapshot.Mode)
	}
	switch snapshot.State {
	case domain.SessionOpening, domain.SessionOpen, domain.SessionRecovering, domain.SessionClosed:
	default:
		return fmt.Errorf("journal snapshot %q has invalid state %q", snapshot.SessionID, snapshot.State)
	}
	if snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return fmt.Errorf("journal snapshot %q has invalid timestamps", snapshot.SessionID)
	}
	if configured := snapshot.Recovery.Configuration.Capsule; configured != "" && configured != snapshot.Capsule {
		return fmt.Errorf("journal snapshot %q has mismatched capsule identity", snapshot.SessionID)
	}
	if lease := snapshot.Lease.Lease; lease != nil &&
		(lease.SessionID != snapshot.SessionID || lease.Capsule != snapshot.Capsule || lease.Lineage != snapshot.Lineage) {
		return fmt.Errorf("journal snapshot %q has mismatched lease identity", snapshot.SessionID)
	}
	return nil
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
