package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

type IntentRecord struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"sessionId"`
	Transition string          `json:"transition"`
	Attempt    int             `json:"attempt"`
	Timestamp  time.Time       `json:"timestamp"`
	Input      json.RawMessage `json:"input,omitempty"`
}

type PointerCommit struct {
	Pointer  domain.LatestPointer `json:"pointer"`
	Revision string               `json:"revision"`
}

type FactRecord struct {
	IntentID   string          `json:"intentId"`
	SessionID  string          `json:"sessionId"`
	Transition string          `json:"transition"`
	Timestamp  time.Time       `json:"timestamp"`
	Output     json.RawMessage `json:"output,omitempty"`
	Pointer    *PointerCommit  `json:"pointer,omitempty"`
}

type PendingIntent struct {
	Intent IntentRecord
}

type Journal interface {
	Create(context.Context, domain.JournalSnapshot) error
	RecordIntent(context.Context, IntentRecord) error
	RecordFact(context.Context, FactRecord, domain.JournalSnapshot) error
	Load(context.Context, string) (domain.JournalSnapshot, []PendingIntent, error)
	List(context.Context) ([]domain.JournalSnapshot, error)
}
