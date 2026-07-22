package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

func TestSupervisorClaimReconcilesCrashedClaimantBeforeReplacement(t *testing.T) {
	t.Parallel()

	store, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session-claim-crash"
	if err := store.Create(context.Background(), domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     sessionID,
		Mode:          domain.SessionReadWrite,
	}); err != nil {
		t.Fatal(err)
	}

	failedFact := errors.New("injected fact append failure")
	log := &claimFaultJournal{Journal: store, failBeforeFact: failedFact}
	clock := &controlledClock{now: time.Unix(100, 0).UTC(), ticker: &controlledTicker{channel: make(chan time.Time)}}
	firstIdentity := domain.ProcessIdentity{PID: 101, BootID: "boot-a", StartTicks: 11}
	first := NewSupervise(log, nil, &recordingOperationLocker{}, clock, time.Minute, fakeHostIdentity{process: firstIdentity})
	if err := first.Claim(context.Background(), sessionID); !errors.Is(err, failedFact) {
		t.Fatalf("first Claim() error = %v, want injected failure", err)
	}
	_, pending, err := store.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 1 || pending[0].Intent.Transition != "SupervisorClaimed" || !strings.Contains(string(pending[0].Intent.Input), `"pid":101`) {
		t.Fatalf("pending after failed fact = %#v error=%v", pending, err)
	}

	clock.now = time.Unix(101, 0).UTC()
	replacementIdentity := domain.ProcessIdentity{PID: 202, BootID: "boot-b", StartTicks: 22}
	replacement := NewSupervise(log, nil, &recordingOperationLocker{}, clock, time.Minute, fakeHostIdentity{process: replacementIdentity})
	if err := replacement.Claim(context.Background(), sessionID); err != nil {
		t.Fatalf("replacement Claim() error = %v", err)
	}
	loaded, pending, err := store.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("load after replacement = %#v pending=%#v error=%v", loaded, pending, err)
	}
	if loaded.Supervisor.Identity != replacementIdentity || loaded.Supervisor.Observed != domain.RuntimeObservedPending {
		t.Fatalf("supervisor after replacement = %#v", loaded.Supervisor)
	}
	if log.intentCalls != 2 {
		t.Fatalf("RecordIntent calls = %d, want one per exact claimant", log.intentCalls)
	}
}

func TestSupervisorClaimAcceptsDurableFactAfterLostResponseWithoutDuplicateIntent(t *testing.T) {
	t.Parallel()

	store, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session-claim-response"
	if err := store.Create(context.Background(), domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: sessionID, Mode: domain.SessionReadWrite}); err != nil {
		t.Fatal(err)
	}

	lostResponse := errors.New("injected response loss")
	log := &claimFaultJournal{Journal: store, failAfterFact: lostResponse}
	identity := domain.ProcessIdentity{PID: 303, BootID: "boot-c", StartTicks: 33}
	clock := &controlledClock{now: time.Unix(200, 0).UTC(), ticker: &controlledTicker{channel: make(chan time.Time)}}
	supervise := NewSupervise(log, nil, &recordingOperationLocker{}, clock, time.Minute, fakeHostIdentity{process: identity})
	if err := supervise.Claim(context.Background(), sessionID); !errors.Is(err, lostResponse) {
		t.Fatalf("first Claim() error = %v, want injected response loss", err)
	}
	if err := supervise.Claim(context.Background(), sessionID); err != nil {
		t.Fatalf("idempotent Claim() error = %v", err)
	}
	loaded, pending, err := store.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 0 || loaded.Supervisor.Identity != identity || loaded.Supervisor.Observed != domain.RuntimeObservedPending {
		t.Fatalf("loaded = %#v pending=%#v error=%v", loaded, pending, err)
	}
	if log.intentCalls != 1 {
		t.Fatalf("RecordIntent calls = %d, want 1", log.intentCalls)
	}
}

func TestSupervisorHeartbeatReturnsOwnershipLossSoReplacedOwnerExitsBeforeLeaseSideEffect(t *testing.T) {
	t.Parallel()

	store, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session-owner-replaced"
	now := time.Unix(300, 0).UTC()
	lease := domain.WriterLease{SessionID: sessionID, HeartbeatAt: now, ExpiresAt: now.Add(time.Minute)}
	oldIdentity := domain.ProcessIdentity{PID: 404, BootID: "boot-d", StartTicks: 44}
	newIdentity := domain.ProcessIdentity{PID: 505, BootID: "boot-e", StartTicks: 55}
	if err := store.Create(context.Background(), domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     sessionID,
		Mode:          domain.SessionReadWrite,
		Lease:         domain.LeaseRecord{Lease: &lease, Revision: "r1"},
		Supervisor:    domain.SupervisorRecord{Identity: newIdentity, Desired: domain.RuntimeDesiredRunning, Observed: domain.RuntimeObservedReady},
	}); err != nil {
		t.Fatal(err)
	}

	keeper := &countingLeaseKeeper{}
	clock := &controlledClock{now: now.Add(20 * time.Second), ticker: &controlledTicker{channel: make(chan time.Time)}}
	supervise := NewSupervise(store, keeper, &recordingOperationLocker{}, clock, time.Minute, fakeHostIdentity{process: oldIdentity})
	token := coordination.LeaseToken{Lease: lease, Revision: "r1"}
	err = supervise.heartbeat(context.Background(), sessionID, &token, clock.now)
	if !errors.Is(err, ErrSupervisorOwnership) {
		t.Fatalf("heartbeat() error = %v, want supervisor ownership loss", err)
	}
	if keeper.calls != 0 {
		t.Fatalf("lease Renew calls = %d, want 0", keeper.calls)
	}
	_, pending, loadErr := store.Load(context.Background(), sessionID)
	if loadErr != nil || len(pending) != 0 {
		t.Fatalf("pending after rejected heartbeat = %#v error=%v", pending, loadErr)
	}
}

type claimFaultJournal struct {
	ports.Journal
	failBeforeFact error
	failAfterFact  error
	intentCalls    int
}

func (j *claimFaultJournal) RecordIntent(ctx context.Context, intent ports.IntentRecord) error {
	j.intentCalls++
	return j.Journal.RecordIntent(ctx, intent)
}

func (j *claimFaultJournal) RecordFact(ctx context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	if j.failBeforeFact != nil {
		err := j.failBeforeFact
		j.failBeforeFact = nil
		return err
	}
	if err := j.Journal.RecordFact(ctx, fact, snapshot); err != nil {
		return err
	}
	if j.failAfterFact != nil {
		err := j.failAfterFact
		j.failAfterFact = nil
		return err
	}
	return nil
}

type countingLeaseKeeper struct{ calls int }

func (k *countingLeaseKeeper) Renew(_ context.Context, token coordination.LeaseToken, now time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	k.calls++
	token.Lease.HeartbeatAt = now
	token.Lease.ExpiresAt = now.Add(ttl)
	token.Revision = ports.Revision("r-next")
	return token, nil
}
