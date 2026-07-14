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

var ErrLeaseHeartbeat = errors.New("session supervisor lost its writer lease heartbeat")

type LeaseKeeper interface {
	Renew(context.Context, coordination.LeaseToken, time.Time, time.Duration) (coordination.LeaseToken, error)
}

type Supervise struct {
	journal  ports.Journal
	leases   LeaseKeeper
	clock    ports.Clock
	ttl      time.Duration
	identity ports.HostIdentity
}

func NewSupervise(journal ports.Journal, leases LeaseKeeper, clock ports.Clock, ttl time.Duration, identities ...ports.HostIdentity) *Supervise {
	result := &Supervise{journal: journal, leases: leases, clock: clock, ttl: ttl}
	if len(identities) > 0 {
		result.identity = identities[0]
	}
	return result
}

func (u *Supervise) Run(ctx context.Context, sessionID string) error {
	if u == nil || u.journal == nil || u.clock == nil || u.ttl <= 0 {
		return errors.New("supervisor dependencies are incomplete")
	}
	snapshot, _, err := u.journal.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if u.identity != nil {
		now := u.clock.Now().UTC()
		intent := ports.IntentRecord{ID: sessionID + "-supervisor-claim-" + strconv.FormatInt(now.UnixNano(), 10), SessionID: sessionID, Transition: "SupervisorClaimed", Attempt: 1, Timestamp: now}
		if err := u.journal.RecordIntent(ctx, intent); err != nil {
			return err
		}
		process, err := u.identity.CurrentProcess(ctx)
		if err != nil {
			return err
		}
		snapshot.Supervisor = domain.SupervisorRecord{Identity: process, Desired: domain.RuntimeDesiredRunning, Observed: domain.RuntimeObservedReady}
		fact := ports.FactRecord{IntentID: intent.ID, SessionID: sessionID, Transition: intent.Transition, Timestamp: now}
		if err := u.journal.RecordFact(context.WithoutCancel(ctx), fact, snapshot); err != nil {
			return err
		}
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
			renewed, err := u.leases.Renew(ctx, token, now, u.ttl)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrLeaseHeartbeat, err)
			}
			snapshot.Lease = domain.LeaseRecord{Lease: &renewed.Lease, Revision: string(renewed.Revision)}
			fact := ports.FactRecord{IntentID: intent.ID, SessionID: sessionID, Transition: intent.Transition, Timestamp: now.UTC()}
			if err := u.journal.RecordFact(context.WithoutCancel(ctx), fact, snapshot); err != nil {
				return err
			}
			token = renewed
		}
	}
}
