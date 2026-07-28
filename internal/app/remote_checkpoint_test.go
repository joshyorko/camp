package app

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/remoteworker"
)

type fakeRemoteCheckpointExecutor struct {
	request RemoteCheckpointRequest
	result  remoteworker.CheckpointReceipt
	err     error
}

func (f *fakeRemoteCheckpointExecutor) Prepare(_ context.Context, request RemoteCheckpointRequest) (remoteworker.CheckpointReceipt, error) {
	f.request = request
	return f.result, f.err
}

func TestRemoteCheckpointPreparerBindsImmutableAttemptAndSyncDisposition(t *testing.T) {
	snapshot := remoteCheckpointSnapshot()
	executor := &fakeRemoteCheckpointExecutor{result: remoteworker.CheckpointReceipt{
		SchemaVersion: remoteworker.ProtocolSchemaVersion, Status: "prepared",
		SessionID: snapshot.SessionID, AttemptID: "session-1-checkpoint-1",
	}}
	preparer := NewRemoteCheckpointPreparer(executor)
	receipt, err := preparer.Prepare(context.Background(), snapshot, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.AttemptID != "session-1-checkpoint-1" || executor.request.Close ||
		executor.request.WorkspaceID != snapshot.Workspace.ID ||
		executor.request.DataPlane != *snapshot.Recovery.RemoteDataPlane {
		t.Fatalf("request=%#v receipt=%#v", executor.request, receipt)
	}
}

func TestRemoteCheckpointPreparerRetryAdoptsSameAttempt(t *testing.T) {
	snapshot := remoteCheckpointSnapshot()
	executor := &fakeRemoteCheckpointExecutor{err: errors.New("outcome unknown")}
	preparer := NewRemoteCheckpointPreparer(executor)
	for range 2 {
		_, _ = preparer.Prepare(context.Background(), snapshot, 7, true)
		if executor.request.AttemptID != "session-1-checkpoint-7" || !executor.request.Close {
			t.Fatalf("request = %#v", executor.request)
		}
	}
}

func TestRemoteCheckpointPreparerRejectsLegacyOrDriftedIdentity(t *testing.T) {
	for _, mutate := range []func(*domain.JournalSnapshot){
		func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.RemoteDataPlane = nil },
		func(snapshot *domain.JournalSnapshot) {
			snapshot.Recovery.RemoteDataPlane.Mode = domain.DataPlaneLegacyMirror
		},
		func(snapshot *domain.JournalSnapshot) { snapshot.Recovery.RemoteDataPlane.RequestSession = "other" },
		func(snapshot *domain.JournalSnapshot) {
			snapshot.Recovery.RemoteDataPlane.WorkspaceRoot = "/workspaces/other"
		},
	} {
		snapshot := remoteCheckpointSnapshot()
		mutate(&snapshot)
		executor := &fakeRemoteCheckpointExecutor{}
		if _, err := NewRemoteCheckpointPreparer(executor).Prepare(context.Background(), snapshot, 1, false); err == nil {
			t.Fatal("accepted invalid remote checkpoint identity")
		}
		if executor.request.AttemptID != "" {
			t.Fatal("executor ran before identity validation")
		}
	}
}

func remoteCheckpointSnapshot() domain.JournalSnapshot {
	record := &domain.RemoteDataPlaneRecord{
		Mode: domain.DataPlaneHaulerKitV1, AttemptID: "session-1-hauler-kit-v1",
		RequestSession: "session-1", WorkspaceRoot: "/workspaces/brain",
		RuntimeRoot: "/var/lib/camp/session-1", ManifestPath: "/var/lib/camp/session-1/camp-hauler-kit.json",
		RequestSchema: remoteworker.ProtocolSchemaVersion, Architecture: "linux/amd64",
	}
	return domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-1", Capsule: "brain",
		Lineage:   domain.Lineage{Branch: "main"},
		Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default"},
		Recovery:  domain.RecoveryRecord{RemoteDataPlane: record},
	}
}
