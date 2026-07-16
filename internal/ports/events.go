package ports

import (
	"context"
	"time"
)

type Event struct {
	Time      time.Time
	Kind      string
	Message   string
	Fields    map[string]string
	Sensitive []string
}

type EventSink interface {
	Emit(ctx context.Context, event Event) error
}
