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
	Capsule       string
	Branch        string
	CanonicalRoot string
}

func SelectActiveSession(ctx context.Context, sessions sessionLister, selector SessionSelector) (domain.JournalSnapshot, error) {
	if sessions == nil {
		return domain.JournalSnapshot{}, errors.New("session lister is nil")
	}
	listed, err := sessions.List(ctx)
	if err != nil {
		return domain.JournalSnapshot{}, err
	}
	matches := make([]domain.JournalSnapshot, 0, len(listed))
	for _, snapshot := range listed {
		if !activeSessionState(snapshot.State) ||
			(selector.Capsule != "" && snapshot.Capsule != selector.Capsule) ||
			(selector.Branch != "" && snapshot.Lineage.Branch != selector.Branch) ||
			(selector.CanonicalRoot != "" && snapshot.Materialization.CanonicalPath != selector.CanonicalRoot) {
			continue
		}
		matches = append(matches, snapshot)
	}
	if len(matches) == 0 {
		return domain.JournalSnapshot{}, ErrNoActiveSession
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	ids := make([]string, 0, len(matches))
	for _, snapshot := range matches {
		ids = append(ids, snapshot.SessionID)
	}
	sort.Strings(ids)
	return domain.JournalSnapshot{}, fmt.Errorf("%w: %s", ErrAmbiguousActiveSession, strings.Join(ids, ", "))
}

func activeSessionState(state domain.SessionState) bool {
	return state == domain.SessionOpening || state == domain.SessionOpen || state == domain.SessionRecovering
}
