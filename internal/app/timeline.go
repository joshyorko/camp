package app

import (
	"context"
	"sort"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

type BlueprintState string

const (
	BlueprintKnown   BlueprintState = "known"
	BlueprintUnknown BlueprintState = "unknown-blueprint"
)

type ExecutionBindingReader interface {
	Binding(context.Context, string) (domain.ExecutionBinding, bool, error)
}
type TimelineEntry struct {
	SessionID string                   `json:"sessionId"`
	Capsule   string                   `json:"capsule"`
	Branch    string                   `json:"branch"`
	State     string                   `json:"state"`
	UpdatedAt time.Time                `json:"updatedAt"`
	Blueprint BlueprintState           `json:"blueprint"`
	Binding   *domain.ExecutionBinding `json:"binding,omitempty"`
}
type Timeline struct {
	sessions sessionLister
	bindings ExecutionBindingReader
}

func NewTimeline(sessions sessionLister, bindings ExecutionBindingReader) *Timeline {
	return &Timeline{sessions: sessions, bindings: bindings}
}
func (t *Timeline) List(ctx context.Context) ([]TimelineEntry, error) {
	snapshots, err := t.sessions.List(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]TimelineEntry, 0, len(snapshots))
	for _, snapshot := range snapshots {
		entry := TimelineEntry{SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, State: string(snapshot.State), UpdatedAt: snapshot.UpdatedAt, Blueprint: BlueprintUnknown}
		if t.bindings != nil {
			binding, found, err := t.bindings.Binding(ctx, snapshot.SessionID)
			if err != nil {
				return nil, err
			}
			if found {
				entry.Blueprint = BlueprintKnown
				entry.Binding = &binding
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].SessionID < entries[j].SessionID
		}
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
	return entries, nil
}
