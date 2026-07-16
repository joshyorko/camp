package host

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
)

type Identity struct {
	procRoot       string
	machineIDPaths []string
}

func NewIdentity() *Identity {
	return &Identity{procRoot: "/proc", machineIDPaths: []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}}
}

func (i *Identity) MachineID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	paths := i.machineIDPaths
	if len(paths) == 0 {
		paths = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(body)) != "" {
			return strings.TrimSpace(string(body)), nil
		}
	}
	return "", fmt.Errorf("read machine identity: no machine-id source")
}

func (i *Identity) CurrentProcess(ctx context.Context) (domain.ProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProcessIdentity{}, err
	}
	return i.processIdentity(os.Getpid())
}

func (i *Identity) IsCurrent(ctx context.Context, expected domain.ProcessIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	observed, err := i.processIdentity(expected.PID)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return observed == expected, nil
}

func (i *Identity) processIdentity(pid int) (domain.ProcessIdentity, error) {
	boot, err := os.ReadFile(i.procRoot + "/sys/kernel/random/boot_id")
	if err != nil {
		return domain.ProcessIdentity{}, fmt.Errorf("read boot id: %w", err)
	}
	stat, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", i.procRoot, pid))
	if err != nil {
		return domain.ProcessIdentity{}, fmt.Errorf("read process stat for %d: %w", pid, err)
	}
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 {
		return domain.ProcessIdentity{}, fmt.Errorf("parse process stat for %d: missing command terminator", pid)
	}
	fields := strings.Fields(string(stat)[closing+1:])
	if len(fields) <= 19 {
		return domain.ProcessIdentity{}, fmt.Errorf("parse process stat for %d: short record", pid)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return domain.ProcessIdentity{}, fmt.Errorf("parse process start ticks for %d: %w", pid, err)
	}
	return domain.ProcessIdentity{PID: pid, BootID: strings.TrimSpace(string(boot)), StartTicks: start}, nil
}
