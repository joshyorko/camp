package doctor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/joshyorko/camp/internal/ports"
)

type BackendTransactionProbe struct {
	Store     ports.ObjectStore
	NewSuffix func() (string, error)
}

func (BackendTransactionProbe) Capability() string { return "backend" }

func (p BackendTransactionProbe) Probe(ctx context.Context) (result Result) {
	if p.Store == nil {
		return backendBlocked("backend_transaction_probe_unconfigured", "backend transaction probe is unavailable", "repair Camp composition, then rerun camp doctor")
	}
	newSuffix := p.NewSuffix
	if newSuffix == nil {
		newSuffix = randomProbeSuffix
	}
	suffix, err := newSuffix()
	if err != nil || suffix == "" {
		return backendBlocked("backend_probe_identity_failed", "backend probe identity could not be generated", "repair the host random source, then rerun camp doctor")
	}
	key := "camp-doctor/" + suffix
	first := []byte("camp-doctor-one:" + suffix)
	second := []byte("camp-doctor-two:" + suffix)
	created, err := p.Store.PutConditional(ctx, key, first, ports.WriteCondition{MustBeAbsent: true})
	if err != nil {
		return backendBlocked("backend_transaction_failed", "backend create-if-absent failed", "repair backend read-write access, then rerun camp doctor")
	}
	cleanupRevision := created.Revision
	defer func() {
		if cleanupRevision == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if cleanupErr := p.Store.DeleteConditional(cleanupCtx, key, cleanupRevision); cleanupErr != nil {
			if errors.Is(cleanupErr, ports.ErrConflict) {
				result = backendBlocked("backend_cleanup_identity_mismatch", "backend probe resource identity changed before cleanup", "inspect and remove the unique camp-doctor resource only after verifying its owner and revision")
			} else {
				result = backendBlocked("backend_cleanup_failed", "backend probe resource cleanup failed", "repair backend cleanup access and remove the recorded camp-doctor resource after verifying its identity")
			}
		}
	}()
	if !backendReadback(ctx, p.Store, key, first, created.Revision) {
		return backendBlocked("backend_readback_failed", "backend readback did not match the completed create", "repair backend consistency, then rerun camp doctor")
	}
	if _, err := p.Store.PutConditional(ctx, key, []byte("must-not-win"), ports.WriteCondition{MustBeAbsent: true}); !errors.Is(err, ports.ErrConflict) {
		return backendBlocked("backend_conflict_unenforced", "backend create-if-absent conflict was not enforced", "do not use this backend for Camp writes")
	}
	replaced, err := p.Store.PutConditional(ctx, key, second, ports.WriteCondition{MatchRevision: created.Revision})
	if err != nil || replaced.Revision == "" || replaced.Revision == created.Revision {
		return backendBlocked("backend_replace_failed", "backend conditional replacement failed", "repair backend conditional writes, then rerun camp doctor")
	}
	cleanupRevision = replaced.Revision
	if _, err := p.Store.PutConditional(ctx, key, []byte("stale-must-not-win"), ports.WriteCondition{MatchRevision: created.Revision}); !errors.Is(err, ports.ErrConflict) {
		return backendBlocked("backend_conflict_unenforced", "backend stale revision conflict was not enforced", "do not use this backend for Camp writes")
	}
	if !backendReadback(ctx, p.Store, key, second, replaced.Revision) {
		return backendBlocked("backend_readback_failed", "backend readback did not match the completed replacement", "repair backend consistency, then rerun camp doctor")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	err = p.Store.DeleteConditional(cleanupCtx, key, replaced.Revision)
	cancel()
	if err != nil {
		if errors.Is(err, ports.ErrConflict) {
			return backendBlocked("backend_cleanup_identity_mismatch", "backend probe resource identity changed before cleanup", "inspect and remove the unique camp-doctor resource only after verifying its owner and revision")
		}
		return backendBlocked("backend_cleanup_failed", "backend probe resource cleanup failed", "repair backend cleanup access and remove the recorded camp-doctor resource after verifying its identity")
	}
	cleanupRevision = ""
	if _, err := p.Store.Head(ctx, key); !errors.Is(err, ports.ErrNotFound) {
		return backendBlocked("backend_cleanup_unverified", "backend probe cleanup could not be verified", "inspect the unique camp-doctor resource and repair backend deletion")
	}
	return Result{Capability: "backend", Status: StatusHealthy, Code: "backend_transaction_verified", Summary: "backend conditional transaction and cleanup are verified", Evidence: map[string]string{
		"prefix": "camp-doctor/", "operations": "create,readback,replace,conflict,readback,cleanup",
	}}
}

func backendReadback(ctx context.Context, store ports.ObjectStore, key string, want []byte, revision ports.Revision) bool {
	reader, meta, err := store.Get(ctx, key)
	if err != nil {
		return false
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, int64(len(want))+1))
	closeErr := reader.Close()
	return readErr == nil && closeErr == nil && bytes.Equal(body, want) && meta.Revision == revision && meta.Size == int64(len(want))
}

func backendBlocked(code, summary, remediation string) Result {
	return Result{Capability: "backend", Status: StatusBlocked, Code: code, Summary: summary, Remediation: remediation}
}

func randomProbeSuffix() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
