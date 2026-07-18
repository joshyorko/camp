package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type PortAllocator struct{}

func NewPortAllocator() *PortAllocator { return &PortAllocator{} }

func (a *PortAllocator) Candidates(ctx context.Context, preferred, count int) ([]int, error) {
	if count < 1 || count > 32 || preferred < 0 || preferred > 65535 {
		return nil, errors.New("invalid port candidate request")
	}
	seen := make(map[int]struct{}, count)
	result := make([]int, 0, count)
	probe := func(address string) (int, bool) {
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			return 0, false
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		return port, true
	}
	if preferred > 0 {
		if port, ok := probe(net.JoinHostPort("127.0.0.1", strconv.Itoa(preferred))); ok {
			result = append(result, port)
			seen[port] = struct{}{}
		}
	}
	for attempts := 0; len(result) < count && attempts < count*20; attempts++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		port, ok := probe("127.0.0.1:0")
		if !ok {
			continue
		}
		if _, exists := seen[port]; exists {
			continue
		}
		seen[port] = struct{}{}
		result = append(result, port)
	}
	if len(result) != count {
		return nil, fmt.Errorf("could not discover %d distinct loopback port candidates", count)
	}
	return result, nil
}

type PortMapping struct {
	HostAddress string `json:"hostAddress" yaml:"hostAddress"`
	HostPort    int    `json:"hostPort" yaml:"hostPort"`
	GuestPort   int    `json:"guestPort" yaml:"guestPort"`
}

type EnsureUnit func(context.Context, domain.JournalSnapshot, ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)

func LaunchEndpoint(ctx context.Context, snapshot domain.JournalSnapshot, service ServiceSpec, candidates []int, committed int, ensure EnsureUnit) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	if ensure == nil {
		return domain.ServiceUnitRecord{}, snapshot, errors.New("endpoint launcher has no service ensurer")
	}
	if committed > 0 {
		service.Mapping.HostPort = committed
		return ensure(ctx, snapshot, service)
	}
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	for _, candidate := range candidates {
		service.Mapping.HostPort = candidate
		record, next, err := ensure(ctx, snapshot, service)
		if err == nil {
			return record, next, nil
		}
		if !errors.Is(err, ErrUnknownPortOccupant) {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
	}
	return domain.ServiceUnitRecord{}, snapshot, ErrUnknownPortOccupant
}

type PastaLoopback struct {
	Capability ConfinementCapability
	Mapping    PortMapping
	LogPath    string
	PIDPath    string
	Child      ports.Command
}

func BuildPastaLoopback(unit PastaLoopback) (ports.ProcessSpec, error) {
	if !filepath.IsAbs(unit.Capability.Executable) || unit.Capability.Version == "" || unit.Capability.EnvironmentFingerprint == "" {
		return ports.ProcessSpec{}, errors.New("PastaLoopback requires resolved confinement capability")
	}
	if unit.Mapping.HostAddress != "127.0.0.1" || unit.Mapping.HostPort < 1 || unit.Mapping.HostPort > 65535 || unit.Mapping.GuestPort < 1 || unit.Mapping.GuestPort > 65535 {
		return ports.ProcessSpec{}, errors.New("PastaLoopback requires exact IPv4 loopback ports")
	}
	if !filepath.IsAbs(unit.LogPath) || !filepath.IsAbs(unit.PIDPath) || unit.LogPath == unit.PIDPath {
		return ports.ProcessSpec{}, errors.New("PastaLoopback requires separate absolute private log and pid paths")
	}
	if !filepath.IsAbs(unit.Child.Executable) || len(unit.Child.Argv) == 0 {
		return ports.ProcessSpec{}, errors.New("PastaLoopback requires an exact absolute child command")
	}
	mapping := net.JoinHostPort(unit.Mapping.HostAddress, strconv.Itoa(unit.Mapping.HostPort))
	// pasta's HOST:GUEST mapping syntax uses a slash before the host endpoint.
	mapping = unit.Mapping.HostAddress + "/" + strconv.Itoa(unit.Mapping.HostPort) + ":" + strconv.Itoa(unit.Mapping.GuestPort)
	argv := []string{
		"--foreground", "--quiet", "--log-file", unit.LogPath, "--pid", unit.PIDPath,
		"--ipv4-only", "--host-lo-to-ns-lo", "--tcp-ports", mapping,
		"--udp-ports", "none", "--tcp-ns", "none", "--udp-ns", "none", "--",
		unit.Child.Executable,
	}
	argv = append(argv, unit.Child.Argv...)
	if mapping == "" {
		return ports.ProcessSpec{}, fmt.Errorf("invalid mapping")
	}
	return ports.ProcessSpec{
		Command: ports.Command{
			Executable:  unit.Capability.Executable,
			Argv:        argv,
			Directory:   unit.Child.Directory,
			Environment: cloneEnvironment(unit.Child.Environment),
			Redaction:   unit.Child.Redaction,
		},
		NewSession: true,
		LogPath:    unit.LogPath,
	}, nil
}

