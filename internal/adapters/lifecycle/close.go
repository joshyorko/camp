package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"golang.org/x/sys/unix"
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
		if err := NewForwarderManager(nil, e.processes).Stop(ctx, forwarding); err != nil {
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
	err := e.processes.Stop(ctx, snapshot.Supervisor.Identity, 5*time.Second)
	if errors.Is(err, supervisor.ErrProcessIdentity) {
		return nil
	}
	return err
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

func (e *CloseEffects) RemoveSessionArtifacts(ctx context.Context, snapshot domain.JournalSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !snapshot.Recovery.Cleanup.RemoveSessionArtifacts {
		return errors.New("session artifact cleanup is not authorized")
	}
	runtimeRoot := filepath.Clean(snapshot.Recovery.Session.RuntimeRoot)
	sessionRoot := filepath.Clean(snapshot.Recovery.Session.Root)
	if !filepath.IsAbs(runtimeRoot) || runtimeRoot == string(filepath.Separator) || snapshot.SessionID == "" {
		return errors.New("recorded session runtime root is unsafe")
	}
	if runtimeRoot != filepath.Join(sessionRoot, "runtime") && filepath.Base(runtimeRoot) != snapshot.SessionID {
		return errors.New("recorded session runtime root does not match the session")
	}
	info, err := os.Lstat(runtimeRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("recorded session runtime root is not a real directory")
	}

	allowed := map[string]struct{}{"store-seed": {}}
	for _, service := range snapshot.Services {
		expected := filepath.Join(runtimeRoot, service.Name+".log")
		if filepath.Clean(service.LogPath) != expected {
			return fmt.Errorf("service %q log is outside the session runtime root", service.Name)
		}
		allowed[filepath.Base(expected)] = struct{}{}
	}
	for _, forwarding := range snapshot.Recovery.Forwarding {
		expectedEvidence := filepath.Join(runtimeRoot, forwarding.Name+"-forward.json")
		if filepath.Clean(forwarding.EvidencePath) != expectedEvidence {
			return fmt.Errorf("forwarder %q evidence is outside the session runtime root", forwarding.Name)
		}
		allowed[forwarding.Name+"-forward.log"] = struct{}{}
	}
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return fmt.Errorf("unexpected session runtime artifact %q", entry.Name())
		}
		var stat unix.Stat_t
		path := filepath.Join(runtimeRoot, entry.Name())
		if err := unix.Lstat(path, &stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
			return fmt.Errorf("session runtime artifact %q is not an owned regular file", entry.Name())
		}
		if entry.Name() == "store-seed" {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(body) != snapshot.SessionID+"\n" {
				return errors.New("session runtime ownership seed does not match the session")
			}
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(runtimeRoot, entry.Name())); err != nil {
			return err
		}
	}
	if err := os.Remove(runtimeRoot); err != nil {
		return err
	}
	if filepath.Base(runtimeRoot) == snapshot.SessionID {
		if err := os.Remove(filepath.Dir(runtimeRoot)); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOTEMPTY) {
			return err
		}
	}
	return nil
}

func validateProcessIdentity(identity domain.ProcessIdentity) error {
	if identity.PID <= 0 || identity.BootID == "" || identity.StartTicks == 0 {
		return errors.New("recorded process identity is incomplete")
	}
	return nil
}
