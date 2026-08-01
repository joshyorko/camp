package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

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

type boundedRemoteWriter struct {
	mu       sync.Mutex
	body     []byte
	limit    int
	overflow bool
}

func newBoundedRemoteWriter(limit int) *boundedRemoteWriter {
	return &boundedRemoteWriter{limit: limit}
}

func (writer *boundedRemoteWriter) Write(body []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	remaining := writer.limit - len(writer.body)
	if remaining > 0 {
		if remaining > len(body) {
			remaining = len(body)
		}
		writer.body = append(writer.body, body[:remaining]...)
	}
	if len(body) > remaining {
		writer.overflow = true
	}
	return len(body), nil
}

func (writer *boundedRemoteWriter) Bytes() []byte {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return append([]byte(nil), writer.body...)
}

func (writer *boundedRemoteWriter) Overflowed() bool {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.overflow
}

type RemoteCheckpointWorkerError struct {
	Code       string
	Diagnostic string
}

func (err *RemoteCheckpointWorkerError) Error() string {
	return "remote checkpoint worker failed: " + err.Code + ": " + err.Diagnostic
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
	stdout := newBoundedRemoteWriter(remoteworker.DiagnosticLimit)
	stderr := newBoundedRemoteWriter(4 << 10)
	result, runErr := e.devpod.SSH(ctx, devpodadapter.SSHOptions{
		WorkspaceID: request.WorkspaceID, Context: request.Context, StartServices: false,
		ForwardedArgv: []string{"--command", ".camp-bootstrap/camp-bootstrap __remote-worker"},
		Stdin:         bytes.NewReader(body), Stdout: stdout, Stderr: stderr,
	})
	if stdout.Overflowed() {
		return remoteworker.CheckpointReceipt{}, errors.New("remote checkpoint worker response exceeded the protocol limit")
	}
	if runErr != nil || result.ExitCode != 0 {
		if envelope, err := decodeRemoteCheckpointEnvelope(stdout.Bytes()); err == nil {
			var remoteError remoteworker.ErrorReceipt
			if json.Unmarshal(envelope.Receipt, &remoteError) == nil && remoteError.Status == "error" && remoteError.Code != "" {
				return remoteworker.CheckpointReceipt{}, &RemoteCheckpointWorkerError{
					Code: remoteError.Code, Diagnostic: remoteError.Diagnostic,
				}
			}
		}
		return remoteworker.CheckpointReceipt{}, fmt.Errorf("run remote checkpoint worker: %w: %s", runErr, string(stderr.Bytes()))
	}
	envelope, err := decodeRemoteCheckpointEnvelope(stdout.Bytes())
	if err != nil {
		return remoteworker.CheckpointReceipt{}, err
	}
	var receipt remoteworker.CheckpointReceipt
	if err := json.Unmarshal(envelope.Receipt, &receipt); err != nil {
		return remoteworker.CheckpointReceipt{}, errors.New("remote checkpoint worker returned an invalid receipt")
	}
	return receipt, nil
}

func decodeRemoteCheckpointEnvelope(body []byte) (remoteworker.Result, error) {
	var envelope remoteworker.Result
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return envelope, errors.New("remote checkpoint worker returned an invalid envelope")
	}
	if err := json.Unmarshal(body[:len(body)-1], &envelope); err != nil ||
		envelope.SchemaVersion != remoteworker.ProtocolSchemaVersion ||
		envelope.Operation != remoteworker.OperationCheckpoint {
		return remoteworker.Result{}, errors.New("remote checkpoint worker returned an invalid envelope")
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return remoteworker.Result{}, errors.New("remote checkpoint worker returned trailing or non-canonical output")
	}
	return envelope, nil
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
	expectedWorkspaceRoot := filepath.Join("/workspaces", snapshot.Workspace.ID)
	expectedRuntimeRoot := filepath.Join(expectedWorkspaceRoot, ".camp", "runtime", "bootstrap", snapshot.SessionID)
	if record == nil || record.Mode != domain.DataPlaneHaulerKitV1 ||
		record.RequestSchema != remoteworker.ProtocolSchemaVersion ||
		record.RequestSession != snapshot.SessionID ||
		record.WorkspaceRoot != expectedWorkspaceRoot ||
		record.RuntimeRoot != expectedRuntimeRoot ||
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
