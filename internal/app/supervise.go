package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrLeaseHeartbeat      = errors.New("session supervisor lost its writer lease heartbeat")
	ErrSupervisorOwnership = errors.New("session supervisor lost exact supervisor ownership")
)

type supervisorClaimInput struct {
	Identity domain.ProcessIdentity `json:"identity"`
	Observed domain.RuntimeState    `json:"observedState"`
}

type LeaseKeeper interface {
	Renew(context.Context, coordination.LeaseToken, time.Time, time.Duration) (coordination.LeaseToken, error)
}

type Supervise struct {
	journal  ports.Journal
	leases   LeaseKeeper
	locks    operationLocker
	clock    ports.Clock
	ttl      time.Duration
	identity ports.HostIdentity
}

func NewSupervise(journal ports.Journal, leases LeaseKeeper, locks operationLocker, clock ports.Clock, ttl time.Duration, identities ...ports.HostIdentity) *Supervise {
	result := &Supervise{journal: journal, leases: leases, locks: locks, clock: clock, ttl: ttl}
	if len(identities) > 0 {
		result.identity = identities[0]
	}
	return result
}

func (u *Supervise) Run(ctx context.Context, sessionID string) error {
	if u == nil || u.journal == nil || u.clock == nil || u.ttl <= 0 || u.locks == nil {
		return errors.New("supervisor dependencies are incomplete")
	}
	if u.identity != nil {
		if err := u.Claim(ctx, sessionID); err != nil {
			return err
		}
		if err := u.MarkReady(ctx, sessionID); err != nil {
			return err
		}
	}
	return u.RunClaimed(ctx, sessionID)
}

func (u *Supervise) Claim(ctx context.Context, sessionID string) error {
	if u == nil || u.journal == nil || u.clock == nil || u.locks == nil || u.identity == nil {
		return errors.New("supervisor dependencies are incomplete")
	}
	return u.recordSupervisorState(ctx, sessionID, domain.RuntimeObservedPending)
}

func (u *Supervise) MarkReady(ctx context.Context, sessionID string) error {
	if u == nil || u.journal == nil || u.clock == nil || u.locks == nil || u.identity == nil {
		return errors.New("supervisor dependencies are incomplete")
	}
	return u.recordSupervisorState(ctx, sessionID, domain.RuntimeObservedReady)
}

func (u *Supervise) RunClaimed(ctx context.Context, sessionID string) error {
	if u == nil || u.journal == nil || u.clock == nil || u.ttl <= 0 || u.locks == nil || u.identity == nil {
		return errors.New("supervisor dependencies are incomplete")
	}
	snapshot, err := u.loadSnapshot(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := u.verifySupervisorOwnership(ctx, snapshot); err != nil {
		return err
	}
	if snapshot.Mode == domain.SessionReadOnly || snapshot.Lease.Lease == nil {
		<-ctx.Done()
		return nil
	}
	if u.leases == nil || snapshot.Lease.Revision == "" {
		return ErrLeaseHeartbeat
	}
	token := coordination.LeaseToken{Lease: *snapshot.Lease.Lease, Revision: ports.Revision(snapshot.Lease.Revision)}
	interval := u.ttl / 3
	if interval <= 0 {
		interval = time.Second
	}
	ticker := u.clock.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C():
			if err := u.heartbeat(ctx, sessionID, &token, now); err != nil {
				return err
			}
		}
	}
}

func (u *Supervise) claim(ctx context.Context, sessionID string) error {
	return u.recordSupervisorState(ctx, sessionID, domain.RuntimeObservedPending)
}

