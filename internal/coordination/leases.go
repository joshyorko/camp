package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrLeaseHeld = errors.New("writer lease is held")
	ErrLeaseLost = errors.New("writer lease ownership was lost")
)

type LeaseOwner struct {
	SessionID string
	Machine   string
}

type LeaseToken struct {
	Lease    domain.WriterLease
	Revision ports.Revision
}

type LeaseRepository struct {
	store ports.ObjectStore
}

func NewLeaseRepository(store ports.ObjectStore) *LeaseRepository {
	return &LeaseRepository{store: store}
}

func (r *LeaseRepository) Read(ctx context.Context, capsule string, lineage domain.Lineage) (LeaseToken, error) {
	key, err := lineage.LeaseKey(capsule)
	if err != nil {
		return LeaseToken{}, err
	}
	lease, meta, err := readJSON[domain.WriterLease](ctx, r.store, key)
	if err != nil {
		return LeaseToken{}, err
	}
	if err := validateLease(lease, capsule, lineage); err != nil {
		return LeaseToken{}, err
	}
	return LeaseToken{Lease: lease, Revision: meta.Revision}, nil
}

func (r *LeaseRepository) Acquire(ctx context.Context, capsule string, lineage domain.Lineage, owner LeaseOwner, observed *PointerRecord, now time.Time, ttl time.Duration) (LeaseToken, error) {
	var opened *domain.GenerationRef
	if observed != nil {
		if observed.Revision == "" {
			return LeaseToken{}, fmt.Errorf("observed pointer has no revision: %w", ErrInvalidDocument)
		}
		if err := validatePointer(observed.Pointer, capsule, lineage); err != nil {
			return LeaseToken{}, fmt.Errorf("validate observed pointer: %w", err)
		}
		opened = cloneGenerationRef(&observed.Pointer.Generation)
	}
	return r.acquire(ctx, capsule, lineage, owner, observed, opened, now, ttl)
}

func (r *LeaseRepository) AcquireBranchFrom(ctx context.Context, capsule string, lineage domain.Lineage, owner LeaseOwner, source PointerRecord, now time.Time, ttl time.Duration) (LeaseToken, error) {
	if lineage.IsMain() || source.Pointer.Lineage == lineage || source.Revision == "" {
		return LeaseToken{}, fmt.Errorf("invalid branch source: %w", ErrInvalidDocument)
	}
	if err := validatePointer(source.Pointer, capsule, source.Pointer.Lineage); err != nil {
		return LeaseToken{}, fmt.Errorf("validate branch source pointer: %w", err)
	}
	pointers := NewPointerRepository(r.store)
	if err := pointers.Revalidate(ctx, source); err != nil {
		return LeaseToken{}, fmt.Errorf("revalidate branch source pointer: %w", err)
	}
	token, err := r.acquire(ctx, capsule, lineage, owner, nil, cloneGenerationRef(&source.Pointer.Generation), now, ttl)
	if err != nil {
		return LeaseToken{}, err
	}
	if err := pointers.Revalidate(ctx, source); err == nil {
		return token, nil
	} else {
		observationErr := fmt.Errorf("branch source changed during lease acquisition: %w", ErrPointerChanged)
		if releaseErr := r.releaseAcquiredLease(ctx, token); releaseErr != nil {
			return LeaseToken{}, errors.Join(observationErr, releaseErr)
		}
		return LeaseToken{}, observationErr
	}
}

