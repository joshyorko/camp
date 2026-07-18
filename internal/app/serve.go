package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type serveController interface {
	Observe(context.Context, domain.ServiceUnitRecord) (supervisor.UnitObservation, error)
	Restart(context.Context, string, string, string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)
}

type serveLogReader interface {
	ReadTail(context.Context, domain.ServiceUnitRecord, int64) (supervisor.LogChunk, error)
}

type Serve struct {
	journal    ports.Journal
	locks      operationLocker
	guard      recoveryGuard
	controller serveController
	logs       serveLogReader
}

type ServeRestartRequest struct {
	Selector    SessionSelector
	Service     string
	LaunchToken string
}

func NewServe(journal ports.Journal, locks operationLocker, guard recoveryGuard, controller serveController, logs serveLogReader) *Serve {
	return &Serve{journal: journal, locks: locks, guard: guard, controller: controller, logs: logs}
}

func (u *Serve) Status(ctx context.Context, selector SessionSelector, serviceName string) (ServiceReadModel, error) {
	_, record, err := u.prepare(ctx, selector, serviceName)
	if err != nil {
		return ServiceReadModel{}, err
	}
	observation, err := u.controller.Observe(ctx, record)
	if err != nil {
		return ServiceReadModel{}, err
	}
	return serviceReadModel(observation), nil
}

func (u *Serve) Logs(ctx context.Context, selector SessionSelector, serviceName string, limit int64) (supervisor.LogChunk, error) {
	if u == nil || u.logs == nil {
		return supervisor.LogChunk{}, errors.New("serve log reader is nil")
	}
	_, record, err := u.prepare(ctx, selector, serviceName)
	if err != nil {
		return supervisor.LogChunk{}, err
	}
	if _, err := u.controller.Observe(ctx, record); err != nil {
		return supervisor.LogChunk{}, err
	}
	return u.logs.ReadTail(ctx, record, limit)
}

func (u *Serve) Restart(ctx context.Context, request ServeRestartRequest) (result ServiceReadModel, resultErr error) {
	if u == nil || u.journal == nil || u.locks == nil || u.guard == nil || u.controller == nil || request.Service == "" || request.LaunchToken == "" {
		return ServiceReadModel{}, errors.New("serve restart dependencies or request are incomplete")
	}
	selected, err := SelectSession(ctx, u.journal, request.Selector, SelectionActiveMutation)
	if err != nil {
		return ServiceReadModel{}, err
	}
	token, err := u.locks.Acquire(ctx, ports.OperationOwner{SessionID: selected.SessionID, Operation: "serve-restart:" + request.Service})
	if err != nil {
		return ServiceReadModel{}, err
	}
	defer func() {
		if err := u.locks.Release(context.WithoutCancel(ctx), token); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	loaded, pending, err := u.journal.Load(ctx, selected.SessionID)
	if err != nil {
		return ServiceReadModel{}, err
	}
	if !sameRecoveryIdentity(selected, loaded) {
		return ServiceReadModel{}, ErrRecoveryIdentityChanged
	}
	if err := u.guard.Revalidate(ctx, loaded, pending); err != nil {
		return ServiceReadModel{}, err
	}
	if _, ok := recordedServiceForApp(loaded, request.Service); !ok {
		return ServiceReadModel{}, fmt.Errorf("service %q is not recorded", request.Service)
	}
	restarted, _, err := u.controller.Restart(ctx, loaded.SessionID, request.Service, request.LaunchToken)
	if err != nil {
		return ServiceReadModel{}, err
	}
	observation, err := u.controller.Observe(ctx, restarted)
	if err != nil {
		return ServiceReadModel{}, err
	}
	return serviceReadModel(observation), nil
}

func (u *Serve) prepare(ctx context.Context, selector SessionSelector, serviceName string) (domain.JournalSnapshot, domain.ServiceUnitRecord, error) {
	if u == nil || u.journal == nil || u.guard == nil || u.controller == nil || serviceName == "" {
		return domain.JournalSnapshot{}, domain.ServiceUnitRecord{}, errors.New("serve dependencies or service are incomplete")
	}
	selected, err := SelectSession(ctx, u.journal, selector, SelectionActiveMutation)
	if err != nil {
		return domain.JournalSnapshot{}, domain.ServiceUnitRecord{}, err
	}
	loaded, pending, err := u.journal.Load(ctx, selected.SessionID)
	if err != nil {
		return domain.JournalSnapshot{}, domain.ServiceUnitRecord{}, err
	}
	if !sameRecoveryIdentity(selected, loaded) {
		return domain.JournalSnapshot{}, domain.ServiceUnitRecord{}, ErrRecoveryIdentityChanged
	}
	if err := u.guard.Revalidate(ctx, loaded, pending); err != nil {
		return domain.JournalSnapshot{}, domain.ServiceUnitRecord{}, err
	}
	record, ok := recordedServiceForApp(loaded, serviceName)
	if !ok {
		return domain.JournalSnapshot{}, domain.ServiceUnitRecord{}, fmt.Errorf("service %q is not recorded", serviceName)
	}
	return loaded, record, nil
}

func recordedServiceForApp(snapshot domain.JournalSnapshot, name string) (domain.ServiceUnitRecord, bool) {
	for _, service := range snapshot.Services {
		if service.Name == name {
			return service, true
		}
	}
	return domain.ServiceUnitRecord{}, false
}

func serviceReadModel(observation supervisor.UnitObservation) ServiceReadModel {
	liveness := ServiceLivenessStopped
	if observation.State == supervisor.UnitLive {
		liveness = ServiceLivenessLive
	}
	return ServiceReadModel{Name: observation.Record.Name, Liveness: liveness}
}
