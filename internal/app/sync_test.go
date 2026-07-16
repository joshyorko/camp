package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type fakeSyncSessionReader struct {
	snapshot domain.JournalSnapshot
	pending  []ports.PendingIntent
	err      error
	calls    int
}

func (r *fakeSyncSessionReader) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	r.calls++
	return r.snapshot, r.pending, r.err
}

type fakeOperationLocker struct {
	events     *[]string
	token      ports.OperationToken
	acquireErr error
	releaseErr error
}

func (l *fakeOperationLocker) Acquire(_ context.Context, owner ports.OperationOwner) (ports.OperationToken, error) {
	*l.events = append(*l.events, "lock:"+owner.Operation)
	l.token.Owner = owner
	return l.token, l.acquireErr
}

func (l *fakeOperationLocker) Release(_ context.Context, token ports.OperationToken) error {
	*l.events = append(*l.events, "unlock:"+token.Owner.Operation)
	return l.releaseErr
}

type fakeCheckpointPublisher struct {
	events *[]string
	result CheckpointResult
	err    error
}

func (p *fakeCheckpointPublisher) Publish(_ context.Context, token ports.OperationToken, sessionID string) (CheckpointResult, error) {
	*p.events = append(*p.events, "publish:"+sessionID+":"+token.Owner.Operation)
	return p.result, p.err
}

func TestSyncOwnsOneOperationLockAcrossPublisherAndAlwaysReleases(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		publishErr error
		wantErr    bool
	}{
		{"success", nil, false},
		{"publisher failure", errors.New("publish failed"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			locker := &fakeOperationLocker{events: &events, token: ports.OperationToken{ID: "lock"}}
			publisher := &fakeCheckpointPublisher{events: &events, result: CheckpointResult{Published: test.publishErr == nil}, err: test.publishErr}
			result, err := NewSync(&fakeSyncSessionReader{snapshot: domain.JournalSnapshot{SessionID: "session-a", Mode: domain.SessionReadWrite}}, locker, publisher).Run(context.Background(), "session-a")
			if (err != nil) != test.wantErr {
				t.Fatalf("Run() = %#v, %v", result, err)
			}
			want := []string{"lock:sync", "publish:session-a:sync", "unlock:sync"}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v, want %#v", events, want)
			}
		})
	}
}

func TestSyncReadonlyIsTypedNoopWithZeroMutations(t *testing.T) {
	t.Parallel()
	events := []string{}
	reader := &fakeSyncSessionReader{snapshot: domain.JournalSnapshot{SessionID: "session-readonly", Mode: domain.SessionReadOnly, State: domain.SessionOpen}}
	result, err := NewSync(
		reader,
		&fakeOperationLocker{events: &events, token: ports.OperationToken{ID: "lock"}},
		&fakeCheckpointPublisher{events: &events, err: errors.New("publisher must not run")},
	).Run(context.Background(), "session-readonly")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Disposition != CheckpointDispositionSkippedReadOnly || result.Published {
		t.Fatalf("Run() result = %#v", result)
	}
	if reader.calls != 1 || len(events) != 0 {
		t.Fatalf("reader calls=%d mutation events=%#v, want one read and zero mutations", reader.calls, events)
	}
}
