package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var ErrProcessIdentity = errors.New("process identity no longer matches")

type ProcessManager struct {
	procRoot string
	bootID   string
}

func NewProcessManager() (*ProcessManager, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, fmt.Errorf("read boot id: %w", err)
	}
	return &ProcessManager{procRoot: "/proc", bootID: strings.TrimSpace(string(boot))}, nil
}

func (m *ProcessManager) Start(ctx context.Context, spec ports.ProcessSpec) (domain.ProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProcessIdentity{}, err
	}
	if !filepath.IsAbs(spec.Command.Executable) || len(spec.Command.Argv) == 0 || !filepath.IsAbs(spec.LogPath) {
		return domain.ProcessIdentity{}, errors.New("process start requires absolute executable, argv, and log path")
	}
	if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
		return domain.ProcessIdentity{}, fmt.Errorf("create process log directory: %w", err)
	}
	log, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.ProcessIdentity{}, fmt.Errorf("open process log: %w", err)
	}
	if err := os.Chmod(spec.LogPath, 0o600); err != nil {
		_ = log.Close()
		return domain.ProcessIdentity{}, fmt.Errorf("secure process log: %w", err)
	}
	cmd := exec.Command(spec.Command.Executable, spec.Command.Argv...)
	cmd.Dir = spec.Command.Directory
	cmd.Env = processEnvironment(spec.Command.Environment)
	cmd.Stdin = spec.Command.Stdin
	cmd.Stdout = log
	cmd.Stderr = log
	if spec.NewSession {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return domain.ProcessIdentity{}, fmt.Errorf("start process: %w", err)
	}
	_ = log.Close()
	identity, err := m.startedIdentity(ctx, cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return domain.ProcessIdentity{}, err
	}
	go func() { _ = cmd.Wait() }()
	return identity, nil
}

func (m *ProcessManager) Inspect(ctx context.Context, expected domain.ProcessIdentity) (ports.ProcessStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.ProcessStatus{}, err
	}
	status, err := m.inspectPID(expected.PID)
	if err != nil {
		if isProcessGone(err) {
			return ports.ProcessStatus{Identity: expected, Running: false}, nil
		}
		return ports.ProcessStatus{}, err
	}
	if status.Identity != expected {
		return status, ErrProcessIdentity
	}
	return status, nil
}

func (m *ProcessManager) InspectPID(ctx context.Context, pid int) (ports.ProcessStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.ProcessStatus{}, err
	}
	return m.inspectPID(pid)
}

func (m *ProcessManager) Children(ctx context.Context, parent domain.ProcessIdentity) ([]ports.ProcessStatus, error) {
	parentStatus, err := m.Inspect(ctx, parent)
	if err != nil {
		return nil, err
	}
	if !parentStatus.Running {
		return nil, nil
	}
	return m.scan(ctx, func(status ports.ProcessStatus) bool { return status.ParentPID == parent.PID })
}

func (m *ProcessManager) Group(ctx context.Context, pgid int) ([]ports.ProcessStatus, error) {
	if pgid <= 0 {
		return nil, errors.New("invalid process group")
	}
	entries, err := os.ReadDir(m.procRoot)
	if err != nil {
		return nil, err
	}
	var result []ports.ProcessStatus
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		candidate, err := m.inspectPIDStat(pid)
		if err != nil {
			if isProcessGone(err) {
				continue
			}
			return nil, fmt.Errorf("inspect process-group member %d stat: %w", pid, err)
		}
		if !candidate.Running || candidate.PGID != pgid {
			continue
		}
		status, err := m.inspectPID(pid)
		if err != nil {
			if isProcessGone(err) {
				continue
			}
			latest, latestErr := m.inspectPIDStat(pid)
			if reconcileErr := reconcileGroupInspectionFailure(candidate, latest, latestErr, err); reconcileErr != nil {
				return nil, fmt.Errorf("inspect process-group member %d: %w", pid, reconcileErr)
			}
			continue
		}
		if err := validateGroupCandidate(candidate, status); err != nil {
			return nil, fmt.Errorf("inspect process-group member %d changed during validation: %w", pid, err)
		}
		if status.Running && status.PGID == pgid {
			result = append(result, status)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Identity.PID < result[j].Identity.PID })
	return result, nil
}

func validateGroupCandidate(candidate, observed ports.ProcessStatus) error {
	if observed.Identity != candidate.Identity || observed.PGID != candidate.PGID {
		return ErrProcessIdentity
	}
	return nil
}

func reconcileGroupInspectionFailure(candidate, latest ports.ProcessStatus, latestErr, inspectErr error) error {
	if isProcessGone(latestErr) {
		return nil
	}
	if latestErr != nil {
		return errors.Join(inspectErr, latestErr)
	}
	if !latest.Running {
		return nil
	}
	if err := validateGroupCandidate(candidate, latest); err != nil {
		return errors.Join(inspectErr, err)
	}
	return inspectErr
}