func (r *LeaseRepository) acquire(ctx context.Context, capsule string, lineage domain.Lineage, owner LeaseOwner, observed *PointerRecord, opened *domain.GenerationRef, now time.Time, ttl time.Duration) (LeaseToken, error) {
	key, err := lineage.LeaseKey(capsule)
	if err != nil {
		return LeaseToken{}, err
	}
	if owner.SessionID == "" || owner.Machine == "" || now.IsZero() || ttl <= 0 {
		return LeaseToken{}, fmt.Errorf("invalid lease acquisition: %w", ErrInvalidDocument)
	}
	if opened != nil {
		if err := validateGenerationRef(*opened); err != nil {
			return LeaseToken{}, err
		}
	}
	expiresAt := now.Add(ttl)
	if !expiresAt.After(now) {
		return LeaseToken{}, fmt.Errorf("invalid lease expiry: %w", ErrInvalidDocument)
	}
	lease := domain.WriterLease{
		SchemaVersion:    domain.SchemaVersion,
		Capsule:          capsule,
		Lineage:          lineage,
		SessionID:        owner.SessionID,
		Machine:          owner.Machine,
		OpenedGeneration: opened,
		CreatedAt:        now,
		HeartbeatAt:      now,
		ExpiresAt:        expiresAt,
	}
	body, err := json.Marshal(lease)
	if err != nil {
		return LeaseToken{}, err
	}

	for {
		meta, putErr := r.store.PutConditional(ctx, key, body, ports.WriteCondition{MustBeAbsent: true})
		if putErr == nil {
			return r.finishAcquire(ctx, capsule, lineage, LeaseToken{Lease: lease, Revision: meta.Revision}, observed)
		}
		if errors.Is(putErr, ports.ErrAmbiguous) {
			if reconciled, ok := r.reconcileLease(ctx, lease); ok {
				return r.finishAcquire(ctx, capsule, lineage, reconciled, observed)
			}
			return LeaseToken{}, putErr
		}
		if !errors.Is(putErr, ports.ErrConflict) {
			return LeaseToken{}, putErr
		}
		current, readErr := r.Read(ctx, capsule, lineage)
		if errors.Is(readErr, ports.ErrNotFound) {
			continue
		}
		if readErr != nil {
			return LeaseToken{}, readErr
		}
		if current.Lease.ExpiresAt.After(now) {
			return LeaseToken{}, fmt.Errorf("lineage %s/%s is leased by session %s until %s: %w", capsule, lineage.Branch, current.Lease.SessionID, current.Lease.ExpiresAt.Format(time.RFC3339), ErrLeaseHeld)
		}
		meta, putErr = r.store.PutConditional(ctx, key, body, ports.WriteCondition{MatchRevision: current.Revision})
		if putErr == nil {
			return r.finishAcquire(ctx, capsule, lineage, LeaseToken{Lease: lease, Revision: meta.Revision}, observed)
		}
		if errors.Is(putErr, ports.ErrAmbiguous) {
			if reconciled, ok := r.reconcileLease(ctx, lease); ok {
				return r.finishAcquire(ctx, capsule, lineage, reconciled, observed)
			}
			return LeaseToken{}, putErr
		}
		if !errors.Is(putErr, ports.ErrConflict) {
			return LeaseToken{}, putErr
		}
		if err := ctx.Err(); err != nil {
			return LeaseToken{}, err
		}
	}
}

func (r *LeaseRepository) finishAcquire(ctx context.Context, capsule string, lineage domain.Lineage, token LeaseToken, observed *PointerRecord) (LeaseToken, error) {
	pointers := NewPointerRepository(r.store)
	var observationErr error
	if observed == nil {
		_, err := pointers.Read(ctx, capsule, lineage)
		switch {
		case errors.Is(err, ports.ErrNotFound):
			return token, nil
		case err == nil:
			observationErr = fmt.Errorf("pointer appeared after absent observation: %w", ErrPointerChanged)
		default:
			observationErr = errors.Join(fmt.Errorf("verify pointer absence: %w", ErrPointerChanged), err)
		}
	} else if err := pointers.Revalidate(ctx, *observed); err == nil {
		return token, nil
	} else if errors.Is(err, ErrPointerChanged) {
		observationErr = err
	} else {
		observationErr = errors.Join(fmt.Errorf("revalidate observed pointer: %w", ErrPointerChanged), err)
	}

	releaseErr := r.releaseAcquiredLease(ctx, token)
	if releaseErr == nil {
		return LeaseToken{}, observationErr
	}
	return LeaseToken{}, errors.Join(observationErr, releaseErr)
}

func (r *LeaseRepository) releaseAcquiredLease(ctx context.Context, token LeaseToken) error {
	key, err := token.Lease.Lineage.LeaseKey(token.Lease.Capsule)
	if err != nil {
		return err
	}
	err = r.store.DeleteConditional(ctx, key, token.Revision)
	if err == nil {
		return nil
	}
	if errors.Is(err, ports.ErrConflict) || errors.Is(err, ports.ErrNotFound) {
		return fmt.Errorf("release acquired lease: %w", errors.Join(ErrLeaseLost, err))
	}
	return fmt.Errorf("release acquired lease: %w", err)
}

