package app

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

type strikeJournal struct{ sessions []domain.JournalSnapshot }

func (j strikeJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	return j.sessions, nil
}

type strikeController struct{ called bool }

func (c *strikeController) Archive(context.Context, StrikePlan) (string, error) {
	c.called = true
	return "/tmp/archive", nil
}
func (c *strikeController) Purge(context.Context, StrikePlan) error { c.called = true; return nil }

type strikeEffects struct {
	workspace, forwarders, services, supervisor, materialization int
}

func (e *strikeEffects) CloseWorkspace(context.Context, domain.JournalSnapshot, bool) error {
	e.workspace++
	return nil
}
func (e *strikeEffects) StopForwarders(context.Context, domain.JournalSnapshot) error {
	e.forwarders++
	return nil
}
func (e *strikeEffects) StopServices(context.Context, domain.JournalSnapshot) error {
	e.services++
	return nil
}
func (e *strikeEffects) StopSupervisor(context.Context, domain.JournalSnapshot) error {
	e.supervisor++
	return nil
}
func (e *strikeEffects) RemoveSessionArtifacts(context.Context, domain.JournalSnapshot) error {
	return nil
}
func (e *strikeEffects) ReleaseLease(context.Context, domain.JournalSnapshot) error { return nil }
func (e *strikeEffects) RemoveMaterialization(context.Context, domain.JournalSnapshot) (bool, error) {
	e.materialization++
	return true, nil
}

func TestStrikeQuiescesActiveSessionBeforeArchive(t *testing.T) {
	controller := &strikeController{}
	effects := &strikeEffects{}
	session := domain.JournalSnapshot{
		State:           domain.SessionOpen,
		Workspace:       domain.WorkspaceRecord{ID: "workspace", Context: "default"},
		Services:        []domain.ServiceUnitRecord{{Name: "registry"}},
		Recovery:        domain.RecoveryRecord{Forwarding: []domain.ForwardingRecord{{Name: "registry"}}},
		Supervisor:      domain.SupervisorRecord{Identity: domain.ProcessIdentity{PID: 1, BootID: "boot", StartTicks: 1}},
		Materialization: domain.Materialization{SchemaVersion: domain.SchemaVersion, Mode: domain.MaterializationCreated, CleanupPermitted: true},
	}
	_, err := (Strike{Sessions: strikeJournal{sessions: []domain.JournalSnapshot{session}}, Controller: controller, Effects: effects}).Run(context.Background(), StrikeRequest{}, StrikePlan{BackendSafe: true})
	if err != nil || !controller.called {
		t.Fatalf("active session was not quiesced and archived: %v", err)
	}
	if effects.workspace != 1 || effects.forwarders != 1 || effects.services != 1 || effects.supervisor != 1 || effects.materialization != 0 {
		t.Fatalf("cleanup calls = %+v", effects)
	}
}

func TestStrikePurgeRequiresOwnedMaterializationRemoval(t *testing.T) {
	controller := &strikeController{}
	effects := &strikeEffects{}
	session := domain.JournalSnapshot{
		State:           domain.SessionOpen,
		Materialization: domain.Materialization{SchemaVersion: domain.SchemaVersion, Mode: domain.MaterializationCreated, CleanupPermitted: true},
	}
	_, err := (Strike{Sessions: strikeJournal{sessions: []domain.JournalSnapshot{session}}, Controller: controller, Effects: effects}).Run(context.Background(), StrikeRequest{Purge: true, Yes: true}, StrikePlan{BackendSafe: true})
	if err != nil || !controller.called || effects.materialization != 1 {
		t.Fatalf("purge did not verify materialization removal: effects=%+v called=%t err=%v", effects, controller.called, err)
	}
}

func TestStrikeSkipsIncompleteEffectsForAbandonedOpeningSession(t *testing.T) {
	controller := &strikeController{}
	effects := &strikeEffects{}
	abandoned := domain.JournalSnapshot{State: domain.SessionOpening, Workspace: domain.WorkspaceRecord{Context: "default"}}
	_, err := (Strike{Sessions: strikeJournal{sessions: []domain.JournalSnapshot{abandoned}}, Controller: controller, Effects: effects}).Run(context.Background(), StrikeRequest{}, StrikePlan{BackendSafe: true})
	if err != nil || !controller.called {
		t.Fatalf("abandoned opening session blocked strike: %v", err)
	}
	if *effects != (strikeEffects{}) {
		t.Fatalf("incomplete effects were invoked: %+v", effects)
	}
}

func TestStrikeRequiresPurgeConfirmation(t *testing.T) {
	controller := &strikeController{}
	_, err := (Strike{Sessions: strikeJournal{}, Controller: controller}).Run(context.Background(), StrikeRequest{Purge: true}, StrikePlan{})
	if err == nil || controller.called {
		t.Fatal("unconfirmed purge was not rejected")
	}
}

func TestStrikeArchivesClosedState(t *testing.T) {
	controller := &strikeController{}
	result, err := (Strike{Sessions: strikeJournal{}, Controller: controller}).Run(context.Background(), StrikeRequest{}, StrikePlan{BackendSafe: true})
	if err != nil || !controller.called || result.ArchivedPath == "" {
		t.Fatalf("archive: result=%+v err=%v", result, err)
	}
}

func TestStrikeRejectsUnsupportedBackend(t *testing.T) {
	controller := &strikeController{}
	_, err := (Strike{Sessions: strikeJournal{}, Controller: controller}).Run(context.Background(), StrikeRequest{}, StrikePlan{BackendSafe: false})
	if !errors.Is(err, ErrStrikeBackend) || controller.called {
		t.Fatalf("backend rejection: %v", err)
	}
}
