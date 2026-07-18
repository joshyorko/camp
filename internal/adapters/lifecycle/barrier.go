package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/registry"
)

type barrierJournalReader interface {
	Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error)
}
type barrierController interface {
	Observe(context.Context, domain.ServiceUnitRecord) (supervisor.UnitObservation, error)
	Stop(context.Context, domain.ServiceUnitRecord) error
	Restart(context.Context, string, string, string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)
}

type RegistryBarrier struct {
	journal  barrierJournalReader
	services barrierController
}

func NewRegistryBarrier(journal barrierJournalReader, services barrierController) *RegistryBarrier {
	return &RegistryBarrier{journal: journal, services: services}
}

func (b *RegistryBarrier) WithCut(ctx context.Context, request registry.SnapshotRequest, cut func() error) (resultErr error) {
	if b == nil || b.journal == nil || b.services == nil || request.SessionID == "" || cut == nil {
		return errors.New("registry seal barrier dependencies are incomplete")
	}
	snapshot, pending, err := b.journal.Load(ctx, request.SessionID)
	if err != nil {
		return err
	}
	if len(pending) != 0 {
		return errors.New("registry seal barrier requires a reconciled session")
	}
	var record domain.ServiceUnitRecord
	matches := 0
	for _, service := range snapshot.Services {
		if service.Name == hauler.RegistryServiceName {
			record = service
			matches++
		}
	}
	if matches != 1 {
		return errors.New("registry seal barrier requires exactly one recorded registry")
	}
	observation, err := b.services.Observe(ctx, record)
	if err != nil || observation.State != supervisor.UnitLive {
		return errors.Join(err, errors.New("registry is not durably live before seal"))
	}
	tokenBytes := make([]byte, 12)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	if err := b.services.Stop(ctx, record); err != nil {
		return err
	}
	defer func() {
		_, _, restartErr := b.services.Restart(context.WithoutCancel(ctx), snapshot.SessionID, hauler.RegistryServiceName, "seal-"+hex.EncodeToString(tokenBytes))
		resultErr = errors.Join(resultErr, restartErr)
	}()
	return cut()
}
