package lifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"golang.org/x/sys/unix"
)

const (
	maxForwardingEvidenceBytes       = 64 << 10
	defaultForwarderStartAttempts    = 2
	defaultForwarderReadinessTimeout = 15 * time.Second
	defaultForwarderReadinessPoll    = 100 * time.Millisecond
)

var errForwarderEndpointNotReady = errors.New("workspace forwarder endpoint is not ready")

type forwardingEvidenceIdentity struct {
	device uint64
	inode  uint64
}

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
	devpod            forwardDevPod
	processes         forwardProcesses
	resolveExecutable func(string) (string, error)
	startAttempts     int
	readinessTimeout  time.Duration
	readinessInterval time.Duration
}

func NewForwarderManager(client forwardDevPod, processes forwardProcesses) *ForwarderManager {
	return &ForwarderManager{
		devpod: client, processes: processes, resolveExecutable: canonicalForwardingExecutable,
		startAttempts: defaultForwarderStartAttempts, readinessTimeout: defaultForwarderReadinessTimeout,
		readinessInterval: defaultForwarderReadinessPoll,
	}
}

func (m *ForwarderManager) Start(ctx context.Context, request domain.ForwardingRequest) (domain.ForwardingRecord, error) {
	if m == nil || m.devpod == nil || m.processes == nil {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder dependencies or request are incomplete")
	}
	if err := validateForwardingRequest(request); err != nil {
		return domain.ForwardingRecord{}, err
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
	expectedExecutable, err := m.expectedExecutable(command.Executable)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	var lastErr error
	for attempt := 1; attempt <= m.startAttempts; attempt++ {
		record, retryable, err := m.startAttempt(ctx, request, command, expectedExecutable)
		if err == nil {
			return record, nil
		}
		lastErr = err
		if !retryable || attempt == m.startAttempts {
			return domain.ForwardingRecord{}, err
		}
	}
	return domain.ForwardingRecord{}, lastErr
}

func (m *ForwarderManager) startAttempt(ctx context.Context, request domain.ForwardingRequest, command ports.Command, expectedExecutable string) (domain.ForwardingRecord, bool, error) {
	identity, err := m.processes.Start(ctx, ports.ProcessSpec{Command: command, NewSession: true, LogPath: request.LogPath})
	if err != nil {
		return domain.ForwardingRecord{}, false, err
	}
	status, err := m.processes.Inspect(ctx, identity)
	if err != nil || !status.Running {
		record, cleanupErr := m.cleanupStartedProcess(ctx, identity, errors.Join(err, errors.New("workspace forwarder exited before readiness")))
		return record, false, cleanupErr
	}
	record, err := newForwardingRecord(request, command, expectedExecutable, identity, status)
	if err != nil {
		record, cleanupErr := m.cleanupStartedProcess(ctx, identity, err)
		return record, false, cleanupErr
	}
	evidenceIdentity, err := writeForwardingEvidence(request.EvidencePath, record)
	if err != nil {
		// Publication may have failed because another inode won the no-replace
		// race. Never remove a path this attempt cannot prove it created.
		record, cleanupErr := m.cleanupStartedProcess(ctx, identity, err)
		return record, false, cleanupErr
	}
	record.EvidenceDevice = evidenceIdentity.device
	record.EvidenceInode = evidenceIdentity.inode
	if err := m.waitForReadiness(ctx, request, identity); err != nil {
		cleanupErr := m.cleanupForwarder(context.WithoutCancel(ctx), record)
		if cleanupErr != nil {
			return domain.ForwardingRecord{}, false, errors.Join(err, cleanupErr)
		}
		return domain.ForwardingRecord{}, errors.Is(err, errForwarderEndpointNotReady), err
	}
	record.ObservedState = domain.RuntimeObservedReady
	return record, false, nil
}

func (m *ForwarderManager) Stop(ctx context.Context, record domain.ForwardingRecord) error {
	if m == nil || m.processes == nil || record.Process.Identity.PID <= 0 {
		return errors.New("workspace forwarder identity is incomplete")
	}
	if err := m.processes.Stop(ctx, record.Process.Identity, 5*time.Second); err != nil && !errors.Is(err, supervisor.ErrProcessIdentity) {
		return err
	}
	if record.EvidencePath != "" {
		if err := removeForwardingEvidence(record); err != nil {
			return err
		}
	}
	return nil
}

func (m *ForwarderManager) Observe(ctx context.Context, request domain.ForwardingRequest) (domain.ForwardingRecord, error) {
	if m == nil || m.devpod == nil || m.processes == nil {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder dependencies or request are incomplete")
	}
	if err := validateForwardingRequest(request); err != nil {
		return domain.ForwardingRecord{}, err
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
	expectedExecutable, err := m.expectedExecutable(command.Executable)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	status, err := m.processes.Inspect(ctx, record.Process.Identity)
	if err != nil || !status.Running {
		return domain.ForwardingRecord{}, errors.Join(err, errors.New("workspace forwarder is not running"))
	}
	if err := validateForwardingRecord(command, expectedExecutable, record, status); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if err := m.waitForReadiness(ctx, request, record.Process.Identity); err != nil {
		return domain.ForwardingRecord{}, err
	}
	record.ObservedState = domain.RuntimeObservedReady
	return record, nil
}

func (m *ForwarderManager) waitForReadiness(ctx context.Context, request domain.ForwardingRequest, identity domain.ProcessIdentity) error {
	probeURL := "http://" + request.WorkspaceEndpoint + "/"
	if request.Name == "registry" {
		probeURL += "v2/"
	}
	probe := ports.WorkspaceCommand{WorkspaceID: request.WorkspaceID, Context: request.Context, Argv: []string{"curl", "--fail", "--silent", "--show-error", "--max-time", "5", probeURL}}
	deadline := time.Now().Add(m.readinessTimeout)
	var lastErr error
	for {
		if _, err := m.devpod.Execute(ctx, probe); err == nil {
			return nil
		} else {
			lastErr = err
		}
		status, inspectErr := m.processes.Inspect(ctx, identity)
		if inspectErr != nil || !status.Running {
			return errors.Join(lastErr, inspectErr, errors.New("workspace forwarder exited during readiness"))
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("workspace endpoint %s did not become ready: %w: %v", request.WorkspaceEndpoint, errForwarderEndpointNotReady, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.readinessInterval):
		}
	}
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

func validateForwardingRequest(request domain.ForwardingRequest) error {
	if request.Name == "" || request.WorkspaceID == "" || request.Context == "" || request.LogPath == "" || request.EvidencePath == "" {
		return errors.New("workspace forwarder request is incomplete")
	}
	if !filepath.IsAbs(request.LogPath) || !filepath.IsAbs(request.EvidencePath) || filepath.Dir(request.LogPath) != filepath.Dir(request.EvidencePath) || request.LogPath == request.EvidencePath {
		return errors.New("workspace forwarder log and evidence paths must be distinct absolute siblings")
	}
	if err := validateForwardEndpoint(request.LocalEndpoint); err != nil {
		return err
	}
	if err := validateForwardEndpoint(request.WorkspaceEndpoint); err != nil {
		return err
	}
	return nil
}

func newForwardingRecord(request domain.ForwardingRequest, command ports.Command, expectedExecutable string, identity domain.ProcessIdentity, status ports.ProcessStatus) (domain.ForwardingRecord, error) {
	record := domain.ForwardingRecord{
		Name: request.Name, LocalEndpoint: request.LocalEndpoint, WorkspaceEndpoint: request.WorkspaceEndpoint, EvidencePath: request.EvidencePath,
		Process: domain.ProcessRecord{
			Identity: identity, DesiredExecutable: command.Executable, ObservedExecutable: status.Executable,
			Argv: append([]string(nil), status.Argv...), ArgvSHA256: forwardingArgvDigest(status.Argv),
			ParentPID: status.ParentPID, PGID: status.PGID, SID: status.SID, NetNS: status.NetNS,
		}, DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedPending,
	}
	if err := validateForwardingRecord(command, expectedExecutable, record, status); err != nil {
		return domain.ForwardingRecord{}, err
	}
	return record, nil
}

func validateForwardingRecord(command ports.Command, expectedExecutable string, record domain.ForwardingRecord, status ports.ProcessStatus) error {
	expectedArgv := append([]string{command.Executable}, command.Argv...)
	identity := record.Process.Identity
	if identity.PID <= 0 || identity.BootID == "" || identity.StartTicks == 0 || status.Identity != identity || !status.Running ||
		record.DesiredState != domain.RuntimeDesiredRunning || record.ObservedState != domain.RuntimeObservedPending ||
		record.Process.DesiredExecutable != command.Executable || record.Process.ObservedExecutable != expectedExecutable || status.Executable != expectedExecutable ||
		!reflect.DeepEqual(record.Process.Argv, expectedArgv) || !reflect.DeepEqual(status.Argv, expectedArgv) || record.Process.ArgvSHA256 != forwardingArgvDigest(expectedArgv) ||
		status.PGID != record.Process.PGID || status.SID != record.Process.SID || status.NetNS != record.Process.NetNS ||
		status.PGID != identity.PID || status.SID != identity.PID || status.NetNS == "" {
		return errors.New("workspace forwarder evidence does not match the expected live process")
	}
	return nil
}

func (m *ForwarderManager) expectedExecutable(path string) (string, error) {
	if m.resolveExecutable == nil {
		return "", errors.New("workspace forwarder executable resolver is incomplete")
	}
	return m.resolveExecutable(path)
}

func canonicalForwardingExecutable(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace forwarder executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("validate workspace forwarder executable: %w", errors.Join(err, errors.New("not an executable regular file")))
	}
	return resolved, nil
}

func forwardingArgvDigest(argv []string) string {
	digest := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(digest[:])
}

func writeForwardingEvidence(path string, record domain.ForwardingRecord) (forwardingEvidenceIdentity, error) {
	if record.EvidenceDevice != 0 || record.EvidenceInode != 0 {
		return forwardingEvidenceIdentity{}, errors.New("workspace forwarder evidence identity must be empty before publication")
	}
	body, err := json.Marshal(record)
	if err != nil {
		return forwardingEvidenceIdentity{}, err
	}
	if len(body) == 0 || len(body) > maxForwardingEvidenceBytes {
		return forwardingEvidenceIdentity{}, errors.New("workspace forwarder evidence exceeds the size bound")
	}
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".forwarding-evidence-*")
	if err != nil {
		return forwardingEvidenceIdentity{}, err
	}
	temporary := file.Name()
	installed := false
	defer func() {
		if !installed {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return forwardingEvidenceIdentity{}, err
	}
	if written, err := file.Write(body); err != nil || written != len(body) {
		_ = file.Close()
		return forwardingEvidenceIdentity{}, errors.Join(err, io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return forwardingEvidenceIdentity{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		_ = file.Close()
		return forwardingEvidenceIdentity{}, err
	}
	if err := file.Close(); err != nil {
		return forwardingEvidenceIdentity{}, err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, temporary, unix.AT_FDCWD, path, unix.RENAME_NOREPLACE); err != nil {
		return forwardingEvidenceIdentity{}, err
	}
	installed = true
	if err := syncParentDirectory(path); err != nil {
		return forwardingEvidenceIdentity{}, err
	}
	return forwardingEvidenceIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func readForwardingEvidence(path string) (domain.ForwardingRecord, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return domain.ForwardingRecord{}, errors.New("open workspace forwarder evidence")
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o7777 != 0o600 || before.Nlink != 1 || before.Uid != uint32(os.Geteuid()) || before.Size <= 0 || before.Size > maxForwardingEvidenceBytes {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence shape, ownership, permissions, link count, or size is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxForwardingEvidenceBytes+1))
	if err != nil {
		return domain.ForwardingRecord{}, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if len(body) != int(before.Size) || len(body) > maxForwardingEvidenceBytes || !sameForwardingEvidenceStat(before, after) {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence changed while being read")
	}
	var record domain.ForwardingRecord
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if err := ensureForwardingEvidenceEOF(decoder); err != nil {
		return domain.ForwardingRecord{}, err
	}
	if record.EvidencePath != path {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence path does not match record")
	}
	if record.EvidenceDevice != 0 || record.EvidenceInode != 0 {
		return domain.ForwardingRecord{}, errors.New("workspace forwarder evidence contains an untrusted inode identity")
	}
	record.EvidenceDevice = uint64(before.Dev)
	record.EvidenceInode = before.Ino
	return record, nil
}

func sameForwardingEvidenceStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Nlink == right.Nlink && left.Uid == right.Uid && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func ensureForwardingEvidenceEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workspace forwarder evidence contains trailing JSON")
		}
		return err
	}
	return nil
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

func removeForwardingEvidence(record domain.ForwardingRecord) error {
	if !filepath.IsAbs(record.EvidencePath) || record.EvidenceDevice == 0 || record.EvidenceInode == 0 {
		return errors.New("workspace forwarder evidence removal identity is incomplete")
	}
	directory := filepath.Dir(record.EvidencePath)
	name := filepath.Base(record.EvidencePath)
	dirFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	quarantine := "." + name + ".remove-" + hex.EncodeToString(nonce)
	if err := unix.Renameat2(dirFD, name, dirFD, quarantine, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	fd, err := unix.Openat(dirFD, quarantine, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Renameat2(dirFD, quarantine, dirFD, name, unix.RENAME_NOREPLACE)
		return err
	}
	var stat unix.Stat_t
	statErr := unix.Fstat(fd, &stat)
	_ = unix.Close(fd)
	if statErr != nil || uint64(stat.Dev) != record.EvidenceDevice || stat.Ino != record.EvidenceInode {
		restoreErr := unix.Renameat2(dirFD, quarantine, dirFD, name, unix.RENAME_NOREPLACE)
		return errors.Join(statErr, restoreErr, errors.New("workspace forwarder evidence inode changed before removal"))
	}
	if err := unix.Unlinkat(dirFD, quarantine, 0); err != nil {
		return err
	}
	return unix.Fsync(dirFD)
}

func (m *ForwarderManager) cleanupForwarder(ctx context.Context, record domain.ForwardingRecord) error {
	if stopErr := m.processes.Stop(ctx, record.Process.Identity, 5*time.Second); stopErr != nil {
		return stopErr
	}
	if err := removeForwardingEvidence(record); err != nil {
		return err
	}
	return nil
}

func (m *ForwarderManager) cleanupStartedProcess(ctx context.Context, identity domain.ProcessIdentity, cause error) (domain.ForwardingRecord, error) {
	if stopErr := m.processes.Stop(context.WithoutCancel(ctx), identity, 5*time.Second); stopErr != nil {
		return domain.ForwardingRecord{}, errors.Join(cause, stopErr)
	}
	return domain.ForwardingRecord{}, cause
}
