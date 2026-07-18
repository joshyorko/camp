package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrRecoveryIdentityChanged    = errors.New("session identity changed during recovery")
	ErrRecoveryObservationChanged = errors.New("session observation changed during recovery")
	ErrRecoveryNotRequired        = errors.New("session does not require supported recovery")
)

type RecoveryAction string

const (
	RecoveryActionLifecycle RecoveryAction = "lifecycle"
	RecoveryActionCleanup   RecoveryAction = "cleanup"
)

type recoveryGuard interface {
	// Revalidate checks ownership, session identity, the writer lease when
	// applicable, and pending-transition preconditions immediately before the
	// selected reconciler may perform effects.
	Revalidate(context.Context, domain.JournalSnapshot, []ports.PendingIntent) error
}

type recoveryOwnership interface {
	Revalidate(domain.Materialization) error
}

type recoveryLeaseValidator interface {
	Revalidate(context.Context, coordination.LeaseToken, time.Time) error
}

type RecoverySafetyGuard struct {
	ownership recoveryOwnership
	leases    recoveryLeaseValidator
	clock     ports.Clock
}

func NewRecoverySafetyGuard(ownership recoveryOwnership, leases recoveryLeaseValidator, clock ports.Clock) *RecoverySafetyGuard {
	return &RecoverySafetyGuard{ownership: ownership, leases: leases, clock: clock}
}

func (g *RecoverySafetyGuard) Revalidate(ctx context.Context, snapshot domain.JournalSnapshot, pending []ports.PendingIntent) error {
	if g == nil || g.ownership == nil || g.clock == nil {
		return errors.New("recovery safety dependencies are incomplete")
	}
	if snapshot.SchemaVersion != domain.SchemaVersion || snapshot.SessionID == "" || snapshot.Capsule == "" || snapshot.Lineage.Branch == "" {
		return errors.New("recovery session identity is incomplete")
	}
	for _, item := range pending {
		if item.Intent.SessionID != snapshot.SessionID || item.Intent.ID == "" || item.Intent.Transition == "" {
			return errors.New("pending recovery transition does not match the session")
		}
	}
	if err := g.ownership.Revalidate(snapshot.Materialization); err != nil {
		return fmt.Errorf("revalidate recovery materialization: %w", err)
	}
	if snapshot.Mode == domain.SessionReadOnly {
		return nil
	}
	if g.leases == nil || snapshot.Lease.Lease == nil || snapshot.Lease.Revision == "" {
		return errors.New("recovery requires an active writer lease")
	}
	lease := snapshot.Lease.Lease
	if lease.SchemaVersion != domain.SchemaVersion || lease.SessionID != snapshot.SessionID || lease.Capsule != snapshot.Capsule || lease.Lineage != snapshot.Lineage || !sameGeneration(lease.OpenedGeneration, snapshot.OpenedGeneration) {
		return errors.New("recovery writer lease does not match the session")
	}
	token := coordination.LeaseToken{Lease: *lease, Revision: ports.Revision(snapshot.Lease.Revision)}
	if err := g.leases.Revalidate(ctx, token, g.clock.Now().UTC()); err != nil {
		return fmt.Errorf("revalidate recovery writer lease: %w", err)
	}
	return nil
}

type recoveryReconciler interface {
	Reconcile(context.Context, string) (domain.JournalSnapshot, error)
}

type Recover struct {
	journal   ports.Journal
	observer  sessionObserver
	guard     recoveryGuard
	lifecycle recoveryReconciler
	cleanup   recoveryReconciler
}

type RecoverResult struct {
	Action  RecoveryAction   `json:"action"`
	Session SessionReadModel `json:"session"`
}

func NewRecover(journal ports.Journal, observer sessionObserver, guard recoveryGuard, lifecycle, cleanup recoveryReconciler) *Recover {
	return &Recover{journal: journal, observer: observer, guard: guard, lifecycle: lifecycle, cleanup: cleanup}
}

func (u *Recover) Run(ctx context.Context, selector SessionSelector) (RecoverResult, error) {
	if u == nil || u.journal == nil || u.observer == nil || u.guard == nil {
		return RecoverResult{}, errors.New("recovery dependencies are incomplete")
	}
	selected, err := SelectSession(ctx, u.journal, selector, SelectionRecovery)
	if err != nil {
		return RecoverResult{}, err
	}
	initialEvidence, err := u.observer.Observe(ctx, selected)
	if err != nil {
		return RecoverResult{}, fmt.Errorf("observe recovery candidate %q: %w", selected.SessionID, err)
	}
	loaded, pending, err := u.journal.Load(ctx, selected.SessionID)
	if err != nil {
		return RecoverResult{}, err
	}
	if !sameRecoveryIdentity(selected, loaded) {
		return RecoverResult{}, ErrRecoveryIdentityChanged
	}
	action, reconciler, err := u.reconcilerFor(loaded)
	if err != nil {
		return RecoverResult{}, err
	}
	if err := u.guard.Revalidate(ctx, loaded, pending); err != nil {
		return RecoverResult{}, fmt.Errorf("revalidate recovery candidate %q: %w", loaded.SessionID, err)
	}
	currentEvidence, err := u.observer.Observe(ctx, loaded)
	if err != nil {
		return RecoverResult{}, fmt.Errorf("reobserve recovery candidate %q: %w", loaded.SessionID, err)
	}
	if !reflect.DeepEqual(initialEvidence, currentEvidence) {
		return RecoverResult{}, ErrRecoveryObservationChanged
	}
	reconciled, err := reconciler.Reconcile(ctx, loaded.SessionID)
	if err != nil {
		return RecoverResult{}, err
	}
	finalEvidence, err := u.observer.Observe(ctx, reconciled)
	if err != nil {
		return RecoverResult{}, fmt.Errorf("observe recovered session %q: %w", reconciled.SessionID, err)
	}
	models := BuildSessionReadModels([]domain.JournalSnapshot{reconciled}, map[string]SessionEvidence{reconciled.SessionID: finalEvidence})
	return RecoverResult{Action: action, Session: models[0]}, nil
}

func (u *Recover) reconcilerFor(snapshot domain.JournalSnapshot) (RecoveryAction, recoveryReconciler, error) {
	if snapshot.Cleanup.State == domain.CleanupFailed || snapshot.Cleanup.State == domain.CleanupRunning {
		if u.cleanup == nil {
			return "", nil, errors.New("cleanup reconciler is nil")
		}
		return RecoveryActionCleanup, u.cleanup, nil
	}
	if snapshot.State == domain.SessionOpening || snapshot.State == domain.SessionRecovering {
		if u.lifecycle == nil {
			return "", nil, errors.New("lifecycle reconciler is nil")
		}
		return RecoveryActionLifecycle, u.lifecycle, nil
	}
	return "", nil, ErrRecoveryNotRequired
}

func sameRecoveryIdentity(first, second domain.JournalSnapshot) bool {
	return first.SessionID == second.SessionID && first.Capsule == second.Capsule && first.Lineage == second.Lineage &&
		first.Mode == second.Mode && first.Materialization.CanonicalPath == second.Materialization.CanonicalPath &&
		first.Workspace.ID == second.Workspace.ID && first.Workspace.Context == second.Workspace.Context &&
		first.Lease.Revision == second.Lease.Revision
}
