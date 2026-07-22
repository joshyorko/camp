package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
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
	events  *heartbeatEventLog
}

type fakeHostIdentity struct{ process domain.ProcessIdentity }

func (f fakeHostIdentity) MachineID(context.Context) (string, error) { return "machine", nil }
func (f fakeHostIdentity) CurrentProcess(context.Context) (domain.ProcessIdentity, error) {
	return f.process, nil
}

func (k *fakeLeaseKeeper) Renew(_ context.Context, token coordination.LeaseToken, _ time.Time, _ time.Duration) (coordination.LeaseToken, error) {
	if k.events != nil {
		k.events.append("renew")
	}
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
	supervisorIdentity := domain.ProcessIdentity{PID: 900, BootID: "boot", StartTicks: 44}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Mode: domain.SessionReadWrite, Lease: domain.LeaseRecord{Lease: &lease, Revision: "r1"}, Supervisor: domain.SupervisorRecord{Identity: supervisorIdentity, Desired: domain.RuntimeDesiredRunning, Observed: domain.RuntimeObservedReady}}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	nextLease := lease
	nextLease.HeartbeatAt = now.Add(20 * time.Second)
	nextLease.ExpiresAt = now.Add(80 * time.Second)
	keeper := &fakeLeaseKeeper{renewed: make(chan coordination.LeaseToken, 1), next: coordination.LeaseToken{Lease: nextLease, Revision: "r2"}}
	ticker := &controlledTicker{channel: make(chan time.Time, 1)}
	clock := &controlledClock{now: nextLease.HeartbeatAt, ticker: ticker}
	supervisor := NewSupervise(log, keeper, &recordingOperationLocker{}, clock, time.Minute, fakeHostIdentity{process: supervisorIdentity})
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
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Mode: domain.SessionReadWrite, Lease: domain.LeaseRecord{Lease: &lease, Revision: "r1"}, Supervisor: domain.SupervisorRecord{Desired: domain.RuntimeDesiredRunning, Observed: domain.RuntimeObservedReady}}
	_ = log.Create(ctx, snapshot)
	keeper := &fakeLeaseKeeper{renewed: make(chan coordination.LeaseToken, 1), err: errors.New("lost")}
	ticker := &controlledTicker{channel: make(chan time.Time, 1)}
	clock := &controlledClock{now: now.Add(20 * time.Second), ticker: ticker}
	ticker.channel <- clock.now
	err := NewSupervise(log, keeper, &recordingOperationLocker{}, clock, time.Minute).Run(ctx, snapshot.SessionID)
	if !errors.Is(err, ErrLeaseHeartbeat) {
		t.Fatalf("Run() error = %v, want ErrLeaseHeartbeat", err)
	}
}

func TestSuperviseReloadsDurableSnapshotUnderOperationLock(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := &heartbeatEventLog{}

	log := &recordingSupervisorJournal{
		events: events,
		snapshot: domain.JournalSnapshot{
			SchemaVersion: domain.SchemaVersion,
			SessionID:     "session-a",
			Mode:          domain.SessionReadWrite,
			Supervisor:    domain.SupervisorRecord{Desired: domain.RuntimeDesiredRunning, Observed: domain.RuntimeObservedReady},
			Lease:         domain.LeaseRecord{Lease: &domain.WriterLease{SessionID: "session-a", HeartbeatAt: time.Unix(100, 0), ExpiresAt: time.Unix(160, 0)}, Revision: "r1"},
		},
	}
	keeper := &fakeLeaseKeeper{renewed: make(chan coordination.LeaseToken, 2), next: coordination.LeaseToken{Lease: domain.WriterLease{SessionID: "session-a", HeartbeatAt: time.Unix(120, 0), ExpiresAt: time.Unix(180, 0)}, Revision: "r2"}, events: events}
	ticker := &controlledTicker{channel: make(chan time.Time, 2)}
	clock := &controlledClock{now: time.Unix(120, 0).UTC(), ticker: ticker}
	locker := &recordingOperationLocker{events: events}
	supervisor := NewSupervise(log, keeper, locker, clock, time.Minute)

	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "session-a") }()

	ticker.channel <- clock.now
	select {
	case <-keeper.renewed:
	case <-time.After(time.Second):
		t.Fatal("first heartbeat did not renew")
	}
	if got := events.snapshot(); !containsAllInOrder(got, []string{"acquire", "load", "acquire", "load", "renew", "fact", "release"}) {
		t.Fatalf("events after first heartbeat = %#v", got)
	}

	log.mutate(func(snapshot *domain.JournalSnapshot) {
		snapshot.Checkpoint = domain.Checkpoint{State: domain.CheckpointPublished, PublicationSucceeded: true, Generation: &domain.GenerationRef{Generation: 9}}
		snapshot.Services = []domain.ServiceUnitRecord{{Name: "registry", DesiredState: domain.RuntimeDesiredRunning}}
		snapshot.Cleanup = domain.Cleanup{State: domain.CleanupSucceeded}
	})

	ticker.channel <- clock.now.Add(time.Minute)
	select {
	case <-keeper.renewed:
	case <-time.After(time.Second):
		t.Fatal("second heartbeat did not renew")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() after cancel error = %v", err)
	}

	if got := log.recordedSnapshots(); len(got) < 2 || got[len(got)-1].Checkpoint.Generation == nil || got[len(got)-1].Checkpoint.Generation.Generation != 9 || len(got[len(got)-1].Services) != 1 || got[len(got)-1].Cleanup.State != domain.CleanupSucceeded {
		t.Fatalf("recorded snapshots = %#v", got)
	}
}

