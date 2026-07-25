package integration

import (
	"bytes"
	"context"
	"encoding/json"
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
	assertNoDevPodWorkspaces(t, context.Background())

	root := t.TempDir()
	source := filepath.Join(root, "mock Second Brain")
	backend := filepath.Join(root, "backend")
	controller := filepath.Join(root, "controller")
	bin := filepath.Join(root, "camp")
	registryPort, fileserverPort := reserveLoopbackPort(t), reserveLoopbackPort(t)
	writeLifecycleFixture(t, source)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", bin, "../cmd/camp")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build camp: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	t.Cleanup(cancel)
	environment := lifecycleEnvironment(controller, source, backend, registryPort, fileserverPort)
	var opening *exec.Cmd
	openingWaited := false
	t.Cleanup(func() {
		if opening != nil && !openingWaited && opening.Process != nil {
			_ = opening.Process.Signal(syscall.SIGCONT)
			_ = opening.Process.Kill()
			_ = opening.Wait()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()
		_, _ = runLifecycleCommand(cleanupCtx, environment, bin, "--json", "close")
		stopRecordedSessionProcesses(cleanupCtx, controller)
		workspaces, _ := listDevPodWorkspaces(cleanupCtx)
		for _, workspace := range workspaces {
			_, _ = runLifecycleCommand(cleanupCtx, nil, "devpod", "delete", "--context", "default", "--ignore-not-found", workspace)
		}
	})

	mustRunLifecycle(t, ctx, environment, bin, "--json", "init", source)
	var openingOutput bytes.Buffer
	opening = exec.CommandContext(ctx, bin, "--json", "open", "Projects/Unicode space")
	opening.Env = append(os.Environ(), environment...)
	opening.Stdout = &openingOutput
	opening.Stderr = &openingOutput
	if err := opening.Start(); err != nil {
		t.Fatal(err)
	}

	evidencePath := waitForForwarderEvidence(t, ctx, controller, "registry")
	if err := opening.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stop opening controller: %v", err)
	}
	sessionID := filepath.Base(filepath.Dir(filepath.Dir(evidencePath)))
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
		_ = opening.Wait()
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
	if err := opening.Wait(); err == nil {
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

	reopened := decodeOpenResult(t, mustRunLifecycle(t, ctx, environment, bin, "--json", "open", "Projects/Unicode space"))
	if reopened.SessionID != sessionID || reopened.WorkspaceID == "" {
		t.Fatalf("recovered open = %#v, want session %q", reopened, sessionID)
	}
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
	if _, ok := forwardingRecord(reconciled, "fileserver"); !ok || len(reconciled.Recovery.Forwarding) != 2 {
		t.Fatalf("reconciled forwarders = %#v", reconciled.Recovery.Forwarding)
	}

	mustRunLifecycle(t, ctx, environment, bin, "--json", "close")
	assertNoDevPodWorkspaces(t, ctx)
	status, err = processes.Inspect(ctx, evidence.Process.Identity)
	if err == nil && status.Running {
		t.Fatalf("recovered registry forwarder survived close: %#v", status)
	}
	assertLoopbackPortClosed(t, registryPort)
	assertLoopbackPortClosed(t, fileserverPort)
}

func waitForForwarderEvidence(t *testing.T, ctx context.Context, controller, name string) string {
	t.Helper()
	pattern := filepath.Join(controller, "data", "camp", "sessions", "*", "runtime", name+"-forward.json")
	for {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			return matches[0]
		}
		if len(matches) > 1 {
			t.Fatalf("ambiguous forwarder evidence: %v", matches)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s forwarder evidence: %v", name, ctx.Err())
		case <-time.After(2 * time.Millisecond):
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
