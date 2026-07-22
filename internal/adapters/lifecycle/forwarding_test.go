package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestForwarderManagerStartsExactDevPodReverseForwardAndProbesWorkspace(t *testing.T) {
	client := &fakeForwardDevPod{}
	processes := &fakeForwardProcesses{status: ports.ProcessStatus{
		Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}, Running: true,
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "camp-brain"}, PGID: 41, SID: 41, NetNS: "net:[1]",
	}}
	manager := NewForwarderManager(client, processes)
	record, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: "/tmp/forward.log",
		EvidencePath: filepath.Join(t.TempDir(), "registry-forwarding.json"),
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

func TestForwarderManagerStartRejectsPreexistingEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	if err := os.WriteFile(evidencePath, []byte(`{"name":"registry"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewForwarderManager(&fakeForwardDevPod{}, &fakeForwardProcesses{})
	_, err := manager.Start(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: "/tmp/forward.log",
		EvidencePath: evidencePath,
	})
	if err == nil {
		t.Fatal("Start() error = nil")
	}
}

func TestForwarderManagerObserveReplaysPersistedEvidenceWithoutStarting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	record := domain.ForwardingRecord{
		Name: "registry", LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", EvidencePath: evidencePath,
		Process: domain.ProcessRecord{
			Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99},
			DesiredExecutable: "/opt/devpod", ObservedExecutable: "/opt/devpod",
			Argv: []string{"/opt/devpod", "ssh", "camp-brain", "--command", "sleep 2147483647"},
			ArgvSHA256: "0", ParentPID: 1, PGID: 41, SID: 41, NetNS: "net:[1]",
		},
		DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
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
		Executable: "/opt/devpod", Argv: []string{"/opt/devpod", "ssh", "camp-brain", "--command", "sleep 2147483647"}, PGID: 41, SID: 41, NetNS: "net:[1]",
	}}
	manager := NewForwarderManager(&fakeForwardDevPod{}, processes)
	got, err := manager.Observe(context.Background(), domain.ForwardingRequest{
		Name: "registry", WorkspaceID: "camp-brain", Context: "default",
		LocalEndpoint: "127.0.0.1:39401", WorkspaceEndpoint: "127.0.0.1:39401", LogPath: "/tmp/forward.log",
		EvidencePath: evidencePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Process.Identity, record.Process.Identity) || got.ObservedState != domain.RuntimeObservedReady {
		t.Fatalf("Observe() = %#v", got)
	}
}

func TestForwarderManagerStopRemovesEvidenceAfterSuccessfulIdentityStop(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "forwarding.json")
	if err := os.WriteFile(evidencePath, []byte(`{"evidencePath":"`+evidencePath+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewForwarderManager(&fakeForwardDevPod{}, &fakeForwardProcesses{})
	if err := manager.Stop(context.Background(), domain.ForwardingRecord{
		EvidencePath: evidencePath,
		Process: domain.ProcessRecord{Identity: domain.ProcessIdentity{PID: 41, BootID: "boot", StartTicks: 99}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(evidencePath); !os.IsNotExist(err) {
		t.Fatalf("evidence exists after Stop: %v", err)
	}
}

type fakeForwardDevPod struct {
	options  devpod.SSHOptions
	executed []ports.WorkspaceCommand
}

func (f *fakeForwardDevPod) SSHCommand(options devpod.SSHOptions) (ports.Command, error) {
	f.options = options
	return ports.Command{Executable: "/opt/devpod", Argv: []string{"ssh", options.WorkspaceID}}, nil
}
func (f *fakeForwardDevPod) Execute(_ context.Context, command ports.WorkspaceCommand) (ports.Result, error) {
	f.executed = append(f.executed, command)
	return ports.Result{ExitCode: 0}, nil
}

type fakeForwardProcesses struct {
	status ports.ProcessStatus
	spec   ports.ProcessSpec
}

func (f *fakeForwardProcesses) Start(_ context.Context, spec ports.ProcessSpec) (domain.ProcessIdentity, error) {
	f.spec = spec
	return f.status.Identity, nil
}
func (f *fakeForwardProcesses) Inspect(context.Context, domain.ProcessIdentity) (ports.ProcessStatus, error) {
	return f.status, nil
}
func (f *fakeForwardProcesses) Stop(context.Context, domain.ProcessIdentity, time.Duration) error {
	return nil
}
