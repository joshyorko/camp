package app

import (
	"context"
	"sort"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
)

type pointerLister interface {
	List(context.Context) ([]coordination.PointerRecord, error)
}

type CampReadModel struct {
	Capsule       string    `json:"capsule"`
	Branch        string    `json:"branch"`
	Generation    uint64    `json:"generation,omitempty"`
	ArchiveSHA256 string    `json:"archiveSHA256,omitempty"`
	State         string    `json:"state"`
	SessionID     string    `json:"sessionId,omitempty"`
	Backend       string    `json:"backend"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type CampInventory struct {
	Sessions sessionLister
	Pointers pointerLister
	Backend  string
}

func (q CampInventory) List(ctx context.Context) ([]CampReadModel, error) {
	snapshots, err := q.Sessions.List(ctx)
	if err != nil {
		return nil, err
	}
	pointers, err := q.Pointers.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := map[string]CampReadModel{}
	for _, record := range pointers {
		pointer := record.Pointer
		key := pointer.Capsule + "\x00" + pointer.Lineage.Branch
		rows[key] = CampReadModel{
			Capsule: pointer.Capsule, Branch: pointer.Lineage.Branch,
			Generation: pointer.Generation.Generation, ArchiveSHA256: pointer.Generation.ArchiveSHA256,
			State: "stored", SessionID: pointer.SessionID, Backend: q.Backend, UpdatedAt: pointer.CreatedAt,
		}
	}
	for _, snapshot := range snapshots {
		if snapshot.Capsule == "" || snapshot.Lineage.Branch == "" {
			continue
		}
		key := snapshot.Capsule + "\x00" + snapshot.Lineage.Branch
		row, ok := rows[key]
		if !ok {
			row = CampReadModel{Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, Backend: q.Backend}
		}
		if row.SessionID == "" || snapshot.UpdatedAt.After(row.UpdatedAt) {
			row.SessionID = snapshot.SessionID
			row.State = string(snapshot.State)
			row.UpdatedAt = snapshot.UpdatedAt
		}
		if snapshot.Checkpoint.Generation != nil && snapshot.Checkpoint.Generation.Generation > row.Generation {
			row.Generation = snapshot.Checkpoint.Generation.Generation
			row.ArchiveSHA256 = snapshot.Checkpoint.Generation.ArchiveSHA256
		}
		rows[key] = row
	}
	result := make([]CampReadModel, 0, len(rows))
	for _, row := range rows {
		if row.State == "" {
			row.State = string(domain.SessionClosed)
		}
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Capsule != result[j].Capsule {
			return result[i].Capsule < result[j].Capsule
		}
		return result[i].Branch < result[j].Branch
	})
	return result, nil
}
