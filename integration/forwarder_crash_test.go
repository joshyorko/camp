package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

func TestLocalLifecycleCrashMatrix(t *testing.T) {
	t.Log("implemented process-death cut: controller death after forwarder spawn and before durable forwarding fact")
	if os.Getenv("CAMP_TEST_REAL_LIFECYCLE") != "1" {
		t.Skip("set CAMP_TEST_REAL_LIFECYCLE=1 to run the real DevPod/Hauler lifecycle")
	}
	for _, name := range []string{"go", "devpod", "hauler", "pasta", "docker"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required executable %q: %v", name, err)
		}
	}
	root := t.TempDir()
	source := filepath.Join(root, "mock Second Brain")
	backend := filepath.Join(root, "backend")
	controller := filepath.Join(root, "controller")
	devPod := newDevPodTestIsolation(root)
	bin := candidateBinary(t)
	writeLifecycleFixture(t, source)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	t.Cleanup(cancel)
	createdWorkspaces := newCreatedWorkspaceTracker()
	environment := lifecycleEnvironment(controller, backend, devPod)
	var opening *exec.Cmd
	var openingDone chan error
	openingWaited := false
	retryReceipt := ""
	cleanupFailed := false
	sessionInitialized := false
	t.Cleanup(func() {
		if opening != nil && !openingWaited && opening.Process != nil {
			_ = opening.Process.Signal(syscall.SIGCONT)
			_ = opening.Process.Kill()
			if openingDone != nil {
				_ = <-openingDone
			}
		}
		if t.Failed() {
			diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			logDevPodFailureEvidence(t, diagnosticCtx, devPod)
			diagnosticCancel()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()
		if sessionInitialized {
			if output, err := runLifecycleCommand(cleanupCtx, environment, bin, "--json", "close"); err != nil {
				cleanupFailed = true
				t.Errorf("close test-owned session: %v\n%s", err, output)
			}
			stopRecordedSessionProcesses(cleanupCtx, controller)
			if err := createdWorkspaces.TrackController(controller); err != nil {
				cleanupFailed = true
				t.Errorf("recover exact workspace IDs from test-owned controller: %v", err)
			}
		}
		if err := createdWorkspaces.DeleteAll(func(workspaceID string) error {
			output, err := runDevPodCommand(cleanupCtx, devPod, "delete", "--ignore-not-found", workspaceID)
			if err != nil {
				cleanupFailed = true
				return fmt.Errorf("%w: %s", err, output)
			}
			return nil
		}); err != nil {
			cleanupFailed = true
			t.Errorf("clean exact test-owned DevPod workspaces: %v", err)
		}
		if !cleanupFailed && retryReceipt != "" {
			fmt.Println(retryReceipt)
		}
	})

	t.Log("bootstrap the Docker provider inside the private DevPod context")
	if output, err := bootstrapDevPodDockerProvider(ctx, devPod); err != nil {
		retryReceipt = `{"retry":"crash-matrix-bootstrap"}`
		t.Fatalf("retryable crash-matrix setup failure: bootstrap private DevPod Docker provider: %v\n%s", err, output)
	}
	mustRunLifecycle(t, ctx, environment, bin, "--json", "init", source, "--name", "forwarder-crash")
	sessionInitialized = true
	var openingOutput bytes.Buffer
	opening = newCrashOpenCommand(ctx, bin, source, environment, &openingOutput)
	if err := opening.Start(); err != nil {
		t.Fatal(err)
	}
	openingDone = make(chan error, 1)
	go func() {
		openingDone <- opening.Wait()
	}()

	evidenceCtx, evidenceCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer evidenceCancel()
	evidencePath, err := waitForForwarderEvidenceOrExit(evidenceCtx, controller, "registry", openingDone, &openingOutput)
	if err != nil {
		if _, exited := err.(*openingExitedBeforeEvidenceError); exited {
			openingWaited = true
		} else {
			_ = opening.Process.Signal(syscall.SIGCONT)
			_ = opening.Process.Kill()
			waitErr := <-openingDone
			openingWaited = true
			err = fmt.Errorf("%w\ncamp open exit after evidence wait: %v\ncamp open output:\n%s", err, waitErr, openingOutput.String())
		}
		t.Fatal(err)
	}
	if err := opening.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stop opening controller: %v", err)
	}
	sessionID := forwarderEvidenceSessionID(evidencePath)
	store, err := journalstore.NewStore(filepath.Join(controller, "data", "camp"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, pending, err := store.Load(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPendingTransition(pending, "ForwarderStarted:registry") || hasForwardingRecord(snapshot, "registry") {
		_ = opening.Process.Signal(syscall.SIGCONT)
		openingWaited = true
		_ = <-openingDone
		t.Fatalf("did not stop at pending registry forwarder: pending=%v forwarding=%#v\n%s", pendingTransitions(pending), snapshot.Recovery.Forwarding, openingOutput.Bytes())
	}
	body, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence domain.ForwardingRecord
	if err := json.Unmarshal(body, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Process.Identity.PID <= 0 || evidence.Process.ParentPID != opening.Process.Pid {
		t.Fatalf("forwarder evidence = %#v controller pid=%d", evidence, opening.Process.Pid)
	}
	if err := opening.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill opening controller: %v", err)
	}
	openingWaited = true
	if err := <-openingDone; err == nil {
		t.Fatal("opening controller exited successfully after SIGKILL")
	}

	processes, err := supervisor.NewProcessManager()
	if err != nil {
		t.Fatal(err)
	}
	status := waitForReparentedForwarder(t, ctx, processes, evidence)
	if status.ParentPID == evidence.Process.ParentPID {
		t.Fatalf("forwarder PPID did not change after controller death: %#v", status)
	}

	reopened := decodeOpenResult(t, mustRunLifecycleAt(t, ctx, environment, source, bin, "--json", "open"))
	if reopened.SessionID != sessionID || reopened.WorkspaceID == "" {
		t.Fatalf("recovered open = %#v, want session %q", reopened, sessionID)
	}
	createdWorkspaces.Track(reopened.WorkspaceID)
	reconciled, remaining, err := store.Load(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("pending transitions after recovery: %v", pendingTransitions(remaining))
	}
	registry, ok := forwardingRecord(reconciled, "registry")
	if !ok || registry.Process.Identity != evidence.Process.Identity || registry.EvidenceDevice == 0 || registry.EvidenceInode == 0 {
		t.Fatalf("reconciled registry forwarder = %#v, evidence = %#v", registry, evidence)
	}
	fileserver, fileserverOK := forwardingRecord(reconciled, "fileserver")
	if !fileserverOK || len(reconciled.Recovery.Forwarding) != 2 {
		t.Fatalf("reconciled forwarders = %#v", reconciled.Recovery.Forwarding)
	}

	mustRunLifecycle(t, ctx, environment, bin, "--json", "close", "--camp", "forwarder-crash")
	assertDevPodWorkspacesAbsent(t, ctx, devPod, reopened.WorkspaceID)
	status, err = processes.Inspect(ctx, evidence.Process.Identity)
	if err == nil && status.Running {
		t.Fatalf("recovered registry forwarder survived close: %#v", status)
	}
	assertEndpointClosed(t, registry.LocalEndpoint)
	assertEndpointClosed(t, fileserver.LocalEndpoint)
}

func newCrashOpenCommand(ctx context.Context, bin, source string, environment []string, output *bytes.Buffer) *exec.Cmd {
	command := exec.CommandContext(ctx, bin, "--json", "open", "Projects/Unicode space")
	command.Dir = source
	command.Env = mergeCommandEnvironment(os.Environ(), environment)
	command.Stdout = output
	command.Stderr = output
	return command
}

type openingExitedBeforeEvidenceError struct {
	name   string
	err    error
	output string
}

func (err *openingExitedBeforeEvidenceError) Error() string {
	return fmt.Sprintf("camp open exited before %s forwarder evidence: %v\ncamp open output:\n%s", err.name, err.err, err.output)
}

func forwarderEvidenceSessionID(path string) string {
	return filepath.Base(filepath.Dir(path))
}

func waitForForwarderEvidenceOrExit(ctx context.Context, controller, name string, openingDone <-chan error, output *bytes.Buffer) (string, error) {
	pattern := filepath.Join(scenarioRuntimeDirectory(controller), "camp", "*", name+"-forward.json")
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", err
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("ambiguous forwarder evidence: %v", matches)
		}
		select {
		case err := <-openingDone:
			return "", &openingExitedBeforeEvidenceError{name: name, err: err, output: output.String()}
		case <-ctx.Done():
			return "", fmt.Errorf("wait for %s forwarder evidence: %w", name, ctx.Err())
		case <-poll.C:
		}
	}
}

