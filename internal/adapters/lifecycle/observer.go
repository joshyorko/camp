package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type serviceObserver interface {
	Observe(context.Context, domain.ServiceUnitRecord) (supervisor.UnitObservation, error)
}

type SessionObserver struct {
	processes ports.ProcessManager
	services  serviceObserver
}

func NewSessionObserver(processes ports.ProcessManager, services serviceObserver) *SessionObserver {
	return &SessionObserver{processes: processes, services: services}
}

func (o *SessionObserver) Observe(ctx context.Context, snapshot domain.JournalSnapshot) (app.SessionEvidence, error) {
	if o == nil || o.processes == nil || o.services == nil {
		return app.SessionEvidence{}, errors.New("session observer dependencies are incomplete")
	}
	evidence := app.SessionEvidence{Services: make(map[string]app.ServiceEvidence, len(snapshot.Services))}
	if snapshot.Supervisor.Identity != (domain.ProcessIdentity{}) {
		observed, err := o.observeProcess(ctx, snapshot.Supervisor.Identity)
		if err != nil {
			return app.SessionEvidence{}, fmt.Errorf("observe session supervisor: %w", err)
		}
		evidence.Supervisor = observed
	} else {
		evidence.Supervisor = app.ProcessIdentityUnknown
	}
	for _, service := range snapshot.Services {
		helper, err := o.observeProcess(ctx, service.Helper.Identity)
		if err != nil {
			return app.SessionEvidence{}, fmt.Errorf("observe service %q helper: %w", service.Name, err)
		}
		child, err := o.observeProcess(ctx, service.Child.Identity)
		if err != nil {
			return app.SessionEvidence{}, fmt.Errorf("observe service %q child: %w", service.Name, err)
		}
		evidence.Services[service.Name] = app.ServiceEvidence{Helper: helper, Child: child}
		if helper == app.ProcessIdentityMatch || helper == app.ProcessIdentityAbsent {
			observation, err := o.services.Observe(ctx, service)
			if err != nil {
				return app.SessionEvidence{}, fmt.Errorf("validate service %q listeners: %w", service.Name, err)
			}
			if helper == app.ProcessIdentityMatch && (child != app.ProcessIdentityMatch || observation.State != supervisor.UnitLive || observation.Record.Helper.Identity != service.Helper.Identity || observation.Record.Child.Identity != service.Child.Identity) {
				return app.SessionEvidence{}, fmt.Errorf("service %q live observation changed identity", service.Name)
			}
			if helper == app.ProcessIdentityAbsent && observation.State != supervisor.UnitStopped {
				return app.SessionEvidence{}, fmt.Errorf("service %q stopped observation remained live", service.Name)
			}
		}
	}
	return evidence, nil
}

func (o *SessionObserver) observeProcess(ctx context.Context, expected domain.ProcessIdentity) (app.ProcessIdentityEvidence, error) {
	if err := validateProcessIdentity(expected); err != nil {
		return app.ProcessIdentityUnknown, nil
	}
	status, err := o.processes.Inspect(ctx, expected)
	if err != nil {
		if errors.Is(err, supervisor.ErrProcessIdentity) {
			return app.ProcessIdentityReused, nil
		}
		return app.ProcessIdentityUnknown, err
	}
	if status.Identity != expected {
		return app.ProcessIdentityReused, nil
	}
	if !status.Running {
		return app.ProcessIdentityAbsent, nil
	}
	return app.ProcessIdentityMatch, nil
}
