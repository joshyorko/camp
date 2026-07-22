package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestProcessManagerRecordsExactIdentityAndNeverSignalsPIDReuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager, err := NewProcessManager()
	if err != nil {
		t.Fatalf("NewProcessManager() error = %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "private.log")
	identity, err := manager.Start(ctx, ports.ProcessSpec{
		Command:    ports.Command{Executable: "/usr/bin/sleep", Argv: []string{"30"}},
		NewSession: true,
		LogPath:    logPath,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), identity, time.Second) })

	status, err := manager.Inspect(ctx, identity)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	expectedExecutable, err := filepath.EvalSymlinks("/usr/bin/sleep")
	if err != nil {
		expectedExecutable = "/usr/bin/sleep"
	}
	if !status.Running || status.Identity != identity || status.Executable != expectedExecutable || status.PGID != identity.PID || status.SID != identity.PID || status.NetNS == "" {
		t.Fatalf("status = %#v", status)
	}
	if len(status.Argv) != 2 || status.Argv[0] != "/usr/bin/sleep" || status.Argv[1] != "30" {
		t.Fatalf("argv = %#v", status.Argv)
	}
	if info, err := os.Stat(logPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %v, %v; want 0600", info, err)
	}

	reused := identity
	reused.StartTicks++
	if err := manager.Stop(ctx, reused, 10*time.Millisecond); !errors.Is(err, ErrProcessIdentity) {
		t.Fatalf("Stop(reused identity) error = %v, want ErrProcessIdentity", err)
	}
	if status, err := manager.Inspect(ctx, identity); err != nil || !status.Running {
		t.Fatalf("real process was signalled by reused identity: %#v, %v", status, err)
	}

	if err := manager.Stop(ctx, identity, time.Second); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if status, err := manager.Inspect(ctx, identity); err != nil || status.Running {
		t.Fatalf("Inspect(after stop) = %#v, %v; want absent", status, err)
	}
}

func TestProcessManagerGroupFailsClosedWhenMemberCannotBeInspected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "123"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "123", "stat"), []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := &ProcessManager{procRoot: root, bootID: "boot"}
	_, err := manager.Group(context.Background(), 7)
	if err == nil {
		t.Fatal("Group() silently ignored an uninspectable process-group member")
	}
}

func TestProcessManagerGroupIgnoresUninspectableNonMember(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	processRoot := filepath.Join(root, "123")
	if err := os.MkdirAll(processRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stat := "123 (kernel worker) S 1 8 8 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 42\n"
	if err := os.WriteFile(filepath.Join(processRoot, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "cmdline"), []byte("kernel-worker\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "exe"), nil, 0o000); err != nil {
		t.Fatal(err)
	}

	manager := &ProcessManager{procRoot: root, bootID: "boot"}
	group, err := manager.Group(context.Background(), 7)
	if err != nil {
		t.Fatalf("Group() error = %v; unrelated process must not require full inspection", err)
	}
	if len(group) != 0 {
		t.Fatalf("Group() = %#v, want no members", group)
	}
}

func TestProcessManagerGroupFailsClosedWhenCandidateChangesGroup(t *testing.T) {
	t.Parallel()
	candidate := ports.ProcessStatus{Identity: domain.ProcessIdentity{PID: 123, BootID: "boot", StartTicks: 42}, Running: true, PGID: 7}
	observed := candidate
	observed.PGID = 8
	if err := validateGroupCandidate(candidate, observed); !errors.Is(err, ErrProcessIdentity) {
		t.Fatalf("validateGroupCandidate() error = %v, want ErrProcessIdentity", err)
	}
}

func TestReconcileGroupInspectionFailureAllowsOnlyExitedCandidate(t *testing.T) {
	t.Parallel()
	candidate := ports.ProcessStatus{Identity: domain.ProcessIdentity{PID: 123, BootID: "boot", StartTicks: 42}, Running: true, PGID: 7}
	inspectErr := errors.New("empty cmdline during exit")
	exited := candidate
	exited.Running = false
	if err := reconcileGroupInspectionFailure(candidate, exited, nil, inspectErr); err != nil {
		t.Fatalf("exited candidate error = %v", err)
	}
	if err := reconcileGroupInspectionFailure(candidate, candidate, nil, inspectErr); !errors.Is(err, inspectErr) {
		t.Fatalf("live candidate error = %v, want inspection failure", err)
	}
}

func TestOriginalGoneWaitsForSameLiveIdentityWhenFullInspectionFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	processRoot := filepath.Join(root, "123")
	if err := os.MkdirAll(processRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stat := "123 (pasta) S 1 123 123 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 42\n"
	if err := os.WriteFile(filepath.Join(processRoot, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "cmdline"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "exe"), nil, 0o000); err != nil {
		t.Fatal(err)
	}

	manager := &ProcessManager{procRoot: root, bootID: "boot"}
	gone, err := manager.originalGone(context.Background(), domain.ProcessIdentity{PID: 123, BootID: "boot", StartTicks: 42})
	if err != nil {
		t.Fatalf("originalGone() error = %v", err)
	}
	if gone {
		t.Fatal("originalGone() treated the same live start identity as gone")
	}
}

func TestCapabilityBearingExecutableFallsBackOnlyToValidatedAbsoluteArgv(t *testing.T) {
	t.Parallel()
	denied := func(string) (string, error) { return "", syscall.EPERM }
	executable, err := resolveObservedExecutable("/proc/7/exe", []string{"/usr/bin/sleep", "30"}, denied)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks("/usr/bin/sleep")
	if executable != want {
		t.Fatalf("executable=%q want=%q", executable, want)
	}
	for _, argv := range [][]string{{"sleep", "30"}, {"/does/not/exist"}, {"/tmp"}} {
		if _, err := resolveObservedExecutable("/proc/7/exe", argv, denied); err == nil {
			t.Fatalf("argv %#v unexpectedly accepted", argv)
		}
	}
}

func TestCapabilityBearingNetNamespaceDenialIsRecordedWithoutInventingNamespace(t *testing.T) {
	t.Parallel()
	identity := domain.ProcessIdentity{PID: 7, BootID: "boot", StartTicks: 11}
	got, err := resolveObservedNetNS("/proc/7/ns/net", identity, func(string) (string, error) { return "", syscall.EPERM })
	if err != nil {
		t.Fatal(err)
	}
	if got != "kernel-denied:boot:11" {
		t.Fatalf("netns marker=%q", got)
	}
	if _, err := resolveObservedNetNS("/proc/7/ns/net", identity, func(string) (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("missing namespace unexpectedly accepted")
	}
}
