package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type workspaceCloser interface {
	StopInContext(context.Context, string, string, bool) (ports.Result, error)
	DeleteInContext(context.Context, string, string, bool) (ports.Result, error)
}

type serviceStopper interface {
	Stop(context.Context, domain.ServiceUnitRecord) error
}

type leaseReleaser interface {
	Release(context.Context, coordination.LeaseToken) error
}

type materializationRemover interface {
	RemoveOwned(context.Context, domain.Materialization) (bool, error)
}

type CloseEffects struct {
	workspaces workspaceCloser
	processes  ports.ProcessManager
	services   serviceStopper
	leases     leaseReleaser
	ownership  materializationRemover
}

func NewCloseEffects(workspaces workspaceCloser, processes ports.ProcessManager, services serviceStopper, leases leaseReleaser, ownership materializationRemover) *CloseEffects {
	return &CloseEffects{workspaces: workspaces, processes: processes, services: services, leases: leases, ownership: ownership}
}

func (e *CloseEffects) CloseWorkspace(ctx context.Context, snapshot domain.JournalSnapshot, keep bool) error {
	if e == nil || e.workspaces == nil {
		return errors.New("workspace close dependency is incomplete")
	}
	if snapshot.Workspace.LocalProvider {
		return nil
	}
	if snapshot.Workspace.ID == "" || snapshot.Workspace.Context == "" {
		return errors.New("recorded workspace identity is incomplete")
	}
	if keep || snapshot.Recovery.Cleanup.WorkspaceAction == domain.WorkspaceCleanupStop {
		_, err := e.workspaces.StopInContext(ctx, snapshot.Workspace.Context, snapshot.Workspace.ID, true)
		return err
	}
	if snapshot.Recovery.Cleanup.WorkspaceAction != domain.WorkspaceCleanupDelete {
		return fmt.Errorf("unsupported recorded workspace cleanup action %q", snapshot.Recovery.Cleanup.WorkspaceAction)
	}
	_, err := e.workspaces.DeleteInContext(ctx, snapshot.Workspace.Context, snapshot.Workspace.ID, true)
	return err
}

func (e *CloseEffects) StopForwarders(ctx context.Context, snapshot domain.JournalSnapshot) error {
	if e == nil || e.processes == nil {
		return errors.New("forwarder process dependency is incomplete")
	}
	for _, forwarding := range snapshot.Recovery.Forwarding {
		if err := validateProcessIdentity(forwarding.Process.Identity); err != nil {
			return fmt.Errorf("forwarder %q: %w", forwarding.Name, err)
		}
		if err := e.processes.Stop(ctx, forwarding.Process.Identity, 5*time.Second); err != nil {
			return fmt.Errorf("stop forwarder %q: %w", forwarding.Name, err)
		}
	}
	return nil
}

func (e *CloseEffects) StopServices(ctx context.Context, snapshot domain.JournalSnapshot) error {
	if e == nil || e.services == nil {
		return errors.New("service stop dependency is incomplete")
	}
	for _, service := range snapshot.Services {
		if err := e.services.Stop(ctx, service); err != nil {
			return fmt.Errorf("stop service %q: %w", service.Name, err)
		}
	}
	return nil
}

func (e *CloseEffects) StopSupervisor(ctx context.Context, snapshot domain.JournalSnapshot) error {
	if e == nil || e.processes == nil {
		return errors.New("supervisor process dependency is incomplete")
	}
	if snapshot.Supervisor.Desired == domain.RuntimeDesiredStopped && snapshot.Supervisor.Identity == (domain.ProcessIdentity{}) {
		return nil
	}
	if err := validateProcessIdentity(snapshot.Supervisor.Identity); err != nil {
		return fmt.Errorf("session supervisor: %w", err)
	}
	return e.processes.Stop(ctx, snapshot.Supervisor.Identity, 5*time.Second)
}

func (e *CloseEffects) ReleaseLease(ctx context.Context, snapshot domain.JournalSnapshot) error {
	if e == nil || e.leases == nil || snapshot.Lease.Lease == nil || snapshot.Lease.Revision == "" {
		return errors.New("recorded lease identity is incomplete")
	}
	lease := snapshot.Lease.Lease
	if lease.SessionID != snapshot.SessionID || lease.Capsule != snapshot.Capsule || lease.Lineage != snapshot.Lineage {
		return errors.New("recorded lease does not match the session")
	}
	return e.leases.Release(ctx, coordination.LeaseToken{Lease: *lease, Revision: ports.Revision(snapshot.Lease.Revision)})
}

func (e *CloseEffects) RemoveMaterialization(ctx context.Context, snapshot domain.JournalSnapshot) (bool, error) {
	if e == nil || e.ownership == nil {
		return false, errors.New("materialization ownership dependency is incomplete")
	}
	return e.ownership.RemoveOwned(ctx, snapshot.Materialization)
}

func validateProcessIdentity(identity domain.ProcessIdentity) error {
	if identity.PID <= 0 || identity.BootID == "" || identity.StartTicks == 0 {
		return errors.New("recorded process identity is incomplete")
	}
	return nil
}