func cloneEnvironment(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type unitInspector struct {
	runner  ports.Runner
	http    HTTPDoer
	ss      string
	nsenter string
}

func NewUnitInspector(runner ports.Runner, httpClient HTTPDoer) UnitInspector {
	ss, _ := exec.LookPath("ss")
	nsenter, _ := exec.LookPath("nsenter")
	return &unitInspector{runner: runner, http: httpClient, ss: ss, nsenter: nsenter}
}

func (i *unitInspector) Prebind(ctx context.Context, mapping PortMapping) error {
	listener, err := net.Listen("tcp4", net.JoinHostPort(mapping.HostAddress, strconv.Itoa(mapping.HostPort)))
	if err != nil {
		return fmt.Errorf("host port %d: %w", mapping.HostPort, ErrUnknownPortOccupant)
	}
	_ = listener.Close()
	for _, family := range []string{"-4", "-6"} {
		output, err := i.hostListeners(ctx, family, mapping.HostPort)
		if err != nil {
			return err
		}
		if strings.TrimSpace(output) != "" {
			return ErrUnknownPortOccupant
		}
	}
	return nil
}

func (i *unitInspector) Ready(ctx context.Context, service ServiceSpec, helper, child ports.ProcessStatus) (UnitEvidence, error) {
	host4, err := i.hostListeners(ctx, "-4", service.Mapping.HostPort)
	if err != nil {
		return UnitEvidence{}, err
	}
	host6, err := i.hostListeners(ctx, "-6", service.Mapping.HostPort)
	if err != nil {
		return UnitEvidence{}, err
	}
	wantHost := net.JoinHostPort(service.Mapping.HostAddress, strconv.Itoa(service.Mapping.HostPort))
	if !onlyLocalEndpoint(host4, wantHost) || strings.TrimSpace(host6) != "" {
		return UnitEvidence{}, fmt.Errorf("host mapping is not exact IPv4 loopback: %w", ErrUnitInvariant)
	}
	for _, family := range []string{"-4", "-6"} {
		guestOnHost, err := i.hostListeners(ctx, family, service.Mapping.GuestPort)
		if err != nil {
			return UnitEvidence{}, err
		}
		if strings.TrimSpace(guestOnHost) != "" {
			return UnitEvidence{}, fmt.Errorf("guest port is exposed on host: %w", ErrUnitInvariant)
		}
	}
	guest, err := i.guestListeners(ctx, child.Identity.PID, service.Mapping.GuestPort)
	if err != nil {
		return UnitEvidence{}, err
	}
	if !strings.Contains(guest, ":"+strconv.Itoa(service.Mapping.GuestPort)) || !strings.Contains(guest, "pid="+strconv.Itoa(child.Identity.PID)) {
		return UnitEvidence{}, fmt.Errorf("guest listener is not owned by exact Hauler child: %w", ErrUnitInvariant)
	}
	path := "/"
	if service.Name == "registry" {
		path = "/v2/"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+wantHost+path, nil)
	if err != nil {
		return UnitEvidence{}, err
	}
	response, err := i.http.Do(request)
	if err != nil {
		return UnitEvidence{}, fmt.Errorf("service readiness HTTP: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return UnitEvidence{}, fmt.Errorf("service readiness HTTP status %d: %w", response.StatusCode, ErrUnitInvariant)
	}
	return UnitEvidence{HostEndpoint: wantHost, GuestEndpoint: net.JoinHostPort("127.0.0.1", strconv.Itoa(service.Mapping.GuestPort)), ChildNetNS: child.NetNS}, nil
}

func (i *unitInspector) Stopped(ctx context.Context, record domain.ServiceUnitRecord) error {
	for _, port := range []int{record.Mapping.HostPort, record.Mapping.GuestPort} {
		if port <= 0 {
			continue
		}
		for _, family := range []string{"-4", "-6"} {
			listeners, err := i.hostListeners(ctx, family, port)
			if err != nil {
				return err
			}
			if strings.TrimSpace(listeners) != "" {
				return fmt.Errorf("service listener remains on port %d: %w", port, ErrUnitInvariant)
			}
		}
	}
	if record.PIDPath != "" {
		body, err := os.ReadFile(record.PIDPath)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
			if parseErr != nil || pid != record.Helper.Identity.PID {
				return fmt.Errorf("private pidfile ownership changed: %w", ErrUnitInvariant)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (i *unitInspector) Absent(ctx context.Context, record domain.ServiceUnitRecord) error {
	if err := i.Stopped(ctx, record); err != nil {
		return err
	}
	if record.PIDPath != "" {
		if err := os.Remove(record.PIDPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (i *unitInspector) hostListeners(ctx context.Context, family string, port int) (string, error) {
	if i.runner == nil || i.ss == "" {
		return "", errors.New("ss is unavailable for service inspection")
	}
	result, err := i.runner.Run(ctx, ports.Command{Executable: i.ss, Argv: []string{"-H", "-ltn" + strings.TrimPrefix(family, "-"), "sport", "=", ":" + strconv.Itoa(port)}})
	if err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("inspect host listeners: %w", err)
	}
	return string(result.Stdout), nil
}

func (i *unitInspector) guestListeners(ctx context.Context, pid, port int) (string, error) {
	if i.runner == nil || i.ss == "" || i.nsenter == "" {
		return "", errors.New("nsenter and ss are required for guest inspection")
	}
	result, err := i.runner.Run(ctx, ports.Command{Executable: i.nsenter, Argv: []string{"--target", strconv.Itoa(pid), "--net", "--", i.ss, "-H", "-ltnp", "sport", "=", ":" + strconv.Itoa(port)}})
	if err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("inspect guest listener: %w", err)
	}
	return string(result.Stdout), nil
}

func onlyLocalEndpoint(output, expected string) bool {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return false
	}
	fields := strings.Fields(lines[0])
	return len(fields) >= 4 && fields[3] == expected
}
