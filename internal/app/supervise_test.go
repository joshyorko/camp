package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

type controlledTicker struct{ channel chan time.Time }

func (t *controlledTicker) C() <-chan time.Time { return t.channel }
func (t *controlledTicker) Stop()               {}

type controlledClock struct {
	now    time.Time
	ticker *controlledTicker
}

func (c *controlledClock) Now() time.Time                       { return c.now }
func (c *controlledClock) NewTicker(time.Duration) ports.Ticker { return c.ticker }

type fakeLeaseKeeper struct {
	renewed chan coordination.LeaseToken
	next    coordination.LeaseToken
	err     error
}

type fakeHostIdentity struct{ process domain.ProcessIdentity }

func (f fakeHostIdentity) MachineID(context.Context) (string, error) { return "machine", nil }
func (f fakeHostIdentity) CurrentProcess(context.Context) (domain.ProcessIdentity, error) {
	return f.process, nil
}

func (k *fakeLeaseKeeper) Renew(_ context.Context, token coordination.LeaseToken, _ time.Time, _ time.Duration) (coordination.LeaseToken, error) {
	k.renewed <- token
	return k.next, k.err
}

func TestSuperviseOwnsLeaseHeartbeatAndPersistsExactRenewedToken(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, _ := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, SessionID: "session-a", Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Minute)}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Mode: domain.SessionReadWrite, Lease: domain.LeaseRecord{Lease: &lease, Revision: "r1"}}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	nextLease := lease
	nextLease.HeartbeatAt = now.Add(20 * time.Second)
	nextLease.ExpiresAt = now.Add(80 * time.Second)
	keeper := &fakeLeaseKeeper{renewed: make(chan coordination.LeaseToken, 1), next: coordination.LeaseToken{Lease: nextLease, Revision: "r2"}}
	ticker := &controlledTicker{channel: make(chan time.Time, 1)}
	clock := &controlledClock{now: nextLease.HeartbeatAt, ticker: ticker}
	supervisorIdentity := domain.ProcessIdentity{PID: 900, BootID: "boot", StartTicks: 44}
	supervisor := NewSupervise(log, keeper, clock, time.Minute, fakeHostIdentity{process: supervisorIdentity})
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, snapshot.SessionID) }()
	ticker.channel <- clock.now
	select {
	case token := <-keeper.renewed:
		if token.Revision != "r1" {
			t.Fatalf("renew token = %#v", token)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not run")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() after cancel error = %v", err)
	}
	loaded, pending, err := log.Load(context.Background(), snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.Lease.Revision != "r2" || loaded.Lease.Lease.HeartbeatAt != nextLease.HeartbeatAt || loaded.Supervisor.Identity != supervisorIdentity || loaded.Supervisor.Observed != domain.RuntimeObservedReady {
		t.Fatalf("loaded = %#v pending=%#v error=%v", loaded.Lease, pending, err)
	}
}

func TestSuperviseReturnsTypedHeartbeatLoss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log, _ := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SessionID: "session-a", HeartbeatAt: now, ExpiresAt: now.Add(time.Minute)}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Mode: domain.SessionReadWrite, Lease: domain.LeaseRecord{Lease: &lease, Revision: "r1"}}
	_ = log.Create(ctx, snapshot)
	keeper := &fakeLeaseKeeper{renewed: make(chan coordination.LeaseToken, 1), err: errors.New("lost")}
	ticker := &controlledTicker{channel: make(chan time.Time, 1)}
	clock := &controlledClock{now: now.Add(20 * time.Second), ticker: ticker}
	ticker.channel <- clock.now
	err := NewSupervise(log, keeper, clock, time.Minute).Run(ctx, snapshot.SessionID)
	if !errors.Is(err, ErrLeaseHeartbeat) {
		t.Fatalf("Run() error = %v, want ErrLeaseHeartbeat", err)
	}
}
