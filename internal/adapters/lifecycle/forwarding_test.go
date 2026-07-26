package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"golang.org/x/sys/unix"
)

func TestForwarderManagerStartsExactDevPodReverseForwardAndProbesWorkspace(t *testing.T) {
	root := t.TempDir()
	client := &fakeForwardDevPod{}
	processes := &fakeForwardProcesses{status: ports.ProcessStatus{
		Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}, Running: true,
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "camp-brain"}, PGID: 41, SID: 41, NetNS: "net:[1]",
	}}
	manager := newTestForwarderManager(client, processes)
	record, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(root, "forward.log"),
		EvidencePath: filepath.Join(root, "registry-forwarding.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOptions := devpod.SSHOptions{WorkspaceID: "camp-brain", Context: "default", ReverseForwards: []string{"127.0.0.1:39401:127.0.0.1:39401"}, StartServices: true, ForwardedArgv: []string{"--command", "sleep 2147483647"}}
	if !reflect.DeepEqual(client.options, wantOptions) {
		t.Fatalf("SSH options = %#v, want %#v", client.options, wantOptions)
	}
	if len(client.executed) != 1 || !reflect.DeepEqual(client.executed[0].Argv, []string{"curl", "--fail", "--silent", "--show-error", "--max-time", "5", "http://127.0.0.1:39401/v2/"}) {
		t.Fatalf("probe = %#v", client.executed)
	}
	if record.Process.Identity.PID != 41 || record.ObservedState != domain.RuntimeObservedReady {
		t.Fatalf("record = %#v", record)
	}
}

func TestForwarderManagerRetriesUnreadyTunnelWithFreshExactProcessIdentity(t *testing.T) {
	root := t.TempDir()
	first := forwarderProcessStatus(41)
	second := forwarderProcessStatus(42)
	client := &fakeForwardDevPod{executeErrors: []error{errors.New("tunnel not installed"), nil}}
	processes := &fakeForwardProcesses{startStatuses: []ports.ProcessStatus{first, second}}
	manager := newTestForwarderManager(client, processes)
	manager.startAttempts = 2
	manager.readinessTimeout = 0
	manager.readinessInterval = 0

	record, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "fileserver", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39402", WorkspaceEndpoint: "127.0.0.1:39402", LogPath: filepath.Join(root, "forward.log"),
		EvidencePath: filepath.Join(root, "fileserver-forwarding.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Process.Identity != second.Identity {
		t.Fatalf("recorded process = %#v, want replacement %#v", record.Process.Identity, second.Identity)
	}
	if !reflect.DeepEqual(processes.stopped, []domain.ProcessIdentity{first.Identity}) {
		t.Fatalf("stopped identities = %#v, want only failed attempt %#v", processes.stopped, first.Identity)
	}
	if len(processes.specs) != 2 || len(client.executed) != 2 {
		t.Fatalf("starts = %d probes = %d, want two exact attempts", len(processes.specs), len(client.executed))
	}
	persisted, err := readForwardingEvidence(filepath.Join(root, "fileserver-forwarding.json"))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Process.Identity != second.Identity {
		t.Fatalf("persisted process = %#v, want replacement %#v", persisted.Process.Identity, second.Identity)
	}
}

func TestForwarderManagerFailedRetriesStopExactProcessesAndRemoveEvidence(t *testing.T) {
	root := t.TempDir()
	first := forwarderProcessStatus(51)
	second := forwarderProcessStatus(52)
	client := &fakeForwardDevPod{executeErrors: []error{errors.New("first tunnel missing"), errors.New("second tunnel missing")}}
	processes := &fakeForwardProcesses{startStatuses: []ports.ProcessStatus{first, second}}
	manager := newTestForwarderManager(client, processes)
	manager.startAttempts = 2
	manager.readinessTimeout = 0
	manager.readinessInterval = 0
	evidencePath := filepath.Join(root, "fileserver-forwarding.json")

	_, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "fileserver", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39402", WorkspaceEndpoint: "127.0.0.1:39402", LogPath: filepath.Join(root, "forward.log"),
		EvidencePath: evidencePath,
	})
	if err == nil {
		t.Fatal("Start() error = nil")
	}
	if !reflect.DeepEqual(processes.stopped, []domain.ProcessIdentity{first.Identity, second.Identity}) {
		t.Fatalf("stopped identities = %#v, want both failed exact identities", processes.stopped)
	}
	if _, statErr := os.Lstat(evidencePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed retry evidence remains: %v", statErr)
	}
}

func TestForwarderManagerStartRejectsPreexistingEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	if err := os.WriteFile(evidencePath, []byte(`{"name":"registry"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newTestForwarderManager(&fakeForwardDevPod{}, &fakeForwardProcesses{})
	_, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(root, "forward.log"),
		EvidencePath: evidencePath,
	})
	if err == nil {
		t.Fatal("Start() error = nil")
	}
}

func TestForwarderManagerStartRejectsProcessThatDoesNotMatchRequestedCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	processes := &fakeForwardProcesses{status: ports.ProcessStatus{
		Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}, Running: true,
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "other-workspace"}, PGID: 41, SID: 41, NetNS: "net:[1]",
	}}
	manager := newTestForwarderManager(&fakeForwardDevPod{}, processes)
	_, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(root, "forward.log"),
		EvidencePath: evidencePath,
	})
	if err == nil {
		t.Fatal("Start() error = nil")
	}
	if _, statErr := os.Lstat(evidencePath); !os.IsNotExist(statErr) {
		t.Fatalf("untrusted process evidence was published: %v", statErr)
	}
}

func TestForwarderManagerStartDoesNotRemoveEvidenceThatWinsPublicationRace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	competitor := []byte(`{"owner":"other-forwarder"}`)
	processes := &fakeForwardProcesses{status: ports.ProcessStatus{
		Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}, Running: true,
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "camp-brain"}, PGID: 41, SID: 41, NetNS: "net:[1]",
	}}
	processes.onStart = func() {
		if err := os.WriteFile(evidencePath, competitor, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := newTestForwarderManager(&fakeForwardDevPod{}, processes)
	_, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(root, "forward.log"), EvidencePath: evidencePath,
	})
	if err == nil {
		t.Fatal("Start() error = nil")
	}
	got, readErr := os.ReadFile(evidencePath)
	if readErr != nil || !reflect.DeepEqual(got, competitor) {
		t.Fatalf("competing evidence = %q, err = %v", got, readErr)
	}
	if processes.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", processes.stopCalls)
	}
}

func TestForwarderManagerReadinessCleanupDoesNotRemoveReplacementEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	competitor := []byte(`{"owner":"other-forwarder"}`)
	ctx, cancel := context.WithCancel(context.Background())
	processes := &fakeForwardProcesses{status: ports.ProcessStatus{
		Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}, Running: true,
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "camp-brain"}, PGID: 41, SID: 41, NetNS: "net:[1]",
	}}
	client := &fakeForwardDevPod{executeErr: errors.New("not ready")}
	client.onExecute = func() {
		if err := os.Rename(evidencePath, evidencePath+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, competitor, 0o600); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	manager := newTestForwarderManager(client, processes)
	_, err := manager.Start(ctx, domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(root, "forward.log"), EvidencePath: evidencePath,
	})
	if err == nil {
		t.Fatal("Start() error = nil")
	}
	got, readErr := os.ReadFile(evidencePath)
	if readErr != nil || !reflect.DeepEqual(got, competitor) {
		t.Fatalf("replacement evidence = %q, err = %v", got, readErr)
	}
	if processes.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", processes.stopCalls)
	}
}

func TestForwarderManagerObserveReplaysPersistedEvidenceWithoutStarting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	record := domain.ForwardingRecord{
		Name: "registry", LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", EvidencePath: evidencePath,
		Process: domain.ProcessRecord{
			Identity:          domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99},
			DesiredExecutable: "/opt/devpod", ObservedExecutable: "/opt/devpod",
			Argv:       []string{"/opt/devpod", "ssh", "camp-brain"},
			ArgvSHA256: forwardingArgvSHA256([]string{"/opt/devpod", "ssh", "camp-brain"}), ParentPID: 500, PGID: 41, SID: 41, NetNS: "net:[1]",
		},
		DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedPending,
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	processes := &fakeForwardProcesses{status: ports.ProcessStatus{
		Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}, Running: true,
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "camp-brain"}, ParentPID: 1, PGID: 41, SID: 41, NetNS: "net:[1]",
	}}
	manager := newTestForwarderManager(&fakeForwardDevPod{}, processes)
	got, err := manager.Observe(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(root, "forward.log"),
		EvidencePath: evidencePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Process.Identity, record.Process.Identity) || got.ObservedState != domain.RuntimeObservedReady {
		t.Fatalf("Observe() = %#v", got)
	}
}

func TestForwarderManagerObservePollsPersistedRegistryForwarderUntilReady(t *testing.T) {
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	status := forwarderProcessStatus(61)
	record := domain.ForwardingRecord{
		Name: "registry", LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", EvidencePath: evidencePath,
		Process: domain.ProcessRecord{
			Identity: status.Identity, DesiredExecutable: "/opt/devpod", ObservedExecutable: "/opt/devpod",
			Argv: append([]string(nil), status.Argv...), ArgvSHA256: forwardingArgvSHA256(status.Argv),
			ParentPID: status.ParentPID, PGID: status.PGID, SID: status.SID, NetNS: status.NetNS,
		},
		DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedPending,
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeForwardDevPod{executeErrors: []error{errors.New("tunnel still converging"), nil}}
	processes := &fakeForwardProcesses{status: status}
	manager := newTestForwarderManager(client, processes)
	manager.readinessTimeout = time.Second
	manager.readinessInterval = 0

	got, err := manager.Observe(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(root, "forward.log"),
		EvidencePath: evidencePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Process.Identity != status.Identity || got.ObservedState != domain.RuntimeObservedReady {
		t.Fatalf("Observe() = %#v", got)
	}
	if len(client.executed) != 2 || len(processes.specs) != 0 || len(processes.stopped) != 0 {
		t.Fatalf("probes=%d starts=%d stops=%#v, want adoption-only polling", len(client.executed), len(processes.specs), processes.stopped)
	}
}

func TestForwarderManagerObserveRejectsEvidenceThatIsNotTheRequestedCommand(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*domain.ForwardingRecord, *ports.ProcessStatus)
	}{
		{name: "argv digest", mutate: func(record *domain.ForwardingRecord, _ *ports.ProcessStatus) { record.Process.ArgvSHA256 = "wrong" }},
		{name: "desired state", mutate: func(record *domain.ForwardingRecord, _ *ports.ProcessStatus) {
			record.DesiredState = domain.RuntimeDesiredStopped
		}},
		{name: "unexpected argv", mutate: func(record *domain.ForwardingRecord, status *ports.ProcessStatus) {
			record.Process.Argv = []string{"/opt/devpod", "ssh", "other-workspace"}
			record.Process.ArgvSHA256 = forwardingArgvSHA256(record.Process.Argv)
			status.Argv = append([]string(nil), record.Process.Argv...)
		}},
		{name: "different executable", mutate: func(record *domain.ForwardingRecord, status *ports.ProcessStatus) {
			record.Process.ObservedExecutable = "/opt/not-devpod"
			status.Executable = record.Process.ObservedExecutable
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidencePath := filepath.Join(t.TempDir(), "forwarding.json")
			record := domain.ForwardingRecord{
				Name: "registry", LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", EvidencePath: evidencePath,
				Process: domain.ProcessRecord{
					Identity:          domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99},
					DesiredExecutable: "/opt/devpod", ObservedExecutable: "/opt/devpod",
					Argv: []string{"/opt/devpod", "ssh", "camp-brain"}, ParentPID: 1, PGID: 41, SID: 41, NetNS: "net:[1]",
				},
				DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedPending,
			}
			record.Process.ArgvSHA256 = forwardingArgvSHA256(record.Process.Argv)
			status := ports.ProcessStatus{
				Identity: record.Process.Identity, Running: true, Executable: record.Process.ObservedExecutable,
				Argv: append([]string(nil), record.Process.Argv...), ParentPID: 1, PGID: 41, SID: 41, NetNS: "net:[1]",
			}
			test.mutate(&record, &status)
			body, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(evidencePath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			manager := newTestForwarderManager(&fakeForwardDevPod{}, &fakeForwardProcesses{status: status})
			_, err = manager.Observe(context.Background(), domain.ForwardingRequest{
				Name: "registry", WorkspaceID: "camp-brain", Context: "default",
				LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: filepath.Join(filepath.Dir(evidencePath), "forward.log"), EvidencePath: evidencePath,
			})
			if err == nil {
				t.Fatal("Observe() error = nil")
			}
		})
	}
}

func TestForwarderManagerStopRemovesEvidenceAfterSuccessfulIdentityStop(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	if err := os.WriteFile(evidencePath, []byte(`{"evidencePath":"`+evidencePath+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := readForwardingEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record.Process.Identity = domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}
	manager := newTestForwarderManager(&fakeForwardDevPod{}, &fakeForwardProcesses{})
	if err := manager.Stop(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(evidencePath); !os.IsNotExist(err) {
		t.Fatalf("evidence exists after Stop: %v", err)
	}
}

func TestForwarderManagerStopDoesNotRemoveReplacementEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	if err := os.WriteFile(evidencePath, []byte(`{"evidencePath":"`+evidencePath+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(evidencePath, &stat); err != nil {
		t.Fatal(err)
	}
	record := domain.ForwardingRecord{
		EvidencePath: evidencePath, EvidenceDevice: uint64(stat.Dev), EvidenceInode: stat.Ino,
		Process: domain.ProcessRecord{Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}},
	}
	if err := os.Rename(evidencePath, evidencePath+".original"); err != nil {
		t.Fatal(err)
	}
	competitor := []byte(`{"owner":"other-forwarder"}`)
	if err := os.WriteFile(evidencePath, competitor, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newTestForwarderManager(&fakeForwardDevPod{}, &fakeForwardProcesses{})
	if err := manager.Stop(context.Background(), record); err == nil {
		t.Fatal("Stop() error = nil")
	}
	got, err := os.ReadFile(evidencePath)
	if err != nil || !reflect.DeepEqual(got, competitor) {
		t.Fatalf("replacement evidence = %q, err = %v", got, err)
	}
}

func TestReadForwardingEvidenceRejectsUnsafeInodes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		build func(*testing.T, string)
	}{
		{name: "symlink", build: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target.json")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", build: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(path, path+".other"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversize", build: func(t *testing.T, path string) {
			if err := os.WriteFile(path, make([]byte, maxForwardingEvidenceBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "forwarding.json")
			test.build(t, path)
			if _, err := readForwardingEvidence(path); err == nil {
				t.Fatal("readForwardingEvidence() error = nil")
			}
		})
	}
}

type fakeForwardDevPod struct {
	options       devpod.SSHOptions
	executed      []ports.WorkspaceCommand
	onExecute     func()
	executeErr    error
	executeErrors []error
}

func (f *fakeForwardDevPod) SSHCommand(options devpod.SSHOptions) (ports.Command, error) {
	f.options = options
	return ports.Command{Executable: "/opt/devpod", Argv: []string{"ssh", options.WorkspaceID}}, nil
}
func (f *fakeForwardDevPod) Execute(_ context.Context, command ports.WorkspaceCommand) (ports.Result, error) {
	f.executed = append(f.executed, command)
	if f.onExecute != nil {
		f.onExecute()
	}
	if len(f.executeErrors) > 0 {
		err := f.executeErrors[0]
		f.executeErrors = f.executeErrors[1:]
		return ports.Result{ExitCode: 0}, err
	}
	return ports.Result{ExitCode: 0}, f.executeErr
}

type fakeForwardProcesses struct {
	status        ports.ProcessStatus
	startStatuses []ports.ProcessStatus
	spec          ports.ProcessSpec
	specs         []ports.ProcessSpec
	onStart       func()
	stopCalls     int
	stopped       []domain.ProcessIdentity
}

func (f *fakeForwardProcesses) Start(_ context.Context, spec ports.ProcessSpec) (domain.ProcessIdentity, error) {
	f.spec = spec
	f.specs = append(f.specs, spec)
	if f.onStart != nil {
		f.onStart()
	}
	if len(f.startStatuses) > 0 {
		f.status = f.startStatuses[0]
		f.startStatuses = f.startStatuses[1:]
	}
	return f.status.Identity, nil
}
func (f *fakeForwardProcesses) Inspect(_ context.Context, identity domain.ProcessIdentity) (ports.ProcessStatus, error) {
	if f.status.Identity != identity {
		return ports.ProcessStatus{Identity: identity}, errors.New("unexpected process identity")
	}
	return f.status, nil
}
func (f *fakeForwardProcesses) Stop(_ context.Context, identity domain.ProcessIdentity, _ time.Duration) error {
	f.stopCalls++
	f.stopped = append(f.stopped, identity)
	return nil
}

func forwarderProcessStatus(pid int) ports.ProcessStatus {
	return ports.ProcessStatus{
		Identity: domain.ProcessIdentity{PID: pid, BootID: "boot", StartTicks: uint64(pid + 100)}, Running: true,
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "camp-brain"}, ParentPID: 500,
		PGID: pid, SID: pid, NetNS: "net:[1]",
	}
}

func forwardingArgvSHA256(argv []string) string {
	digest := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(digest[:])
}

func newTestForwarderManager(client forwardDevPod, processes forwardProcesses) *ForwarderManager {
	manager := NewForwarderManager(client, processes)
	manager.resolveExecutable = func(path string) (string, error) { return path, nil }
	return manager
}
