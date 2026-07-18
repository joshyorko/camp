package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

func TestBuildSessionReadModelsRequiresReconciledServiceEvidence(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	snapshot.Services = []domain.ServiceUnitRecord{{Name: "registry"}, {Name: "files"}}

	unknown := BuildSessionReadModels([]domain.JournalSnapshot{snapshot}, nil)
	if got := unknown[0].Services; !reflect.DeepEqual(got, []ServiceReadModel{{Name: "files", Liveness: ServiceLivenessUnknown}, {Name: "registry", Liveness: ServiceLivenessUnknown}}) {
		t.Fatalf("services without evidence = %#v", got)
	}

	evidence := map[string]SessionEvidence{snapshot.SessionID: {Services: map[string]ServiceEvidence{
		"registry": {Helper: ProcessIdentityMatch, Child: ProcessIdentityMatch},
		"files":    {Helper: ProcessIdentityMatch, Child: ProcessIdentityReused},
	}}}
	models := BuildSessionReadModels([]domain.JournalSnapshot{snapshot}, evidence)
	if got := models[0].Services; !reflect.DeepEqual(got, []ServiceReadModel{{Name: "files", Liveness: ServiceLivenessPIDReused}, {Name: "registry", Liveness: ServiceLivenessLive}}) {
		t.Fatalf("reconciled services = %#v", got)
	}
}

func TestBuildSessionReadModelsKeepsPublicationCleanupAndRecoverySeparate(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	snapshot.State = domain.SessionClosed
	snapshot.Checkpoint = domain.Checkpoint{State: domain.CheckpointPublished, PublicationSucceeded: true, Generation: &domain.GenerationRef{Generation: 44, ArchiveSHA256: "digest"}}
	snapshot.Cleanup = domain.Cleanup{State: domain.CleanupFailed, LastErr: "credential=secret"}

	models := BuildSessionReadModels([]domain.JournalSnapshot{snapshot}, map[string]SessionEvidence{snapshot.SessionID: {}})
	model := models[0]
	if model.Publication.Condition != PublicationPublished || model.Publication.Generation != 44 {
		t.Fatalf("publication = %#v", model.Publication)
	}
	if model.Cleanup.Condition != CleanupFailed || model.Cleanup.Message != "credential=secret" {
		t.Fatalf("cleanup = %#v", model.Cleanup)
	}
	if model.Recovery.Condition != RecoveryCleanupOnly || model.Recovery.Command != "camp recover session-b" {
		t.Fatalf("recovery = %#v", model.Recovery)
	}
}

func TestBuildSessionReadModelsClassifiesOrphanAndPointerConflict(t *testing.T) {
	t.Parallel()
	orphan := readModelSnapshot()
	orphan.SessionID = "orphan"
	orphan.Checkpoint = domain.Checkpoint{State: domain.CheckpointUploaded, Generation: &domain.GenerationRef{Generation: 45}}
	conflict := readModelSnapshot()
	conflict.SessionID = "conflict"
	conflict.Checkpoint = domain.Checkpoint{State: domain.CheckpointUploaded, Generation: &domain.GenerationRef{Generation: 46}}

	models := BuildSessionReadModels([]domain.JournalSnapshot{orphan, conflict}, map[string]SessionEvidence{"conflict": {PointerConflict: true}})
	if models[0].ID != "conflict" || models[0].Publication.Condition != PublicationPointerConflict || models[1].ID != "orphan" || models[1].Publication.Condition != PublicationOrphaned {
		t.Fatalf("models = %#v", models)
	}
}

func TestBuildSessionReadModelsDistinguishesStoppedAndDeadServices(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	snapshot.Services = []domain.ServiceUnitRecord{
		{Name: "stopped", DesiredState: domain.RuntimeDesiredStopped},
		{Name: "dead", DesiredState: domain.RuntimeDesiredRunning},
	}
	evidence := map[string]SessionEvidence{snapshot.SessionID: {Services: map[string]ServiceEvidence{
		"stopped": {Helper: ProcessIdentityAbsent, Child: ProcessIdentityAbsent},
		"dead":    {Helper: ProcessIdentityAbsent, Child: ProcessIdentityAbsent},
	}}}
	models := BuildSessionReadModels([]domain.JournalSnapshot{snapshot}, evidence)
	want := []ServiceReadModel{{Name: "dead", Liveness: ServiceLivenessDead}, {Name: "stopped", Liveness: ServiceLivenessStopped}}
	if !reflect.DeepEqual(models[0].Services, want) {
		t.Fatalf("services = %#v, want %#v", models[0].Services, want)
	}
}

func readModelSnapshot() domain.JournalSnapshot {
	return domain.JournalSnapshot{
		SessionID: "session-b", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen,
		Mode: domain.SessionReadWrite, Workspace: domain.WorkspaceRecord{ID: "brain.devpod", Provider: "docker"},
		Materialization: domain.Materialization{CanonicalPath: "/work/brain"}, Cleanup: domain.Cleanup{State: domain.CleanupPending},
		CreatedAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 18, 12, 1, 0, 0, time.UTC),
	}
}