func waitForReparentedForwarder(t *testing.T, ctx context.Context, processes *supervisor.ProcessManager, evidence domain.ForwardingRecord) ports.ProcessStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := processes.Inspect(ctx, evidence.Process.Identity)
		if err != nil {
			t.Fatalf("inspect surviving forwarder: %v", err)
		}
		if !status.Running {
			t.Fatal("forwarder exited with the killed controller")
		}
		if status.ParentPID != evidence.Process.ParentPID {
			return status
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("forwarder was not reparented: %#v", status)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for forwarder reparenting: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func hasPendingTransition(pending []ports.PendingIntent, transition string) bool {
	for _, item := range pending {
		if item.Intent.Transition == transition {
			return true
		}
	}
	return false
}

func pendingTransitions(pending []ports.PendingIntent) []string {
	result := make([]string, 0, len(pending))
	for _, item := range pending {
		result = append(result, item.Intent.Transition)
	}
	return result
}

func forwardingRecord(snapshot domain.JournalSnapshot, name string) (domain.ForwardingRecord, bool) {
	for _, record := range snapshot.Recovery.Forwarding {
		if record.Name == name {
			return record, true
		}
	}
	return domain.ForwardingRecord{}, false
}

func hasForwardingRecord(snapshot domain.JournalSnapshot, name string) bool {
	_, ok := forwardingRecord(snapshot, name)
	return ok
}

func stopRecordedSessionProcesses(ctx context.Context, controller string) {
	store, err := journalstore.NewStore(filepath.Join(controller, "data", "camp"))
	if err != nil {
		return
	}
	snapshots, err := store.List(ctx)
	if err != nil {
		return
	}
	processes, err := supervisor.NewProcessManager()
	if err != nil {
		return
	}
	for _, snapshot := range snapshots {
		for _, record := range snapshot.Recovery.Forwarding {
			_ = processes.Stop(ctx, record.Process.Identity, 5*time.Second)
		}
		for index := len(snapshot.Services) - 1; index >= 0; index-- {
			_ = processes.Stop(ctx, snapshot.Services[index].Child.Identity, 5*time.Second)
			_ = processes.Stop(ctx, snapshot.Services[index].Helper.Identity, 5*time.Second)
		}
		_ = processes.Stop(ctx, snapshot.Supervisor.Identity, 5*time.Second)
	}
}