func TestSuperviseReleasesLockWhenCancelledDuringHeartbeat(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := &heartbeatEventLog{}

	log := &blockingSupervisorJournal{
		snapshot: domain.JournalSnapshot{
			SchemaVersion: domain.SchemaVersion,
			SessionID:     "session-b",
			Mode:          domain.SessionReadWrite,
			Supervisor:    domain.SupervisorRecord{Desired: domain.RuntimeDesiredRunning, Observed: domain.RuntimeObservedReady},
			Lease:         domain.LeaseRecord{Lease: &domain.WriterLease{SessionID: "session-b", HeartbeatAt: time.Unix(100, 0), ExpiresAt: time.Unix(160, 0)}, Revision: "r1"},
		},
		factBlock: make(chan struct{}),
	}
	keeper := &fakeLeaseKeeper{renewed: make(chan coordination.LeaseToken, 1), next: coordination.LeaseToken{Lease: domain.WriterLease{SessionID: "session-b", HeartbeatAt: time.Unix(120, 0), ExpiresAt: time.Unix(180, 0)}, Revision: "r2"}, events: events}
	ticker := &controlledTicker{channel: make(chan time.Time, 1)}
	clock := &controlledClock{now: time.Unix(120, 0).UTC(), ticker: ticker}
	locker := &recordingOperationLocker{events: events}
	supervisor := NewSupervise(log, keeper, locker, clock, time.Minute)

	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "session-b") }()

	ticker.channel <- clock.now
	select {
	case <-keeper.renewed:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not reach renew")
	}
	cancel()
	close(log.factBlock)
	if err := <-done; err != nil {
		t.Fatalf("Run() after cancel error = %v", err)
	}
	if got := events.snapshot(); countEvent(got, "release") < 2 {
		t.Fatalf("events = %#v", got)
	}
}

func TestSuperviseReleasesInitialLoadLockBeforeHeartbeat(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	locker := &strictOperationLocker{}
	events := &heartbeatEventLog{}
	log := &recordingSupervisorJournal{
		events: events,
		snapshot: domain.JournalSnapshot{
			SchemaVersion: domain.SchemaVersion,
			SessionID:     "session-c",
			Mode:          domain.SessionReadWrite,
			Supervisor:    domain.SupervisorRecord{Desired: domain.RuntimeDesiredRunning, Observed: domain.RuntimeObservedReady},
			Lease:         domain.LeaseRecord{Lease: &domain.WriterLease{SessionID: "session-c", HeartbeatAt: time.Unix(100, 0), ExpiresAt: time.Unix(160, 0)}, Revision: "r1"},
		},
	}
	keeper := &fakeLeaseKeeper{renewed: make(chan coordination.LeaseToken, 1), next: coordination.LeaseToken{Lease: domain.WriterLease{SessionID: "session-c", HeartbeatAt: time.Unix(120, 0), ExpiresAt: time.Unix(180, 0)}, Revision: "r2"}, events: events}
	ticker := &controlledTicker{channel: make(chan time.Time, 1)}
	clock := &controlledClock{now: time.Unix(120, 0).UTC(), ticker: ticker}
	supervisor := NewSupervise(log, keeper, locker, clock, time.Minute)

	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "session-c") }()

	if !locker.waitForSequence(t, []string{"acquire", "release"}) {
		t.Fatal("initial load did not release its lock")
	}
	ticker.channel <- clock.now
	select {
	case <-keeper.renewed:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not renew")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() after cancel error = %v", err)
	}
	if got := locker.events(); !containsAllInOrder(got, []string{"acquire", "release", "acquire", "release"}) {
		t.Fatalf("events = %#v", got)
	}
}