func (u *Supervise) recordSupervisorState(ctx context.Context, sessionID string, observed domain.RuntimeState) error {
	process, err := u.currentProcess(ctx)
	if err != nil {
		return err
	}
	token, err := u.locks.Acquire(ctx, ports.OperationOwner{SessionID: sessionID, Operation: "supervise"})
	if err != nil {
		return err
	}
	defer func() {
		_ = u.locks.Release(context.WithoutCancel(ctx), token)
	}()
	snapshot, pending, err := u.journal.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if snapshot.SessionID != sessionID {
		return errors.New("supervisor journal returned the wrong session")
	}
	if !activeSessionState(snapshot.State) {
		return ErrNoActiveSession
	}
	snapshot, err = u.reconcileSupervisorClaims(ctx, snapshot, pending)
	if err != nil {
		return err
	}
	if supervisorStateSatisfies(snapshot.Supervisor, process, observed) {
		return nil
	}
	now := u.clock.Now().UTC()
	input, err := json.Marshal(supervisorClaimInput{Identity: process, Observed: observed})
	if err != nil {
		return err
	}
	intent := ports.IntentRecord{ID: sessionID + "-supervisor-claim-" + strconv.FormatInt(now.UnixNano(), 10), SessionID: sessionID, Transition: "SupervisorClaimed", Attempt: 1, Timestamp: now, Input: input}
	if err := u.journal.RecordIntent(ctx, intent); err != nil {
		return err
	}
	snapshot.Supervisor = domain.SupervisorRecord{Identity: process, Desired: domain.RuntimeDesiredRunning, Observed: observed}
	fact := ports.FactRecord{IntentID: intent.ID, SessionID: sessionID, Transition: intent.Transition, Timestamp: now}
	return u.journal.RecordFact(context.WithoutCancel(ctx), fact, snapshot)
}

func (u *Supervise) reconcileSupervisorClaims(ctx context.Context, snapshot domain.JournalSnapshot, pending []ports.PendingIntent) (domain.JournalSnapshot, error) {
	for _, item := range pending {
		intent := item.Intent
		if intent.SessionID != snapshot.SessionID || intent.Transition != "SupervisorClaimed" {
			return snapshot, fmt.Errorf("supervisor claim has unrelated pending transition %q", intent.Transition)
		}
		if len(intent.Input) == 0 {
			fact := ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: u.clock.Now().UTC()}
			if err := u.journal.RecordFact(context.WithoutCancel(ctx), fact, snapshot); err != nil {
				return snapshot, err
			}
			loaded, remaining, err := u.journal.Load(ctx, snapshot.SessionID)
			if err != nil {
				return snapshot, err
			}
			for _, candidate := range remaining {
				if candidate.Intent.ID == intent.ID {
					return snapshot, fmt.Errorf("pending supervisor claim %q remained after fact", intent.ID)
				}
			}
			snapshot = loaded
			continue
		}
		var input supervisorClaimInput
		if err := json.Unmarshal(intent.Input, &input); err != nil {
			return snapshot, fmt.Errorf("decode pending supervisor claim %q: %w", intent.ID, err)
		}
		if err := validateSupervisorClaimInput(input); err != nil {
			return snapshot, fmt.Errorf("pending supervisor claim %q: %w", intent.ID, err)
		}
		snapshot.Supervisor = domain.SupervisorRecord{Identity: input.Identity, Desired: domain.RuntimeDesiredRunning, Observed: input.Observed}
		fact := ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: u.clock.Now().UTC()}
		if err := u.journal.RecordFact(context.WithoutCancel(ctx), fact, snapshot); err != nil {
			return snapshot, err
		}
		loaded, remaining, err := u.journal.Load(ctx, snapshot.SessionID)
		if err != nil {
			return snapshot, err
		}
		for _, candidate := range remaining {
			if candidate.Intent.ID == intent.ID {
				return snapshot, fmt.Errorf("pending supervisor claim %q remained after fact", intent.ID)
			}
		}
		snapshot = loaded
	}
	return snapshot, nil
}

func validateSupervisorClaimInput(input supervisorClaimInput) error {
	if input.Identity.PID <= 0 || input.Identity.BootID == "" || input.Identity.StartTicks == 0 {
		return errors.New("exact process identity is incomplete")
	}
	if input.Observed != domain.RuntimeObservedPending && input.Observed != domain.RuntimeObservedReady {
		return fmt.Errorf("observed state %q is invalid", input.Observed)
	}
	return nil
}

func supervisorStateSatisfies(record domain.SupervisorRecord, identity domain.ProcessIdentity, observed domain.RuntimeState) bool {
	if record.Identity != identity || record.Desired != domain.RuntimeDesiredRunning {
		return false
	}
	return record.Observed == observed || observed == domain.RuntimeObservedPending && record.Observed == domain.RuntimeObservedReady
}

