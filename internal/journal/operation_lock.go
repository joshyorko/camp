package journal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrOperationLocked    = errors.New("session operation is already locked")
	ErrOperationOwnership = errors.New("operation lock ownership changed")
)

type OperationLocker struct {
	root     string
	identity ports.IdentityVerifier
}

func NewOperationLocker(root string, identity ports.IdentityVerifier) (*OperationLocker, error) {
	if root == "" || identity == nil {
		return nil, errors.New("operation lock requires root and identity verifier")
	}
	canonical, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(canonical, "locks"), 0o700); err != nil {
		return nil, fmt.Errorf("create operation lock directory: %w", err)
	}
	return &OperationLocker{root: canonical, identity: identity}, nil
}

func (l *OperationLocker) Acquire(ctx context.Context, owner ports.OperationOwner) (ports.OperationToken, error) {
	if err := validateOperationOwner(owner); err != nil {
		return ports.OperationToken{}, err
	}
	guard, err := l.lockGuard(owner.SessionID)
	if err != nil {
		return ports.OperationToken{}, err
	}
	defer unlockGuard(guard)

	path := l.lockPath(owner.SessionID)
	current, err := readOperationToken(path)
	if err == nil {
		alive, inspectErr := l.identity.IsCurrent(ctx, current.Identity)
		if inspectErr != nil {
			return ports.OperationToken{}, fmt.Errorf("inspect operation lock owner: %w", inspectErr)
		}
		if alive {
			return ports.OperationToken{}, fmt.Errorf("%s owns %s: %w", current.Owner.Operation, owner.SessionID, ErrOperationLocked)
		}
		if err := os.Remove(path); err != nil {
			return ports.OperationToken{}, fmt.Errorf("remove stale operation lock: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ports.OperationToken{}, err
	}

	identity, err := l.identity.CurrentProcess(ctx)
	if err != nil {
		return ports.OperationToken{}, fmt.Errorf("identify operation owner: %w", err)
	}
	tokenID, err := randomToken()
	if err != nil {
		return ports.OperationToken{}, err
	}
	token := ports.OperationToken{ID: tokenID, Owner: owner, Identity: identity}
	body, err := json.Marshal(token)
	if err != nil {
		return ports.OperationToken{}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ports.OperationToken{}, ErrOperationLocked
		}
		return ports.OperationToken{}, fmt.Errorf("create operation lock: %w", err)
	}
	if _, err := file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return ports.OperationToken{}, fmt.Errorf("write operation lock: %w", err)
	}
	if closeErr != nil {
		return ports.OperationToken{}, fmt.Errorf("close operation lock: %w", closeErr)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return ports.OperationToken{}, fmt.Errorf("sync operation lock directory: %w", err)
	}
	return token, nil
}

func (l *OperationLocker) Release(ctx context.Context, token ports.OperationToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateOperationOwner(token.Owner); err != nil || token.ID == "" {
		return ErrOperationOwnership
	}
	guard, err := l.lockGuard(token.Owner.SessionID)
	if err != nil {
		return err
	}
	defer unlockGuard(guard)
	path := l.lockPath(token.Owner.SessionID)
	current, err := readOperationToken(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrOperationOwnership
		}
		return err
	}
	if current != token {
		return ErrOperationOwnership
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove operation lock: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func (l *OperationLocker) Validate(ctx context.Context, token ports.OperationToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateOperationOwner(token.Owner); err != nil || token.ID == "" {
		return ErrOperationOwnership
	}
	guard, err := l.lockGuard(token.Owner.SessionID)
	if err != nil {
		return err
	}
	defer unlockGuard(guard)
	current, err := readOperationToken(l.lockPath(token.Owner.SessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrOperationOwnership
		}
		return err
	}
	if current != token {
		return ErrOperationOwnership
	}
	return nil
}

func (l *OperationLocker) lockGuard(sessionID string) (*os.File, error) {
	path := filepath.Join(l.root, "locks", sessionID+".guard")
	guard, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); err != nil {
		_ = guard.Close()
		return nil, err
	}
	return guard, nil
}

func unlockGuard(guard *os.File) {
	_ = syscall.Flock(int(guard.Fd()), syscall.LOCK_UN)
	_ = guard.Close()
}

func (l *OperationLocker) lockPath(sessionID string) string {
	return filepath.Join(l.root, "locks", sessionID+".lock")
}

func readOperationToken(path string) (ports.OperationToken, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return ports.OperationToken{}, err
	}
	var token ports.OperationToken
	if err := json.Unmarshal(body, &token); err != nil {
		return ports.OperationToken{}, fmt.Errorf("decode operation lock: %w", err)
	}
	return token, nil
}

func validateOperationOwner(owner ports.OperationOwner) error {
	if owner.SessionID == "" || owner.Operation == "" || strings.ContainsAny(owner.SessionID, "/\\\x00") {
		return errors.New("invalid operation owner")
	}
	return nil
}

func randomToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate operation token: %w", err)
	}
	return hex.EncodeToString(value), nil
}
