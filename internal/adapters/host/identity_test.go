package host

import (
	"context"
	"os"
	"testing"
)

func TestIdentityReturnsStableMachineAndExactCurrentProcess(t *testing.T) {
	t.Parallel()
	identity := NewIdentity()
	machine, err := identity.MachineID(context.Background())
	if err != nil {
		t.Fatalf("MachineID() error = %v", err)
	}
	if machine == "" {
		t.Fatal("MachineID() returned empty identity")
	}

	process, err := identity.CurrentProcess(context.Background())
	if err != nil {
		t.Fatalf("CurrentProcess() error = %v", err)
	}
	if process.PID != os.Getpid() || process.BootID == "" || process.StartTicks == 0 {
		t.Fatalf("CurrentProcess() = %#v, want pid=%d with boot/start identity", process, os.Getpid())
	}
	currentAgain, err := identity.CurrentProcess(context.Background())
	if err != nil || currentAgain != process {
		t.Fatalf("second CurrentProcess() = %#v, %v; want %#v", currentAgain, err, process)
	}
}