func (r *LeaseRepository) Revalidate(ctx context.Context, token LeaseToken, now time.Time) error {
	if now.IsZero() || now.Before(token.Lease.HeartbeatAt) {
		return fmt.Errorf("invalid lease validation time: %w", ErrInvalidDocument)
	}
	current, err := r.Read(ctx, token.Lease.Capsule, token.Lease.Lineage)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return fmt.Errorf("lease disappeared: %w", ErrLeaseLost)
		}
		return err
	}
	if current.Revision != token.Revision || !documentsEqual(current.Lease, token.Lease) || !current.Lease.ExpiresAt.After(now) {
		return fmt.Errorf("lease token is stale or expired: %w", ErrLeaseLost)
	}
	return nil
}

func (r *LeaseRepository) Renew(ctx context.Context, token LeaseToken, now time.Time, ttl time.Duration) (LeaseToken, error) {
	if now.IsZero() || ttl <= 0 || now.Before(token.Lease.HeartbeatAt) {
		return LeaseToken{}, fmt.Errorf("invalid lease renewal: %w", ErrInvalidDocument)
	}
	expiresAt := now.Add(ttl)
	if !expiresAt.After(now) {
		return LeaseToken{}, fmt.Errorf("invalid lease expiry: %w", ErrInvalidDocument)
	}
	if err := r.Revalidate(ctx, token, now); err != nil {
		return LeaseToken{}, err
	}
	renewed := token.Lease
	renewed.HeartbeatAt = now
	renewed.ExpiresAt = expiresAt
	body, err := json.Marshal(renewed)
	if err != nil {
		return LeaseToken{}, err
	}
	key, _ := renewed.Lineage.LeaseKey(renewed.Capsule)
	meta, err := r.store.PutConditional(ctx, key, body, ports.WriteCondition{MatchRevision: token.Revision})
	if err != nil {
		if errors.Is(err, ports.ErrAmbiguous) {
			if reconciled, ok := r.reconcileLease(ctx, renewed); ok {
				return reconciled, nil
			}
		}
		if errors.Is(err, ports.ErrConflict) || errors.Is(err, ports.ErrNotFound) {
			return LeaseToken{}, fmt.Errorf("renew lease: %w", ErrLeaseLost)
		}
		return LeaseToken{}, err
	}
	return LeaseToken{Lease: renewed, Revision: meta.Revision}, nil
}

func (r *LeaseRepository) Release(ctx context.Context, token LeaseToken) error {
	current, err := r.Read(ctx, token.Lease.Capsule, token.Lease.Lineage)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return fmt.Errorf("release missing lease: %w", ErrLeaseLost)
		}
		return err
	}
	if current.Revision != token.Revision || !documentsEqual(current.Lease, token.Lease) {
		return fmt.Errorf("release stale lease token: %w", ErrLeaseLost)
	}
	key, _ := token.Lease.Lineage.LeaseKey(token.Lease.Capsule)
	err = r.store.DeleteConditional(ctx, key, token.Revision)
	if err == nil {
		return nil
	}
	if errors.Is(err, ports.ErrAmbiguous) {
		if _, readErr := r.store.Head(ctx, key); errors.Is(readErr, ports.ErrNotFound) {
			return nil
		}
	}
	if errors.Is(err, ports.ErrConflict) || errors.Is(err, ports.ErrNotFound) {
		return fmt.Errorf("release lease: %w", ErrLeaseLost)
	}
	return err
}

func (r *LeaseRepository) reconcileLease(ctx context.Context, expected domain.WriterLease) (LeaseToken, bool) {
	current, err := r.Read(ctx, expected.Capsule, expected.Lineage)
	return current, err == nil && documentsEqual(current.Lease, expected)
}

func validateLease(lease domain.WriterLease, capsule string, lineage domain.Lineage) error {
	if lease.SchemaVersion != domain.SchemaVersion || lease.Capsule != capsule || lease.Lineage != lineage {
		return fmt.Errorf("lease identity or schema mismatch: %w", ErrInvalidDocument)
	}
	if _, err := lineage.LeaseKey(capsule); err != nil {
		return err
	}
	if lease.SessionID == "" || lease.Machine == "" || lease.CreatedAt.IsZero() || lease.HeartbeatAt.Before(lease.CreatedAt) || !lease.ExpiresAt.After(lease.HeartbeatAt) {
		return fmt.Errorf("lease lacks valid ownership or time bounds: %w", ErrInvalidDocument)
	}
	if lease.OpenedGeneration != nil {
		if err := validateGenerationRef(*lease.OpenedGeneration); err != nil {
			return err
		}
	}
	return nil
}