func containsAllInOrder(values, want []string) bool {
	index := 0
	for _, value := range values {
		if index < len(want) && value == want[index] {
			index++
		}
	}
	return index == len(want)
}

func countEvent(values []string, want string) int {
	total := 0
	for _, value := range values {
		if value == want {
			total++
		}
	}
	return total
}

type recordingOperationLocker struct {
	events *heartbeatEventLog
}

func (l *recordingOperationLocker) Acquire(_ context.Context, owner ports.OperationOwner) (ports.OperationToken, error) {
	if l.events != nil {
		l.events.append("acquire")
	}
	return ports.OperationToken{ID: "token", Owner: owner, Identity: domain.ProcessIdentity{PID: 1, BootID: "boot", StartTicks: 1}}, nil
}

func (l *recordingOperationLocker) Release(_ context.Context, _ ports.OperationToken) error {
	if l.events != nil {
		l.events.append("release")
	}
	return nil
}

type strictOperationLocker struct {
	mu   sync.Mutex
	held bool
	log  heartbeatEventLog
}

func (l *strictOperationLocker) Acquire(_ context.Context, _ ports.OperationOwner) (ports.OperationToken, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return ports.OperationToken{}, errors.New("reentrant lock acquisition")
	}
	l.held = true
	l.log.append("acquire")
	return ports.OperationToken{ID: "token", Owner: ports.OperationOwner{SessionID: "session-c", Operation: "supervise"}, Identity: domain.ProcessIdentity{PID: 1, BootID: "boot", StartTicks: 1}}, nil
}

func (l *strictOperationLocker) Release(_ context.Context, _ ports.OperationToken) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return errors.New("release without acquire")
	}
	l.held = false
	l.log.append("release")
	return nil
}

func (l *strictOperationLocker) events() []string { return l.log.snapshot() }

func (l *strictOperationLocker) waitForSequence(t *testing.T, want []string) bool {
	deadline := time.After(time.Second)
	for {
		if containsAllInOrder(l.log.snapshot(), want) {
			return true
		}
		select {
		case <-deadline:
			t.Logf("events so far: %#v", l.log.snapshot())
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type recordingSupervisorJournal struct {
	mu       sync.Mutex
	events   *heartbeatEventLog
	snapshot domain.JournalSnapshot
	loads    int
	facts    []domain.JournalSnapshot
}

func (j *recordingSupervisorJournal) Create(context.Context, domain.JournalSnapshot) error {
	return nil
}
func (j *recordingSupervisorJournal) RecordIntent(context.Context, ports.IntentRecord) error {
	return nil
}
func (j *recordingSupervisorJournal) RecordFact(_ context.Context, _ ports.FactRecord, snapshot domain.JournalSnapshot) error {
	if j.events != nil {
		j.events.append("fact")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.facts = append(j.facts, snapshot)
	return nil
}
func (j *recordingSupervisorJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	if j.events != nil {
		j.events.append("load")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.loads++
	return j.snapshot, nil, nil
}
func (j *recordingSupervisorJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	return nil, nil
}

func (j *recordingSupervisorJournal) mutate(fn func(*domain.JournalSnapshot)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.snapshot)
}

func (j *recordingSupervisorJournal) recordedSnapshots() []domain.JournalSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]domain.JournalSnapshot(nil), j.facts...)
}

type blockingSupervisorJournal struct {
	mu        sync.Mutex
	events    *heartbeatEventLog
	snapshot  domain.JournalSnapshot
	factBlock chan struct{}
}

func (j *blockingSupervisorJournal) Create(context.Context, domain.JournalSnapshot) error { return nil }
func (j *blockingSupervisorJournal) RecordIntent(context.Context, ports.IntentRecord) error {
	return nil
}
func (j *blockingSupervisorJournal) RecordFact(_ context.Context, _ ports.FactRecord, snapshot domain.JournalSnapshot) error {
	if j.events != nil {
		j.events.append("fact")
	}
	<-j.factBlock
	j.mu.Lock()
	j.snapshot = snapshot
	j.mu.Unlock()
	return nil
}
func (j *blockingSupervisorJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	if j.events != nil {
		j.events.append("load")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshot, nil, nil
}
func (j *blockingSupervisorJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	return nil, nil
}

type heartbeatEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *heartbeatEventLog) append(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *heartbeatEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}