func (m *ProcessManager) Stop(ctx context.Context, identity domain.ProcessIdentity, grace time.Duration) error {
	status, err := m.Inspect(ctx, identity)
	if err != nil {
		return err
	}
	if !status.Running {
		return nil
	}
	if err := syscall.Kill(identity.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate process %d: %w", identity.PID, err)
	}
	deadline := time.Now().Add(grace)
	for {
		gone, err := m.originalGone(ctx, identity)
		if err != nil {
			return err
		}
		if gone {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	status, err = m.Inspect(ctx, identity)
	if errors.Is(err, ErrProcessIdentity) || (err == nil && !status.Running) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := syscall.Kill(identity.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process %d: %w", identity.PID, err)
	}
	for attempts := 0; attempts < 500; attempts++ {
		gone, err := m.originalGone(ctx, identity)
		if err != nil {
			return err
		}
		if gone {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return fmt.Errorf("process %d remained after SIGKILL", identity.PID)
}

func (m *ProcessManager) originalGone(ctx context.Context, identity domain.ProcessIdentity) (bool, error) {
	status, err := m.Inspect(ctx, identity)
	if err == nil {
		return !status.Running, nil
	}
	latest, statErr := m.inspectPIDStat(identity.PID)
	if isProcessGone(statErr) {
		return true, nil
	}
	if statErr != nil {
		return false, errors.Join(err, statErr)
	}
	if latest.Identity != identity || !latest.Running {
		return true, nil
	}
	return false, nil
}

func (m *ProcessManager) scan(ctx context.Context, include func(ports.ProcessStatus) bool) ([]ports.ProcessStatus, error) {
	entries, err := os.ReadDir(m.procRoot)
	if err != nil {
		return nil, err
	}
	var result []ports.ProcessStatus
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		status, err := m.inspectPID(pid)
		if err != nil {
			if isProcessGone(err) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				continue
			}
			continue
		}
		if status.Running && include(status) {
			result = append(result, status)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Identity.PID < result[j].Identity.PID })
	return result, nil
}

func isProcessGone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func (m *ProcessManager) inspectPID(pid int) (ports.ProcessStatus, error) {
	status, err := m.inspectPIDStat(pid)
	if err != nil || !status.Running {
		return status, err
	}
	identity := status.Identity
	cmdline, err := os.ReadFile(filepath.Join(m.procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	argv := splitNUL(cmdline)
	executable, err := resolveObservedExecutable(filepath.Join(m.procRoot, strconv.Itoa(pid), "exe"), argv, os.Readlink)
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	netns, err := resolveObservedNetNS(filepath.Join(m.procRoot, strconv.Itoa(pid), "ns", "net"), identity, os.Readlink)
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	status.Executable = executable
	status.Argv = argv
	status.NetNS = netns
	return status, nil
}

func (m *ProcessManager) inspectPIDStat(pid int) (ports.ProcessStatus, error) {
	statBody, err := os.ReadFile(filepath.Join(m.procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	closing := strings.LastIndexByte(string(statBody), ')')
	if closing < 0 {
		return ports.ProcessStatus{}, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(statBody)[closing+1:])
	if len(fields) <= 19 {
		return ports.ProcessStatus{}, errors.New("short process stat")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	sid, err := strconv.Atoi(fields[3])
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return ports.ProcessStatus{}, err
	}
	identity := domain.ProcessIdentity{PID: pid, BootID: m.bootID, StartTicks: start}
	if fields[0] == "Z" {
		return ports.ProcessStatus{Identity: identity, Running: false, ParentPID: parent, PGID: pgid, SID: sid}, nil
	}
	return ports.ProcessStatus{
		Identity: identity, Running: true, ParentPID: parent, PGID: pgid, SID: sid,
	}, nil
}

func resolveObservedNetNS(path string, identity domain.ProcessIdentity, readlink func(string) (string, error)) (string, error) {
	netns, err := readlink(path)
	if err == nil {
		return netns, nil
	}
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
		return "", err
	}
	if identity.BootID == "" || identity.StartTicks == 0 {
		return "", ErrProcessIdentity
	}
	return "kernel-denied:" + identity.BootID + ":" + strconv.FormatUint(identity.StartTicks, 10), nil
}

func resolveObservedExecutable(exePath string, argv []string, readlink func(string) (string, error)) (string, error) {
	executable, err := readlink(exePath)
	if err == nil {
		return executable, nil
	}
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
		return "", err
	}
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) || filepath.Clean(argv[0]) != argv[0] {
		return "", fmt.Errorf("kernel denied executable identity and argv[0] is unsafe: %w", ErrProcessIdentity)
	}
	resolved, resolveErr := filepath.EvalSymlinks(argv[0])
	if resolveErr != nil {
		return "", fmt.Errorf("resolve denied executable argv[0]: %w", resolveErr)
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("validate denied executable argv[0]: %w", errors.Join(statErr, ErrProcessIdentity))
	}
	return resolved, nil
}

func (m *ProcessManager) startedIdentity(ctx context.Context, pid int) (domain.ProcessIdentity, error) {
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return domain.ProcessIdentity{}, err
		}
		status, err := m.inspectPID(pid)
		if err == nil {
			if !status.Running {
				return domain.ProcessIdentity{}, fmt.Errorf("started process %d exited before identity stabilized", pid)
			}
			if status.Executable != "" && len(status.Argv) > 0 && status.NetNS != "" {
				return status.Identity, nil
			}
			lastErr = errors.New("started process has incomplete runtime identity")
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return domain.ProcessIdentity{}, fmt.Errorf("inspect started process %d: %w", pid, lastErr)
		}
		select {
		case <-ctx.Done():
			return domain.ProcessIdentity{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func splitNUL(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(body), "\x00"), "\x00")
	return parts
}

func processEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if index := strings.IndexByte(entry, '='); index >= 0 {
			values[entry[:index]] = entry[index+1:]
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}
