package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if !status.Running || status.Identity != identity || status.Executable != "/usr/bin/sleep" || status.PGID != identity.PID || status.SID != identity.PID || status.NetNS == "" {
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
