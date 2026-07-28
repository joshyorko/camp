package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/remoteworker"
)

type RemoteCheckpointRequest struct {
	SessionID     string
	AttemptID     string
	Capsule       string
	Lineage       domain.Lineage
	Generation    uint64
	Close         bool
	WorkspaceID   string
	Context       string
	WorkspaceRoot string
	RuntimeRoot   string
	ManifestPath  string
	DataPlane     domain.RemoteDataPlaneRecord
}

type RemoteCheckpointExecutor interface {
	Prepare(context.Context, RemoteCheckpointRequest) (remoteworker.CheckpointReceipt, error)
}

type RemoteCheckpointPreparer struct {
	executor RemoteCheckpointExecutor
}

func NewRemoteCheckpointPreparer(executor RemoteCheckpointExecutor) *RemoteCheckpointPreparer {
	return &RemoteCheckpointPreparer{executor: executor}
}

func (p *RemoteCheckpointPreparer) Prepare(ctx context.Context, snapshot domain.JournalSnapshot, generation uint64, close bool) (remoteworker.CheckpointReceipt, error) {
	if p == nil || p.executor == nil || generation == 0 {
		return remoteworker.CheckpointReceipt{}, errors.New("remote checkpoint dependencies or generation are incomplete")
	}
	record := snapshot.Recovery.RemoteDataPlane
	if record == nil || record.Mode != domain.DataPlaneHaulerKitV1 ||
		record.RequestSchema != remoteworker.ProtocolSchemaVersion ||
		record.RequestSession != snapshot.SessionID ||
		record.WorkspaceRoot != filepath.Join("/workspaces", snapshot.Capsule) ||
		record.RuntimeRoot != filepath.Join("/var/lib/camp", snapshot.SessionID) ||
		record.ManifestPath != filepath.Join(record.RuntimeRoot, "camp-hauler-kit.json") ||
		record.AttemptID != snapshot.SessionID+"-hauler-kit-v1" ||
		snapshot.SessionID == "" || snapshot.Capsule == "" ||
		snapshot.Lineage.Branch == "" || snapshot.Workspace.ID == "" || snapshot.Workspace.Context == "" {
		return remoteworker.CheckpointReceipt{}, errors.New("remote checkpoint identity does not match the persisted Hauler data plane")
	}
	attemptID := snapshot.SessionID + "-checkpoint-" + strconv.FormatUint(generation, 10)
	receipt, err := p.executor.Prepare(ctx, RemoteCheckpointRequest{
		SessionID: snapshot.SessionID, AttemptID: attemptID, Capsule: snapshot.Capsule,
		Lineage: snapshot.Lineage, Generation: generation, Close: close,
		WorkspaceID: snapshot.Workspace.ID, Context: snapshot.Workspace.Context,
		WorkspaceRoot: record.WorkspaceRoot, RuntimeRoot: record.RuntimeRoot,
		ManifestPath: record.ManifestPath, DataPlane: *record,
	})
	if err != nil {
		return receipt, err
	}
	if receipt.SchemaVersion != remoteworker.ProtocolSchemaVersion || receipt.Status != "prepared" ||
		receipt.SessionID != snapshot.SessionID || receipt.AttemptID != attemptID {
		return remoteworker.CheckpointReceipt{}, fmt.Errorf("remote checkpoint returned a drifted attempt receipt")
	}
	return receipt, nil
}
