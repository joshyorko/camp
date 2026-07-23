package s3store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var ErrUnsafeWriter = errors.New("S3 endpoint is unsafe for writer use")

// ProbeWriter proves create-if-absent, conditional replacement, stale-write
// rejection, readback, and conditional cleanup on a unique disposable object.
func (s *Store) ProbeWriter(ctx context.Context, prefix string) (resultErr error) {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("probe prefix must end in slash: %w", ports.ErrInvalidKey)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate S3 probe key: %w", err)
	}
	key := prefix + hex.EncodeToString(random)
	firstBody := append([]byte("camp-s3-probe-one:"), random...)
	secondBody := append([]byte("camp-s3-probe-two:"), random...)

	created, err := s.PutConditional(ctx, key, firstBody, ports.WriteCondition{MustBeAbsent: true})
	if err != nil {
		return fmt.Errorf("S3 writer probe create: %w", err)
	}
	cleanupRevision := created.Revision
	defer func() {
		if cleanupRevision == "" {
			return
		}
		cleanupErr := s.DeleteConditional(context.WithoutCancel(ctx), key, cleanupRevision)
		if cleanupErr != nil && !errors.Is(cleanupErr, ports.ErrNotFound) && resultErr == nil {
			resultErr = fmt.Errorf("S3 writer probe cleanup: %w", cleanupErr)
		}
	}()

	if err := s.verifyProbeReadback(ctx, key, firstBody, created.Revision); err != nil {
		return unsafe(err)
	}
	duplicate, err := s.PutConditional(ctx, key, []byte("must-not-win"), ports.WriteCondition{MustBeAbsent: true})
	if err == nil {
		cleanupRevision = duplicate.Revision
		return unsafe(errors.New("create-if-absent request overwrote an existing object"))
	}
	if !errors.Is(err, ports.ErrConflict) {
		return fmt.Errorf("S3 writer probe duplicate create: %w", err)
	}

	replaced, err := s.PutConditional(ctx, key, secondBody, ports.WriteCondition{MatchRevision: created.Revision})
	if errors.Is(err, ports.ErrAmbiguous) {
		observed, observeErr := s.Head(ctx, key)
		switch {
		case observeErr == nil && observed.Revision == created.Revision:
			replaced, err = s.PutConditional(ctx, key, secondBody, ports.WriteCondition{MatchRevision: created.Revision})
		case observeErr == nil:
			if verifyErr := s.verifyProbeReadback(ctx, key, secondBody, observed.Revision); verifyErr == nil {
				replaced, err = observed, nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("S3 writer probe conditional replace: %w", err)
	}
	cleanupRevision = replaced.Revision
	if replaced.Revision == created.Revision {
		return unsafe(errors.New("conditional replacement did not change the opaque revision"))
	}
	stale, err := s.PutConditional(ctx, key, []byte("stale-must-not-win"), ports.WriteCondition{MatchRevision: created.Revision})
	if err == nil {
		cleanupRevision = stale.Revision
		return unsafe(errors.New("stale conditional replacement succeeded"))
	}
	if !errors.Is(err, ports.ErrConflict) {
		return fmt.Errorf("S3 writer probe stale replacement: %w", err)
	}
	if err := s.verifyProbeReadback(ctx, key, secondBody, replaced.Revision); err != nil {
		return unsafe(err)
	}
	if err := s.DeleteConditional(ctx, key, replaced.Revision); err != nil {
		return fmt.Errorf("S3 writer probe conditional delete: %w", err)
	}
	cleanupRevision = ""
	if _, err := s.Head(ctx, key); !errors.Is(err, ports.ErrNotFound) {
		if err == nil {
			return unsafe(errors.New("conditional delete left the object readable"))
		}
		return fmt.Errorf("S3 writer probe post-delete head: %w", err)
	}
	return nil
}

func (s *Store) verifyProbeReadback(ctx context.Context, key string, expected []byte, revision ports.Revision) error {
	reader, meta, err := s.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read back probe object: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, int64(len(expected))+1))
	closeErr := reader.Close()
	if readErr != nil {
		return fmt.Errorf("read probe object body: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close probe object body: %w", closeErr)
	}
	if !bytes.Equal(body, expected) || meta.Size != int64(len(expected)) || meta.Revision != revision {
		return fmt.Errorf("readback body or metadata did not match the completed mutation")
	}
	return nil
}

func unsafe(cause error) error {
	return fmt.Errorf("%v: %w", cause, ErrUnsafeWriter)
}
