package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/remoteworker"
)

type fakeRemoteCheckpointExecutor struct {
	request RemoteCheckpointRequest
	result  remoteworker.CheckpointReceipt
	err     error
}

type fakeRemoteCheckpointSSH struct {
	options devpodadapter.SSHOptions
	result  ports.Result
	err     error
	chunks  [][]byte
}

func (f *fakeRemoteCheckpointSSH) SSH(_ context.Context, options devpodadapter.SSHOptions) (ports.Result, error) {
	f.options = options
	if options.Stdout != nil {
		if len(f.chunks) == 0 {
			_, _ = options.Stdout.Write(f.result.Stdout)
		} else {
			for _, chunk := range f.chunks {
				_, _ = options.Stdout.Write(chunk)
			}
		}
	}
	if options.Stderr != nil {
		_, _ = options.Stderr.Write(f.result.Stderr)
	}
	return f.result, f.err
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

func TestDevPodRemoteCheckpointExecutorUsesStrictWorkerEnvelope(t *testing.T) {
	snapshot := remoteCheckpointSnapshot()
	record := *snapshot.Recovery.RemoteDataPlane
	receipt := remoteworker.CheckpointReceipt{
		SchemaVersion: remoteworker.ProtocolSchemaVersion, Status: "prepared",
		SessionID: snapshot.SessionID, AttemptID: "session-1-checkpoint-1",
	}
	receiptBody, _ := json.Marshal(receipt)
	envelope, _ := json.Marshal(remoteworker.Result{
		SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: remoteworker.OperationCheckpoint,
		Receipt: receiptBody,
	})
	envelope = append(envelope, '\n')
	ssh := &fakeRemoteCheckpointSSH{result: ports.Result{Stdout: envelope}}
	executor := NewDevPodRemoteCheckpointExecutor(ssh)
	got, err := executor.Prepare(context.Background(), RemoteCheckpointRequest{
		SessionID: snapshot.SessionID, AttemptID: receipt.AttemptID, Capsule: snapshot.Capsule,
		Lineage: snapshot.Lineage, Generation: 1, WorkspaceID: snapshot.Workspace.ID,
		Context: snapshot.Workspace.Context, WorkspaceRoot: record.WorkspaceRoot,
		RuntimeRoot: record.RuntimeRoot, ManifestPath: record.ManifestPath, DataPlane: record,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptID != receipt.AttemptID || ssh.options.StartServices ||
		len(ssh.options.ForwardedArgv) != 2 ||
		ssh.options.ForwardedArgv[1] != ".camp-bootstrap/camp-bootstrap __remote-worker" {
		t.Fatalf("receipt=%#v options=%#v", got, ssh.options)
	}
	var request remoteworker.Request
	if err := json.NewDecoder(ssh.options.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Operation != remoteworker.OperationCheckpoint || request.Checkpoint == nil ||
		request.Checkpoint.AttemptID != receipt.AttemptID ||
		request.Expected.Helper.SHA256 != record.HelperSHA256 {
		t.Fatalf("request = %#v", request)
	}
	if _, ok := ssh.options.Stdout.(*boundedRemoteWriter); !ok {
		t.Fatal("executor did not capture the strict response envelope")
	}
}

func TestDevPodRemoteCheckpointExecutorBoundsStreamingOutputAndRejectsTrailingBytes(t *testing.T) {
	valid := remoteCheckpointEnvelope(t, "")
	tests := []struct {
		name    string
		body    []byte
		chunks  [][]byte
		wantErr bool
	}{
		{name: "streamed", chunks: oneByteChunks(valid)},
		{name: "trailing whitespace", body: append(append([]byte(nil), valid...), ' '), wantErr: true},
		{name: "oversized", chunks: repeatedChunks(remoteworker.DiagnosticLimit + 1), wantErr: true},
		{name: "exact limit", body: remoteCheckpointEnvelopeAtSize(t, remoteworker.DiagnosticLimit)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ssh := &fakeRemoteCheckpointSSH{result: ports.Result{Stdout: test.body}, chunks: test.chunks}
			_, err := NewDevPodRemoteCheckpointExecutor(ssh).Prepare(context.Background(), remoteCheckpointExecutorRequest())
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v", err)
			}
			if writer, ok := ssh.options.Stdout.(*boundedRemoteWriter); ok && len(writer.Bytes()) > remoteworker.DiagnosticLimit {
				t.Fatalf("captured %d bytes", len(writer.Bytes()))
			}
		})
	}
}

func TestDevPodRemoteCheckpointExecutorReturnsTypedBoundedRemoteError(t *testing.T) {
	receipt, _ := json.Marshal(remoteworker.ErrorReceipt{Status: "error", Code: "identity_mismatch", Diagnostic: "drift"})
	body, _ := json.Marshal(remoteworker.Result{
		SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: remoteworker.OperationCheckpoint, Receipt: receipt,
	})
	body = append(body, '\n')
	ssh := &fakeRemoteCheckpointSSH{result: ports.Result{ExitCode: 1, Stdout: body, Stderr: make([]byte, 8<<10)}}
	_, err := NewDevPodRemoteCheckpointExecutor(ssh).Prepare(context.Background(), remoteCheckpointExecutorRequest())
	var remoteErr *RemoteCheckpointWorkerError
	if !errors.As(err, &remoteErr) || remoteErr.Code != "identity_mismatch" {
		t.Fatalf("error = %v", err)
	}
	if writer := ssh.options.Stderr.(*boundedRemoteWriter); len(writer.Bytes()) != 4<<10 || !writer.Overflowed() {
		t.Fatalf("stderr bytes=%d overflow=%v", len(writer.Bytes()), writer.Overflowed())
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
		HelperSHA256: testAppSHA("a"), HelperSize: 10,
		KitSHA256: testAppSHA("b"), KitSize: 20,
		ManifestSHA256: testAppSHA("c"), ManifestSize: 30,
		SourceImage: "example.test/image@sha256:" + testAppSHA("d"),
		OuterImage:  "sha256:" + testAppSHA("e"),
	}
	return domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-1", Capsule: "brain",
		Lineage:   domain.Lineage{Branch: "main"},
		Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default"},
		Recovery:  domain.RecoveryRecord{RemoteDataPlane: record},
	}
}

func testAppSHA(value string) string {
	return value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value
}

func remoteCheckpointExecutorRequest() RemoteCheckpointRequest {
	snapshot := remoteCheckpointSnapshot()
	record := *snapshot.Recovery.RemoteDataPlane
	return RemoteCheckpointRequest{
		SessionID: snapshot.SessionID, AttemptID: "session-1-checkpoint-1", Capsule: snapshot.Capsule,
		Lineage: snapshot.Lineage, Generation: 1, WorkspaceID: snapshot.Workspace.ID,
		Context: snapshot.Workspace.Context, WorkspaceRoot: record.WorkspaceRoot,
		RuntimeRoot: record.RuntimeRoot, ManifestPath: record.ManifestPath, DataPlane: record,
	}
}

func remoteCheckpointEnvelope(t *testing.T, pad string) []byte {
	t.Helper()
	receipt, err := json.Marshal(struct {
		SchemaVersion uint32 `json:"schemaVersion"`
		Status        string `json:"status"`
		SessionID     string `json:"sessionId"`
		AttemptID     string `json:"attemptId"`
		Pad           string `json:"pad,omitempty"`
	}{remoteworker.ProtocolSchemaVersion, "prepared", "session-1", "session-1-checkpoint-1", pad})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(remoteworker.Result{
		SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: remoteworker.OperationCheckpoint, Receipt: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func remoteCheckpointEnvelopeAtSize(t *testing.T, size int) []byte {
	t.Helper()
	base := remoteCheckpointEnvelope(t, "")
	padding := size - len(base) - len(`,"pad":""`)
	if padding < 0 {
		t.Fatal("target is smaller than the envelope")
	}
	for {
		body := remoteCheckpointEnvelope(t, strings.Repeat("a", padding))
		if len(body) == size {
			return body
		}
		padding += size - len(body)
		if padding < 0 {
			t.Fatal("could not size envelope")
		}
	}
}

func oneByteChunks(body []byte) [][]byte {
	chunks := make([][]byte, len(body))
	for index := range body {
		chunks[index] = body[index : index+1]
	}
	return chunks
}

func repeatedChunks(size int) [][]byte {
	chunks := make([][]byte, 0, size/257+1)
	for size > 0 {
		count := 257
		if size < count {
			count = size
		}
		chunks = append(chunks, []byte(strings.Repeat("x", count)))
		size -= count
	}
	return chunks
}
