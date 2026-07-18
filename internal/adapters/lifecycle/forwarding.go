package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
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
	if m == nil || m.devpod == nil || m.processes == nil || request.Name == "" || request.WorkspaceID == "" || request.LogPath == "" {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder dependencies or request are incomplete")
	}
	if err := validateForwardEndpoint(request.LocalEndpoint); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if request.WorkspaceEndpoint != request.LocalEndpoint {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder endpoints must use the same loopback port")
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
	cleanup := func(cause error) (domain.ForwardingRecord, error) {
		stopErr := m.processes.Stop(context.WithoutCancel(ctx), identity, 5*time.Second)
		return domain.ForwardingRecord{}, errors.Join(cause, stopErr)
	}
	status, err := m.processes.Inspect(ctx, identity)
	if err != nil || !status.Running {
		return cleanup(errors.Join(err, errors.New("workspace forwarder exited before readiness")))
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
			return cleanup(errors.Join(err, inspectErr, errors.New("workspace forwarder exited during readiness")))
		}
		if !time.Now().Before(deadline) {
			return cleanup(fmt.Errorf("workspace endpoint %s did not become ready: %w", request.WorkspaceEndpoint, err))
		}
		select {
		case <-ctx.Done():
			return cleanup(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	digest := sha256.Sum256([]byte(strings.Join(status.Argv, "\x00")))
	return domain.ForwardingRecord{
		Name: request.Name, LocalEndpoint: request.LocalEndpoint, WorkspaceEndpoint: request.WorkspaceEndpoint,
		Process: domain.ProcessRecord{
			Identity: identity, DesiredExecutable: command.Executable, ObservedExecutable: status.Executable,
			Argv: append([]string(nil), status.Argv...), ArgvSHA256: hex.EncodeToString(digest[:]),
			ParentPID: status.ParentPID, PGID: status.PGID, SID: status.SID, NetNS: status.NetNS,
		}, DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
	}, nil
}

func (m *ForwarderManager) Stop(ctx context.Context, record domain.ForwardingRecord) error {
	if m == nil || m.processes == nil || record.Process.Identity.PID <= 0 {
		return errors.New("workspace forwarder identity is incomplete")
	}
	return m.processes.Stop(ctx, record.Process.Identity, 5*time.Second)
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
