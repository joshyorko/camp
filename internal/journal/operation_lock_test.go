package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type fakeIdentityVerifier struct {
	current domain.ProcessIdentity
	alive   map[domain.ProcessIdentity]bool
}

func (f *fakeIdentityVerifier) CurrentProcess(context.Context) (domain.ProcessIdentity, error) {
	return f.current, nil
}

func (f *fakeIdentityVerifier) IsCurrent(_ context.Context, identity domain.ProcessIdentity) (bool, error) {
	return f.alive[identity], nil
}

func TestOperationLockerRejectsLiveOwnerAndRecoversPIDReusedStaleLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	old := domain.ProcessIdentity{PID: 400, BootID: "boot-a", StartTicks: 10}
	verifier := &fakeIdentityVerifier{current: old, alive: map[domain.ProcessIdentity]bool{old: true}}
	locker, err := NewOperationLocker(root, verifier)
	if err != nil {
		t.Fatalf("NewOperationLocker() error = %v", err)
	}
	owner := ports.OperationOwner{SessionID: "session-a", Operation: "sync"}
	first, err := locker.Acquire(ctx, owner)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := locker.Acquire(ctx, owner); !errors.Is(err, ErrOperationLocked) {
		t.Fatalf("second Acquire() error = %v, want ErrOperationLocked", err)
	}

	path := filepath.Join(root, "locks", owner.SessionID+".lock")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %v, %v; want 0600", info, err)
	}

	reused := domain.ProcessIdentity{PID: old.PID, BootID: old.BootID, StartTicks: old.StartTicks + 1}
	verifier.current = reused
	verifier.alive[old] = false
	verifier.alive[reused] = true
	second, err := locker.Acquire(ctx, ports.OperationOwner{SessionID: owner.SessionID, Operation: "close"})
	if err != nil {
		t.Fatalf("Acquire() after PID reuse error = %v", err)
	}
	if second.Identity != reused || second.ID == first.ID {
		t.Fatalf("replacement token = %#v, want new exact identity", second)
	}
	if err := locker.Release(ctx, first); !errors.Is(err, ErrOperationOwnership) {
		t.Fatalf("Release(stale token) error = %v, want ErrOperationOwnership", err)
	}
	if err := locker.Release(ctx, second); err != nil {
		t.Fatalf("Release(current token) error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock still exists after release: %v", err)
	}
}

func TestOperationLockerValidateRequiresExactOnDiskToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	identity := domain.ProcessIdentity{PID: 401, BootID: "boot-a", StartTicks: 11}
	verifier := &fakeIdentityVerifier{current: identity, alive: map[domain.ProcessIdentity]bool{identity: true}}
	locker, err := NewOperationLocker(t.TempDir(), verifier)
	if err != nil {
		t.Fatal(err)
	}
	token, err := locker.Acquire(ctx, ports.OperationOwner{SessionID: "session-a", Operation: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	if err := locker.Validate(ctx, token); err != nil {
		t.Fatalf("Validate(exact token) error = %v", err)
	}
	changed := token
	changed.ID = "different"
	if err := locker.Validate(ctx, changed); !errors.Is(err, ErrOperationOwnership) {
		t.Fatalf("Validate(changed token) error = %v, want ErrOperationOwnership", err)
	}
}