func (u *Supervise) currentProcess(ctx context.Context) (domain.ProcessIdentity, error) {
	identity, err := u.identity.CurrentProcess(ctx)
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	if err := validateSupervisorClaimInput(supervisorClaimInput{Identity: identity, Observed: domain.RuntimeObservedPending}); err != nil {
		return domain.ProcessIdentity{}, err
	}
	return identity, nil
}

func (u *Supervise) verifySupervisorOwnership(ctx context.Context, snapshot domain.JournalSnapshot) error {
	identity, err := u.currentProcess(ctx)
	if err != nil {
		return err
	}
	if snapshot.Supervisor.Identity != identity || snapshot.Supervisor.Desired != domain.RuntimeDesiredRunning || snapshot.Supervisor.Observed != domain.RuntimeObservedReady {
		return fmt.Errorf("%w: durable=%#v current=%#v", ErrSupervisorOwnership, snapshot.Supervisor.Identity, identity)
	}
	return nil
}

func (u *Supervise) loadSnapshot(ctx context.Context, sessionID string) (domain.JournalSnapshot, error) {
	lockToken, err := u.locks.Acquire(ctx, ports.OperationOwner{SessionID: sessionID, Operation: "supervise"})
	if err != nil {
		return domain.JournalSnapshot{}, err
	}
	defer func() {
		_ = u.locks.Release(context.WithoutCancel(ctx), lockToken)
	}()
	snapshot, _, err := u.journal.Load(ctx, sessionID)
	if err != nil {
		return domain.JournalSnapshot{}, err
	}
	if snapshot.SessionID != sessionID {
		return domain.JournalSnapshot{}, errors.New("supervisor journal returned the wrong session")
	}
	return snapshot, nil
}

func (u *Supervise) heartbeat(ctx context.Context, sessionID string, token *coordination.LeaseToken, now time.Time) error {
	lockToken, err := u.locks.Acquire(ctx, ports.OperationOwner{SessionID: sessionID, Operation: "supervise-heartbeat"})
	if err != nil {
		return err
	}
	defer func() {
		_ = u.locks.Release(context.WithoutCancel(ctx), lockToken)
	}()
	snapshot, _, err := u.journal.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if snapshot.SessionID != sessionID {
		return errors.New("supervisor journal returned the wrong session")
	}
	if err := u.verifySupervisorOwnership(ctx, snapshot); err != nil {
		return err
	}
	if snapshot.Mode == domain.SessionReadOnly || snapshot.Lease.Lease == nil || snapshot.Lease.Revision == "" {
		return ErrLeaseHeartbeat
	}
	if snapshot.Lease.Lease.SessionID != sessionID {
		return ErrLeaseHeartbeat
	}
	if now.IsZero() {
		now = u.clock.Now()
	}
	input, _ := json.Marshal(struct {
		Revision string    `json:"revision"`
		Now      time.Time `json:"now"`
	}{Revision: string(token.Revision), Now: now.UTC()})
	intent := ports.IntentRecord{
		ID: sessionID + "-lease-renew-" + strconv.FormatInt(now.UnixNano(), 10), SessionID: sessionID,
		Transition: "LeaseRenewed", Attempt: 1, Timestamp: now.UTC(), Input: input,
	}
	if err := u.journal.RecordIntent(ctx, intent); err != nil {
		return err
	}
	*token = coordination.LeaseToken{Lease: *snapshot.Lease.Lease, Revision: ports.Revision(snapshot.Lease.Revision)}
	renewed, err := u.leases.Renew(ctx, *token, now, u.ttl)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLeaseHeartbeat, err)
	}
	snapshot.Lease = domain.LeaseRecord{Lease: &renewed.Lease, Revision: string(renewed.Revision)}
	fact := ports.FactRecord{IntentID: intent.ID, SessionID: sessionID, Transition: intent.Transition, Timestamp: now.UTC()}
	if err := u.journal.RecordFact(context.WithoutCancel(ctx), fact, snapshot); err != nil {
		return err
	}
	*token = renewed
	return nil
}
