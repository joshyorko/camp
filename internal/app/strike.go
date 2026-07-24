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
	Effects    CloseEffects
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
			if err := s.quiesce(ctx, session, request.Purge); err != nil {
				return StrikeResult{}, fmt.Errorf("quiesce session %s before strike: %w", session.SessionID, err)
			}
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

func (s Strike) quiesce(ctx context.Context, session domain.JournalSnapshot, purge bool) error {
	if s.Effects == nil {
		return errors.New("strike cleanup dependencies are incomplete")
	}
	if session.Workspace.ID != "" {
		if session.Workspace.Context == "" {
			return errors.New("recorded workspace identity is incomplete")
		}
		if err := s.Effects.CloseWorkspace(ctx, session, false); err != nil {
			return err
		}
	}
	if len(session.Recovery.Forwarding) != 0 {
		if err := s.Effects.StopForwarders(ctx, session); err != nil {
			return err
		}
	}
	if len(session.Services) != 0 {
		if err := s.Effects.StopServices(ctx, session); err != nil {
			return err
		}
	}
	if session.Supervisor.Identity != (domain.ProcessIdentity{}) || session.Supervisor.Desired != "" {
		if err := s.Effects.StopSupervisor(ctx, session); err != nil {
			return err
		}
	}
	if purge && session.Materialization.SchemaVersion != 0 {
		removed, err := s.Effects.RemoveMaterialization(ctx, session)
		if err != nil {
			return err
		}
		if session.Materialization.Mode == domain.MaterializationCreated && !removed {
			return errors.New("owned materialization was not removed")
		}
		if session.Materialization.Mode == domain.MaterializationAdopted && removed {
			return errors.New("adopted materialization was removed")
		}
	}
	return nil
}
