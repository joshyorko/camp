package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

type sessionObserver interface {
	Observe(context.Context, domain.JournalSnapshot) (SessionEvidence, error)
}

type generationHistory interface {
	List(context.Context, string, domain.Lineage) ([]domain.GenerationMetadata, error)
}

// OperationalQueries implements the read-only list, status, and history use
// cases. Process state is observed before session read models are built.
type OperationalQueries struct {
	Sessions sessionLister
	Observer sessionObserver
	History  generationHistory
}

type GenerationReadModel struct {
	Generation    uint64    `json:"generation"`
	ArchiveSHA256 string    `json:"archiveSHA256"`
	ObjectKey     string    `json:"objectKey"`
	Size          int64     `json:"size"`
	CreatedAt     time.Time `json:"createdAt"`
	SessionID     string    `json:"sessionId"`
}

func (q OperationalQueries) List(ctx context.Context) ([]SessionReadModel, error) {
	if q.Sessions == nil {
		return nil, errors.New("session lister is nil")
	}
	snapshots, err := q.Sessions.List(ctx)
	if err != nil {
		return nil, err
	}
	evidence, err := q.observe(ctx, snapshots)
	if err != nil {
		return nil, err
	}
	return BuildSessionReadModels(snapshots, evidence), nil
}

func (q OperationalQueries) Status(ctx context.Context, selector SessionSelector) (SessionReadModel, error) {
	snapshot, err := SelectSession(ctx, q.Sessions, selector, SelectionHistory)
	if err != nil {
		return SessionReadModel{}, err
	}
	evidence, err := q.observe(ctx, []domain.JournalSnapshot{snapshot})
	if err != nil {
		return SessionReadModel{}, err
	}
	models := BuildSessionReadModels([]domain.JournalSnapshot{snapshot}, evidence)
	return models[0], nil
}

func (q OperationalQueries) HistoryFor(ctx context.Context, selector SessionSelector) ([]GenerationReadModel, error) {
	if q.History == nil {
		return nil, errors.New("generation history is nil")
	}
	snapshot, err := SelectSession(ctx, q.Sessions, selector, SelectionHistory)
	if err != nil {
		return nil, err
	}
	generations, err := q.History.List(ctx, snapshot.Capsule, snapshot.Lineage)
	if err != nil {
		return nil, err
	}
	models := make([]GenerationReadModel, 0, len(generations))
	for _, generation := range generations {
		models = append(models, GenerationReadModel{
			Generation: generation.Generation.Generation, ArchiveSHA256: generation.Generation.ArchiveSHA256,
			ObjectKey: generation.ObjectKey, Size: generation.Size, CreatedAt: generation.CreatedAt, SessionID: generation.SessionID,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Generation != models[j].Generation {
			return models[i].Generation > models[j].Generation
		}
		if !models[i].CreatedAt.Equal(models[j].CreatedAt) {
			return models[i].CreatedAt.After(models[j].CreatedAt)
		}
		return models[i].ArchiveSHA256 < models[j].ArchiveSHA256
	})
	return models, nil
}

func (q OperationalQueries) observe(ctx context.Context, snapshots []domain.JournalSnapshot) (map[string]SessionEvidence, error) {
	if q.Observer == nil {
		return nil, nil
	}
	evidence := make(map[string]SessionEvidence, len(snapshots))
	for _, snapshot := range snapshots {
		observation, err := q.Observer.Observe(ctx, snapshot)
		if err != nil {
			return nil, fmt.Errorf("observe session %q: %w", snapshot.SessionID, err)
		}
		evidence[snapshot.SessionID] = observation
	}
	return evidence, nil
}
