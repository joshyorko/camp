package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/remoteworker"
)

func (o *Open) observeRemoteStartup(ctx context.Context, snapshot domain.JournalSnapshot, input openWorkspaceUpInput) error {
	record := snapshot.Recovery.RemoteDataPlane
	if record == nil || record.Mode != domain.DataPlaneHaulerKitV1 {
		return nil
	}
	if record.RequestSchema != remoteworker.ProtocolSchemaVersion || record.RequestSession != snapshot.SessionID ||
		record.WorkspaceRoot == "" || record.RuntimeRoot == "" || record.ManifestPath == "" ||
		record.HelperSHA256 == "" || record.HelperSize <= 0 || record.KitSHA256 == "" || record.KitSize <= 0 ||
		record.ManifestSHA256 == "" || record.ManifestSize <= 0 || record.SourceImage == "" || record.OuterImage == "" ||
		!validLifecycleUser(record.LifecycleUser) ||
		input.ID == "" || input.Context == "" {
		return errors.New("remote startup observation identity is incomplete")
	}
	expected := remoteworker.ExpectedIdentity{
		Architecture: record.Architecture,
		Helper:       remoteworker.FileIdentity{Name: "camp", SHA256: record.HelperSHA256, Size: record.HelperSize},
		Kit:          remoteworker.FileIdentity{Name: "camp-hauler-kit.tar.zst", SHA256: record.KitSHA256, Size: record.KitSize},
		Manifest:     remoteworker.FileIdentity{Name: "camp-hauler-kit.json", SHA256: record.ManifestSHA256, Size: record.ManifestSize},
		SourceImage:  record.SourceImage,
		Image:        record.OuterImage,
	}
	request := remoteworker.Request{
		SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: remoteworker.OperationObserve,
		SessionID: snapshot.SessionID, WorkspaceRoot: record.WorkspaceRoot, RuntimeRoot: record.RuntimeRoot,
		ManifestPath: record.ManifestPath, Expected: expected,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	stdout := newBoundedRemoteWriter(remoteworker.DiagnosticLimit)
	stderr := newBoundedRemoteWriter(4 << 10)
	result, runErr := o.deps.DevPod.SSH(ctx, devpodadapter.SSHOptions{
		WorkspaceID: input.ID, Context: input.Context, User: record.LifecycleUser, StartServices: false,
		ForwardedArgv: []string{"--command", ".camp-bootstrap/camp-bootstrap __remote-worker"},
		Stdin:         bytes.NewReader(body), Stdout: stdout, Stderr: stderr,
	})
	if stdout.Overflowed() {
		return errors.New("remote startup observation exceeded the protocol limit")
	}
	if runErr != nil || result.ExitCode != 0 {
		if envelope, decodeErr := decodeRemoteWorkerEnvelope(stdout.Bytes(), remoteworker.OperationObserve); decodeErr == nil {
			var remoteError remoteworker.ErrorReceipt
			if json.Unmarshal(envelope.Receipt, &remoteError) == nil && remoteError.Status == "error" && remoteError.Code != "" && remoteError.Diagnostic != "" {
				return fmt.Errorf("observe remote startup receipts: %s: %s", remoteError.Code, remoteError.Diagnostic)
			}
		}
		return fmt.Errorf("observe remote startup receipts: %w: %s", runErr, string(stderr.Bytes()))
	}
	envelope, err := decodeRemoteWorkerEnvelope(stdout.Bytes(), remoteworker.OperationObserve)
	if err != nil {
		return err
	}
	var receipt remoteworker.StartupReceipt
	if err := json.Unmarshal(envelope.Receipt, &receipt); err != nil {
		return errors.New("remote startup worker returned an invalid receipt")
	}
	if receipt.Status != "ready" || receipt.Activation.Status != "completed" ||
		receipt.Activation.SourceImage != expected.SourceImage || receipt.Activation.LocalImage != expected.Image ||
		receipt.Hydration.Status != "completed" || receipt.Hydration.SessionID != request.SessionID ||
		receipt.Hydration.WorkspaceRoot != request.WorkspaceRoot || receipt.Hydration.RuntimeRoot != request.RuntimeRoot ||
		receipt.Hydration.ManifestPath != request.ManifestPath || receipt.Hydration.Expected != expected ||
		!validRemoteStartupDigest(receipt.Hydration.RootSHA256) {
		return errors.New("remote startup receipt identity does not match the persisted Hauler data plane")
	}
	return nil
}

func decodeRemoteWorkerEnvelope(body []byte, operation remoteworker.Operation) (remoteworker.Result, error) {
	var envelope remoteworker.Result
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return envelope, errors.New("remote worker returned an invalid envelope")
	}
	if err := json.Unmarshal(body[:len(body)-1], &envelope); err != nil ||
		envelope.SchemaVersion != remoteworker.ProtocolSchemaVersion || envelope.Operation != operation {
		return remoteworker.Result{}, errors.New("remote worker returned an invalid envelope")
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return remoteworker.Result{}, errors.New("remote worker returned trailing or non-canonical output")
	}
	return envelope, nil
}

func validRemoteStartupDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
