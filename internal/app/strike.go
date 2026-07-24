package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/joshyorko/camp/internal/domain"
)

var ErrStrikeBackend = errors.New("strike requires Camp's managed local file backend")

type StrikeRequest struct {
	Purge bool `json:"purge"`
	Yes   bool `json:"yes"`
}

type StrikePlan struct {
	DataRoot    string   `json:"dataRoot"`
	Targets     []string `json:"targets"`
	BackendSafe bool     `json:"backendSafe"`
}

type StrikeResult struct {
	ArchivedPath string `json:"archivedPath,omitempty"`
	Purged       bool   `json:"purged"`
}

type StrikeSessions interface {
	List(context.Context) ([]domain.JournalSnapshot, error)
}

type StrikeController interface {
	Archive(context.Context, StrikePlan) (string, error)
	Purge(context.Context, StrikePlan) error
}

type Strike struct {
	Sessions   StrikeSessions
	Controller StrikeController
}

func (s Strike) Run(ctx context.Context, request StrikeRequest, plan StrikePlan) (StrikeResult, error) {
	if request.Purge && !request.Yes {
		return StrikeResult{}, errors.New("permanent purge requires both --purge and --yes")
	}
	if !plan.BackendSafe {
		return StrikeResult{}, ErrStrikeBackend
	}
	sessions, err := s.Sessions.List(ctx)
	if err != nil {
		return StrikeResult{}, err
	}
	for _, session := range sessions {
		switch session.State {
		case domain.SessionOpening, domain.SessionOpen, domain.SessionRecovering:
			return StrikeResult{}, fmt.Errorf("refusing strike while session %s is %s; close or recover it first", session.SessionID, session.State)
		}
	}
	if request.Purge {
		if err := s.Controller.Purge(ctx, plan); err != nil {
			return StrikeResult{}, err
		}
		return StrikeResult{Purged: true}, nil
	}
	archive, err := s.Controller.Archive(ctx, plan)
	if err != nil {
		return StrikeResult{}, err
	}
	return StrikeResult{ArchivedPath: archive}, nil
}
