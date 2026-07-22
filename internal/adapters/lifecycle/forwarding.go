package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type forwardDevPod interface {
	SSHCommand(devpod.SSHOptions) (ports.Command, error)
	Execute(context.Context, ports.WorkspaceCommand) (ports.Result, error)
}

type forwardProcesses interface {
	Start(context.Context, ports.ProcessSpec) (domain.ProcessIdentity, error)
	Inspect(context.Context, domain.ProcessIdentity) (ports.ProcessStatus, error)
	Stop(context.Context, domain.ProcessIdentity, time.Duration) error
}

type ForwarderManager struct {
	devpod    forwardDevPod
	processes forwardProcesses
}

func NewForwarderManager(client forwardDevPod, processes forwardProcesses) *ForwarderManager {
	return &ForwarderManager{devpod: client, processes: processes}
}

func (m *ForwarderManager) Start(ctx context.Context, request domain.ForwardingRequest) (domain.ForwardingRecord, error) {
	if m == nil || m.devpod == nil || m.processes == nil || request.Name == "" || request.WorkspaceID == "" || request.LogPath == "" || request.EvidencePath == "" {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder dependencies or request are incomplete")
	}
	if !filepath.IsAbs(request.EvidencePath) {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence path must be absolute")
	}
	if err := validateForwardEndpoint(request.LocalEndpoint); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if request.WorkspaceEndpoint != request.LocalEndpoint {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder endpoints must use the same loopback port")
	}
	if _, err := os.Lstat(request.EvidencePath); err == nil {
		return domain.ForwardingRecord{}, fmt.Errorf("workspace forwarder evidence already exists: %s", request.EvidencePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.ForwardingRecord{}, err
	}
	binding := request.WorkspaceEndpoint + ":" + request.LocalEndpoint
	options := devpod.SSHOptions{
		WorkspaceID: request.WorkspaceID, Context: request.Context, ReverseForwards: []string{binding},
		StartServices: true, ForwardedArgv: []string{"--command", "sleep 2147483647"},
	}
	command, err := m.devpod.SSHCommand(options)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	identity, err := m.processes.Start(ctx, ports.ProcessSpec{Command: command, NewSession: true, LogPath: request.LogPath})
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	status, err := m.processes.Inspect(ctx, identity)
	if err != nil || !status.Running {
		return m.cleanupForwarder(ctx, identity, request.EvidencePath, errors.Join(err, errors.New("workspace forwarder exited before readiness")))
	}
	record := domain.ForwardingRecord{
		Name: request.Name, LocalEndpoint: request.LocalEndpoint, WorkspaceEndpoint: request.WorkspaceEndpoint, EvidencePath: request.EvidencePath,
		Process: domain.ProcessRecord{
			Identity: identity, DesiredExecutable: command.Executable, ObservedExecutable: status.Executable,
			Argv: append([]string(nil), status.Argv...), ArgvSHA256: func() string { digest := sha256.Sum256([]byte(strings.Join(status.Argv, "\x00"))); return hex.EncodeToString(digest[:]) }(),
			ParentPID: status.ParentPID, PGID: status.PGID, SID: status.SID, NetNS: status.NetNS,
		}, DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedPending,
	}
	if err := writeForwardingEvidence(request.EvidencePath, record); err != nil {
		return m.cleanupForwarder(ctx, identity, request.EvidencePath, err)
	}
	probeURL := "http://" + request.WorkspaceEndpoint + "/"
	if request.Name == "registry" {
		probeURL += "v2/"
	}
	probe := ports.WorkspaceCommand{WorkspaceID: request.WorkspaceID, Context: request.Context, Argv: []string{"curl", "--fail", "--silent", "--show-error", "--max-time", "5", probeURL}}
	deadline := time.Now().Add(15 * time.Second)
	for {
	if _, err = m.devpod.Execute(ctx, probe); err == nil {
			break
		}
		status, inspectErr := m.processes.Inspect(ctx, identity)
		if inspectErr != nil || !status.Running {
			return m.cleanupForwarder(ctx, identity, request.EvidencePath, errors.Join(err, inspectErr, errors.New("workspace forwarder exited during readiness")))
		}
		if !time.Now().Before(deadline) {
			return m.cleanupForwarder(ctx, identity, request.EvidencePath, fmt.Errorf("workspace endpoint %s did not become ready: %w", request.WorkspaceEndpoint, err))
		}
		select {
		case <-ctx.Done():
			return m.cleanupForwarder(ctx, identity, request.EvidencePath, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	record.ObservedState = domain.RuntimeObservedReady
	return record, nil
}

func (m *ForwarderManager) Stop(ctx context.Context, record domain.ForwardingRecord) error {
	if m == nil || m.processes == nil || record.Process.Identity.PID <= 0 {
		return errors.New("workspace forwarder identity is incomplete")
	}
	if err := m.processes.Stop(ctx, record.Process.Identity, 5*time.Second); err != nil {
		return err
	}
	if record.EvidencePath != "" {
		if err := os.Remove(record.EvidencePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *ForwarderManager) Observe(ctx context.Context, request domain.ForwardingRequest) (domain.ForwardingRecord, error) {
	if m == nil || m.devpod == nil || m.processes == nil || request.Name == "" || request.WorkspaceID == "" || request.LogPath == "" || request.EvidencePath == "" {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder dependencies or request are incomplete")
	}
	if !filepath.IsAbs(request.EvidencePath) {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence path must be absolute")
	}
	record, err := readForwardingEvidence(request.EvidencePath)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	if record.Name != request.Name || record.LocalEndpoint != request.LocalEndpoint || record.WorkspaceEndpoint != request.WorkspaceEndpoint || record.EvidencePath != request.EvidencePath {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence does not match request")
	}
	options := devpod.SSHOptions{
		WorkspaceID: request.WorkspaceID, Context: request.Context, ReverseForwards: []string{request.WorkspaceEndpoint + ":" + request.LocalEndpoint},
		StartServices: true, ForwardedArgv: []string{"--command", "sleep 2147483647"},
	}
	command, err := m.devpod.SSHCommand(options)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	status, err := m.processes.Inspect(ctx, record.Process.Identity)
	if err != nil || !status.Running {
		return domain.ForwardingRecord{}, errors.Join(err, errors.New("workspace forwarder is not running"))
	}
	if status.Identity != record.Process.Identity || status.Executable != record.Process.ObservedExecutable || !reflect.DeepEqual(status.Argv, record.Process.Argv) || status.PGID != record.Process.PGID || status.SID != record.Process.SID || status.NetNS != record.Process.NetNS {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence does not match the live process")
	}
	if record.Process.DesiredExecutable != command.Executable {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence does not match expected command")
	}
	probeURL := "http://" + request.WorkspaceEndpoint + "/"
	if request.Name == "registry" {
		probeURL += "v2/"
	}
	if _, err = m.devpod.Execute(ctx, ports.WorkspaceCommand{WorkspaceID: request.WorkspaceID, Context: request.Context, Argv: []string{"curl", "--fail", "--silent", "--show-error", "--max-time", "5", probeURL}}); err != nil {
		return domain.ForwardingRecord{}, err
	}
	record.ObservedState = domain.RuntimeObservedReady
	return record, nil
}

func validateForwardEndpoint(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host != "127.0.0.1" {
		return errors.New("workspace forwarder requires an IPv4 loopback endpoint")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("workspace forwarder requires a nonzero port")
	}
	if parsed, err := url.Parse("http://" + value); err != nil || parsed.Hostname() != host {
		return errors.New("workspace forwarder endpoint is invalid")
	}
	return nil
}

func writeForwardingEvidence(path string, record domain.ForwardingRecord) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncParentDirectory(path)
}

func readForwardingEvidence(path string) (domain.ForwardingRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence is not a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence has unsafe permissions")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence is not one-link bounded")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	var record domain.ForwardingRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if record.EvidencePath != path {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence path does not match record")
	}
	return record, nil
}

func syncParentDirectory(path string) error {
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (m *ForwarderManager) cleanupForwarder(ctx context.Context, identity domain.ProcessIdentity, evidencePath string, cause error) (domain.ForwardingRecord, error) {
	if stopErr := m.processes.Stop(context.WithoutCancel(ctx), identity, 5*time.Second); stopErr != nil {
		return domain.ForwardingRecord{}, errors.Join(cause, stopErr)
	}
	if err := os.Remove(evidencePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return domain.ForwardingRecord{}, errors.Join(cause, err)
	}
	return domain.ForwardingRecord{}, cause
}
