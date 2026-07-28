package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
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

type remoteCheckpointSSH interface {
	SSH(context.Context, devpodadapter.SSHOptions) (ports.Result, error)
}

type DevPodRemoteCheckpointExecutor struct {
	devpod remoteCheckpointSSH
}

func NewDevPodRemoteCheckpointExecutor(devpod remoteCheckpointSSH) *DevPodRemoteCheckpointExecutor {
	return &DevPodRemoteCheckpointExecutor{devpod: devpod}
}

func (e *DevPodRemoteCheckpointExecutor) Prepare(ctx context.Context, request RemoteCheckpointRequest) (remoteworker.CheckpointReceipt, error) {
	record := request.DataPlane
	if e == nil || e.devpod == nil || request.SessionID == "" || request.AttemptID == "" ||
		request.WorkspaceID == "" || request.Context == "" || record.Mode != domain.DataPlaneHaulerKitV1 ||
		record.HelperSHA256 == "" || record.HelperSize <= 0 || record.KitSHA256 == "" || record.KitSize <= 0 ||
		record.ManifestSHA256 == "" || record.ManifestSize <= 0 {
		return remoteworker.CheckpointReceipt{}, errors.New("DevPod remote checkpoint executor dependencies or identity are incomplete")
	}
	workerRequest := remoteworker.Request{
		SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: remoteworker.OperationCheckpoint,
		SessionID: request.SessionID, WorkspaceRoot: request.WorkspaceRoot,
		RuntimeRoot: request.RuntimeRoot, ManifestPath: request.ManifestPath,
		Expected: remoteworker.ExpectedIdentity{
			Architecture: record.Architecture,
			Helper:       remoteworker.FileIdentity{Name: "camp", SHA256: record.HelperSHA256, Size: record.HelperSize},
			Kit:          remoteworker.FileIdentity{Name: "camp-hauler-kit.tar.zst", SHA256: record.KitSHA256, Size: record.KitSize},
			Manifest:     remoteworker.FileIdentity{Name: "camp-hauler-kit.json", SHA256: record.ManifestSHA256, Size: record.ManifestSize},
			SourceImage:  record.SourceImage, Image: record.OuterImage,
		},
		Checkpoint: &remoteworker.CheckpointRequest{
			AttemptID: request.AttemptID, Capsule: request.Capsule, Lineage: request.Lineage,
			Generation: request.Generation, Close: request.Close,
		},
	}
	body, err := json.Marshal(workerRequest)
	if err != nil {
		return remoteworker.CheckpointReceipt{}, err
	}
	var stdout, stderr bytes.Buffer
	result, runErr := e.devpod.SSH(ctx, devpodadapter.SSHOptions{
		WorkspaceID: request.WorkspaceID, Context: request.Context, StartServices: false,
		ForwardedArgv: []string{"--command", ".camp-bootstrap/camp-bootstrap __remote-worker"},
		Stdin:         bytes.NewReader(body), Stdout: &stdout, Stderr: &stderr,
	})
	if runErr != nil || result.ExitCode != 0 {
		return remoteworker.CheckpointReceipt{}, fmt.Errorf("run remote checkpoint worker: %w: %s", runErr, boundedRemoteDiagnostic(stderr.Bytes()))
	}
	var envelope remoteworker.Result
	decoder := json.NewDecoder(io.LimitReader(&stdout, remoteworker.DiagnosticLimit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.SchemaVersion != remoteworker.ProtocolSchemaVersion ||
		envelope.Operation != remoteworker.OperationCheckpoint {
		return remoteworker.CheckpointReceipt{}, errors.New("remote checkpoint worker returned an invalid envelope")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return remoteworker.CheckpointReceipt{}, errors.New("remote checkpoint worker returned trailing output")
	}
	var receipt remoteworker.CheckpointReceipt
	if err := json.Unmarshal(envelope.Receipt, &receipt); err != nil {
		return remoteworker.CheckpointReceipt{}, errors.New("remote checkpoint worker returned an invalid receipt")
	}
	return receipt, nil
}

func boundedRemoteDiagnostic(body []byte) string {
	if len(body) > 4<<10 {
		body = body[:4<<10]
	}
	return string(body)
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
