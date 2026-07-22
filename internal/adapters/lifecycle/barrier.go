package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

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
	RestartWithin(context.Context, string, string, string, string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)
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
	sealIndex := -1
	restartIndex := -1
	startIndex := -1
	for index, item := range pending {
		if matchesRegistrySealIntent(item.Intent, request) {
			sealIndex = index
		}
		if item.Intent.SessionID == request.SessionID && item.Intent.Transition == "ServiceRestart" && item.Intent.ID == hauler.RegistryServiceName+"-"+request.RegistryLaunchToken+"-restart" {
			restartIndex = index
		}
		if item.Intent.SessionID == request.SessionID && item.Intent.Transition == "ServiceStart" && item.Intent.ID == hauler.RegistryServiceName+"-"+request.RegistryLaunchToken {
			startIndex = index
		}
	}
	if (len(pending) == 2 && sealIndex >= 0 && restartIndex >= 0) || (len(pending) == 3 && sealIndex >= 0 && restartIndex >= 0 && startIndex >= 0) {
		_, _, restartErr := b.services.RestartWithin(context.WithoutCancel(ctx), snapshot.SessionID, hauler.RegistryServiceName, request.RegistryLaunchToken, pending[sealIndex].Intent.ID)
		return errors.Join(restartErr, errors.New("registry restart reconciliation completed; retry the seal"))
	}
	if len(pending) != 1 || sealIndex != 0 {
		return errors.New("registry seal barrier requires a reconciled session")
	}
	sealIntent := pending[sealIndex].Intent
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
	if request.RegistryLaunchToken == "" || record.LaunchToken != request.RegistryLaunchToken {
		return errors.New("registry seal barrier cannot repeat an outcome-unknown registry restart")
	}
	observation, err := b.services.Observe(ctx, record)
	if err != nil {
		return errors.Join(err, errors.New("registry is not durably live before seal"))
	}
	if observation.State != supervisor.UnitLive {
		if _, statErr := os.Lstat(request.SnapshotRoot); !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(statErr, errors.New("stopped registry has an ambiguous prior seal outcome"))
		}
		_, _, restartErr := b.services.RestartWithin(context.WithoutCancel(ctx), snapshot.SessionID, hauler.RegistryServiceName, record.LaunchToken, sealIntent.ID)
		return errors.Join(restartErr, errors.New("registry was restored after an incomplete pre-cut stop; retry the seal"))
	}
	tokenBytes := make([]byte, 12)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	if stopErr := b.services.Stop(ctx, record); stopErr != nil {
		after, observeErr := b.services.Observe(context.WithoutCancel(ctx), record)
		if observeErr != nil || after.State != supervisor.UnitStopped {
			return errors.Join(stopErr, observeErr)
		}
		_, _, restartErr := b.services.RestartWithin(context.WithoutCancel(ctx), snapshot.SessionID, hauler.RegistryServiceName, record.LaunchToken, sealIntent.ID)
		return errors.Join(stopErr, restartErr)
	}
	defer func() {
		_, _, restartErr := b.services.RestartWithin(context.WithoutCancel(ctx), snapshot.SessionID, hauler.RegistryServiceName, "seal-"+hex.EncodeToString(tokenBytes), sealIntent.ID)
		resultErr = errors.Join(resultErr, restartErr)
	}()
	return cut()
}

func matchesRegistrySealIntent(intent ports.IntentRecord, request registry.SnapshotRequest) bool {
	if intent.ID == "" || intent.SessionID != request.SessionID || intent.Transition != "RegistrySnapshotSealed" {
		return false
	}
	var intended registry.SnapshotRequest
	return json.Unmarshal(intent.Input, &intended) == nil && intended == request
}
