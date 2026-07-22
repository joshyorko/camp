//go:build linux

package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type HostCapabilityProbe struct {
	Name  string
	Check func(context.Context) (map[string]string, error)
}

func (p HostCapabilityProbe) Capability() string { return p.Name }

func (p HostCapabilityProbe) Probe(ctx context.Context) Result {
	codeName := strings.ReplaceAll(p.Name, "-", "_")
	if p.Check == nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: codeName + "_probe_unconfigured", Summary: p.Name + " probe is unavailable", Remediation: "repair Camp composition, then rerun camp doctor"}
	}
	evidence, err := p.Check(ctx)
	if err != nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: codeName + "_unavailable", Summary: p.Name + " is unavailable", Remediation: "enable or repair this Linux host capability, then rerun camp doctor"}
	}
	return Result{Capability: p.Name, Status: StatusHealthy, Code: codeName + "_available", Summary: p.Name + " is available", Evidence: evidence}
}

func LinuxHostProbes() []Probe {
	return []Probe{
		HostCapabilityProbe{Name: "proc-self-fd", Check: checkProcSelfFD},
		HostCapabilityProbe{Name: "tun", Check: checkTun},
		HostCapabilityProbe{Name: "user-namespace", Check: checkUserNamespace},
		HostCapabilityProbe{Name: "lsm", Check: checkLSM},
		HostCapabilityProbe{Name: "container-boundary", Check: checkContainerBoundary},
	}
}

func checkProcSelfFD(context.Context) (map[string]string, error) {
	file, err := os.CreateTemp("", "camp-doctor-fd-")
	if err != nil {
		return nil, err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	link := filepath.Join("/proc/self/fd", fileDescriptor(file))
	target, err := os.Readlink(link)
	if err != nil || target != name {
		return nil, errors.New("proc fd link did not resolve to the opened file")
	}
	return map[string]string{"path": "/proc/self/fd", "operation": "open-readlink"}, nil
}

func fileDescriptor(file *os.File) string {
	return strconv.FormatUint(uint64(file.Fd()), 10)
}

func checkTun(context.Context) (map[string]string, error) {
	file, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return map[string]string{"path": "/dev/net/tun", "operation": "open"}, nil
}

func checkUserNamespace(ctx context.Context) (map[string]string, error) {
	path, err := exec.LookPath("unshare")
	if err != nil {
		return nil, err
	}
	if err := exec.CommandContext(ctx, path, "--user", "--map-root-user", "true").Run(); err != nil {
		return nil, err
	}
	return map[string]string{"operation": "unshare-user-map-root"}, nil
}

func checkLSM(context.Context) (map[string]string, error) {
	for _, path := range []string{"/proc/self/attr/current", "/sys/kernel/security/lsm"} {
		body, err := os.ReadFile(path)
		value := strings.TrimSpace(string(body))
		if err == nil && value != "" {
			return map[string]string{"path": path, "context": safeSingleLine(value)}, nil
		}
	}
	return nil, errors.New("no active LSM evidence")
}

func checkContainerBoundary(context.Context) (map[string]string, error) {
	marker := false
	for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(path); err == nil {
			marker = true
			break
		}
	}
	cgroup := ""
	if body, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		cgroup = string(body)
	}
	boundary := detectContainerBoundary(marker, cgroup)
	return map[string]string{"boundary": boundary, "operation": "proc-and-marker-inspection"}, nil
}

func detectContainerBoundary(marker bool, cgroup string) string {
	if marker {
		return "container"
	}
	cgroup = strings.ToLower(cgroup)
	for _, signature := range []string{"docker", "containerd", "kubepods", "libpod"} {
		if strings.Contains(cgroup, signature) {
			return "container"
		}
	}
	return "host"
}
