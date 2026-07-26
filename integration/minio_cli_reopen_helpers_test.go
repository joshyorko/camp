package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
)

type createdWorkspaceTracker struct {
	ids  []string
	seen map[string]struct{}
}

type lifecycleEndpoints struct{ Registry, Fileserver string }

// lifecycleScenario owns only paths and identities created beneath its temporary
// root.  Its cleanup is intentionally reusable from normal and interrupted test
// paths; it never enumerates or deletes ambient DevPod resources.
type lifecycleScenario struct {
	root         string
	devPod       devPodTestIsolation
	unrelatedID  string
	controllers  []string
	environments map[string][]string
	workspaces   *createdWorkspaceTracker
	processes    map[domain.ProcessIdentity]struct{}
	paths        map[string]struct{}
	removePaths  map[string]struct{}
	listeners    map[string]struct{}
}

func newLifecycleScenario(t *testing.T, root string, devPod devPodTestIsolation, controllers ...string) *lifecycleScenario {
	t.Helper()
	scenario := &lifecycleScenario{
		root: root, devPod: devPod, controllers: controllers, environments: map[string][]string{},
		workspaces: newCreatedWorkspaceTracker(), processes: map[domain.ProcessIdentity]struct{}{}, paths: map[string]struct{}{}, removePaths: map[string]struct{}{}, listeners: map[string]struct{}{},
	}
	for _, controller := range controllers {
		runtimeDirectory := scenarioRuntimeDirectory(controller)
		if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
			t.Fatalf("create private XDG runtime directory: %v", err)
		}
		if err := os.Chmod(runtimeDirectory, 0o700); err != nil {
			t.Fatalf("secure private XDG runtime directory: %v", err)
		}
		scenario.paths[runtimeDirectory] = struct{}{}
		scenario.removePaths[runtimeDirectory] = struct{}{}
	}
	return scenario
}

func (s *lifecycleScenario) CreateUnrelatedWorkspace(t *testing.T, ctx context.Context) {
	t.Helper()
	source := filepath.Join(s.root, "unrelated-workspace")
	if err := os.MkdirAll(filepath.Join(source, ".devcontainer"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".devcontainer", "devcontainer.json"), []byte(`{"image":"alpine:3.20"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(filepath.Clean(source)))
	id := fmt.Sprintf("camp-unrelated-%x", digest[:6])
	output, err := runDevPodCommand(ctx, s.devPod, "up", "--ide", "none", "--open-ide=false", "--id", id, "--provider", "docker", source)
	if err != nil {
		t.Fatalf("create unrelated exact DevPod workspace %q: %v\n%s", id, err, output)
	}
	s.unrelatedID = id
}

func (s *lifecycleScenario) RegisterController(controller string, environment []string) {
	s.environments[controller] = append([]string(nil), environment...)
}

func (s *lifecycleScenario) TrackController(t *testing.T, controller string) {
	t.Helper()
	if err := s.trackController(controller); err != nil {
		t.Fatal(err)
	}
}

func (s *lifecycleScenario) trackController(controller string) error {
	matches, err := filepath.Glob(filepath.Join(controller, "data", "camp", "sessions", "*", "snapshot.json"))
	if err != nil {
		return fmt.Errorf("find scenario snapshots: %w", err)
	}
	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read scenario snapshot %q: %w", path, err)
		}
		var snapshot struct {
			Workspace struct {
				ID string `json:"id"`
			} `json:"workspace"`
			Materialization struct {
				CanonicalPath    string `json:"canonicalPath"`
				CleanupPermitted bool   `json:"cleanupPermitted"`
			} `json:"materialization"`
			Recovery struct {
				Forwarding []struct {
					Name          string `json:"name"`
					LocalEndpoint string `json:"localEndpoint"`
					EvidencePath  string `json:"evidencePath"`
					Process       struct {
						Identity domain.ProcessIdentity `json:"identity"`
					} `json:"process"`
				} `json:"forwarding"`
			} `json:"recovery"`
			Services []struct {
				Helper struct {
					Identity domain.ProcessIdentity `json:"identity"`
				} `json:"helper"`
				Child struct {
					Identity domain.ProcessIdentity `json:"identity"`
				} `json:"child"`
			} `json:"services"`
			Supervisor struct {
				Identity domain.ProcessIdentity `json:"identity"`
			} `json:"supervisor"`
		}
		if err := json.Unmarshal(body, &snapshot); err != nil {
			return fmt.Errorf("decode scenario snapshot %q: %w", path, err)
		}
		s.workspaces.Track(snapshot.Workspace.ID)
		if snapshot.Materialization.CleanupPermitted && snapshot.Materialization.CanonicalPath != "" {
			s.paths[snapshot.Materialization.CanonicalPath] = struct{}{}
		}
		for _, forwarding := range snapshot.Recovery.Forwarding {
			if forwarding.LocalEndpoint != "" {
				s.listeners[forwarding.LocalEndpoint] = struct{}{}
			}
			if forwarding.EvidencePath != "" {
				s.paths[forwarding.EvidencePath] = struct{}{}
			}
			if forwarding.Process.Identity.PID > 0 {
				s.processes[forwarding.Process.Identity] = struct{}{}
			}
		}
		for _, service := range snapshot.Services {
			for _, identity := range []domain.ProcessIdentity{service.Helper.Identity, service.Child.Identity} {
				if identity.PID > 0 {
					s.processes[identity] = struct{}{}
				}
			}
		}
		if snapshot.Supervisor.Identity.PID > 0 {
			s.processes[snapshot.Supervisor.Identity] = struct{}{}
		}
	}
	return nil
}

func (s *lifecycleScenario) Endpoints(t *testing.T, controller, sessionID string) lifecycleEndpoints {
	t.Helper()
	path := filepath.Join(controller, "data", "camp", "sessions", sessionID, "snapshot.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read durable session evidence %q: %v", path, err)
	}
	var snapshot struct {
		Recovery struct {
			Forwarding []struct {
				Name          string `json:"name"`
				LocalEndpoint string `json:"localEndpoint"`
			} `json:"forwarding"`
		} `json:"recovery"`
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatalf("decode durable session evidence %q: %v", path, err)
	}
	var endpoints lifecycleEndpoints
	for _, forwarding := range snapshot.Recovery.Forwarding {
		s.listeners[forwarding.LocalEndpoint] = struct{}{}
		switch forwarding.Name {
		case "registry":
			endpoints.Registry = forwarding.LocalEndpoint
		case "fileserver":
			endpoints.Fileserver = forwarding.LocalEndpoint
		}
	}
	if endpoints.Registry == "" || endpoints.Fileserver == "" {
		t.Fatalf("durable session evidence has incomplete endpoints: %#v", snapshot.Recovery.Forwarding)
	}
	return endpoints
}

func (s *lifecycleScenario) Cleanup(t *testing.T, binary string) {
	t.Helper()
	if err := s.cleanup(binary); err != nil {
		t.Errorf("clean scenario resource ledger: %v", err)
	}
}

func (s *lifecycleScenario) cleanup(binary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var result error
	for _, controller := range s.controllers {
		if environment := s.environments[controller]; len(environment) != 0 {
			_, _ = runLifecycleCommand(ctx, environment, binary, "--json", "close")
		}
		if err := s.trackController(controller); err != nil {
			result = errors.Join(result, fmt.Errorf("recover scenario resource ledger: %w", err))
		}
	}
	for path := range s.removePaths {
		if err := os.RemoveAll(path); err != nil {
			result = errors.Join(result, fmt.Errorf("remove exact scenario path %q: %w", path, err))
		}
	}
	if err := s.workspaces.DeleteAll(func(id string) error {
		output, err := runDevPodCommand(ctx, s.devPod, "delete", "--ignore-not-found", id)
		if err != nil {
			return fmt.Errorf("%w: %s", err, output)
		}
		return nil
	}); err != nil {
		result = errors.Join(result, fmt.Errorf("clean exact scenario DevPod workspaces: %w", err))
	}
	if s.unrelatedID != "" {
		workspaces, err := listDevPodWorkspaces(ctx, s.devPod)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("list unrelated DevPod workspace: %w", err))
		} else {
			found := false
			for _, id := range workspaces {
				found = found || id == s.unrelatedID
			}
			if !found {
				result = errors.Join(result, fmt.Errorf("unrelated DevPod workspace %q was removed by scenario cleanup", s.unrelatedID))
			}
		}
		output, err := runDevPodCommand(ctx, s.devPod, "delete", "--ignore-not-found", s.unrelatedID)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("delete exact unrelated DevPod workspace %q: %w: %s", s.unrelatedID, err, output))
		}
	}
	return errors.Join(result, s.verifyOwnedResources(ctx))
}

func (s *lifecycleScenario) AssertEndpointsClosed(t *testing.T) {
	t.Helper()
	for endpoint := range s.listeners {
		assertEndpointClosed(t, endpoint)
	}
}

func (s *lifecycleScenario) verifyOwnedResources(ctx context.Context) error {
	var result error
	workspaces, err := listDevPodWorkspaces(ctx, s.devPod)
	if err != nil {
		result = errors.Join(result, err)
	} else {
		present := make(map[string]struct{}, len(workspaces))
		for _, id := range workspaces {
			present[id] = struct{}{}
		}
		for _, id := range s.workspaces.ids {
			if _, ok := present[id]; ok {
				result = errors.Join(result, fmt.Errorf("owned DevPod workspace %q remains", id))
			}
		}
	}
	processes, err := supervisor.NewProcessManager()
	if err != nil {
		result = errors.Join(result, err)
	} else {
		for identity := range s.processes {
			status, inspectErr := processes.Inspect(ctx, identity)
			if inspectErr != nil && !errors.Is(inspectErr, supervisor.ErrProcessIdentity) {
				result = errors.Join(result, fmt.Errorf("inspect owned process %d: %w", identity.PID, inspectErr))
				continue
			}
			if inspectErr == nil && status.Running {
				result = errors.Join(result, fmt.Errorf("owned process %d remains", identity.PID))
			}
		}
	}
	for path := range s.paths {
		if _, statErr := os.Lstat(path); statErr == nil {
			result = errors.Join(result, fmt.Errorf("owned path %q remains", path))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("inspect owned path %q: %w", path, statErr))
		}
	}
	for endpoint := range s.listeners {
		connection, dialErr := net.DialTimeout("tcp", endpoint, time.Second)
		if dialErr == nil {
			_ = connection.Close()
			result = errors.Join(result, fmt.Errorf("owned listener remains on %s", endpoint))
		}
	}
	return result
}

type devPodTestIsolation struct {
	home    string
	config  string
	context string
}

func newDevPodTestIsolation(root string) devPodTestIsolation {
	home := filepath.Join(root, "devpod-home")
	contextDigest := sha256.Sum256([]byte(filepath.Clean(root)))
	return devPodTestIsolation{
		home:    home,
		config:  filepath.Join(home, "config.yaml"),
		context: fmt.Sprintf("camp-test-%x", contextDigest[:6]),
	}
}

func (d devPodTestIsolation) Environment() []string {
	return []string{
		"DEVPOD_HOME=" + d.home,
		"DEVPOD_CONFIG=" + d.config,
		"DEVPOD_DISABLE_TELEMETRY=true",
		"SSH_CONFIG_PATH=" + filepath.Join(d.home, "ssh", "config"),
		"CAMP_DEVPOD_CONTEXT=" + d.context,
	}
}

func (d devPodTestIsolation) CommandArgs(command string, argv ...string) []string {
	result := []string{command, "--context", d.context}
	return append(result, argv...)
}

func newCreatedWorkspaceTracker() *createdWorkspaceTracker {
	return &createdWorkspaceTracker{seen: map[string]struct{}{}}
}

func (t *createdWorkspaceTracker) Track(id string) {
	if id == "" {
		return
	}
	if _, ok := t.seen[id]; ok {
		return
	}
	t.seen[id] = struct{}{}
	t.ids = append(t.ids, id)
}

func (t *createdWorkspaceTracker) TrackController(controller string) error {
	matches, err := filepath.Glob(filepath.Join(controller, "data", "camp", "sessions", "*", "snapshot.json"))
	if err != nil {
		return fmt.Errorf("find test-owned controller snapshots: %w", err)
	}
	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read test-owned controller snapshot %q: %w", path, err)
		}
		var snapshot struct {
			Workspace struct {
				ID string `json:"id"`
			} `json:"workspace"`
		}
		if err := json.Unmarshal(body, &snapshot); err != nil {
			return fmt.Errorf("decode test-owned controller snapshot %q: %w", path, err)
		}
		t.Track(snapshot.Workspace.ID)
	}
	return nil
}

func (t *createdWorkspaceTracker) DeleteAll(deleteWorkspace func(string) error) error {
	var result error
	for index := len(t.ids) - 1; index >= 0; index-- {
		if err := deleteWorkspace(t.ids[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("delete exact DevPod workspace %q: %w", t.ids[index], err))
		}
	}
	return result
}

func parseWorkspaceImageDigest(output []byte) (string, error) {
	value := strings.TrimSpace(string(output))
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return "", fmt.Errorf("workspace image digest is not one exact sha256: %q", value)
	}
	for _, character := range value[len("sha256:"):] {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		if !isDigit && !isLowerHex {
			return "", fmt.Errorf("workspace image digest is not lowercase hexadecimal: %q", value)
		}
	}
	return value, nil
}

func TestCreatedWorkspaceCleanupDeletesOnlyTrackedExactIDs(t *testing.T) {
	t.Parallel()

	tracker := newCreatedWorkspaceTracker()
	tracker.Track("camp-session-a")
	tracker.Track("camp-session-b")
	tracker.Track("camp-session-a")
	tracker.Track("")

	var deleted []string
	tracker.DeleteAll(func(id string) error {
		deleted = append(deleted, id)
		return nil
	})
	if want := []string{"camp-session-b", "camp-session-a"}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted workspace IDs = %#v, want %#v", deleted, want)
	}
}

func TestCreatedWorkspaceCleanupRecoversExactIDFromOwnedController(t *testing.T) {
	controller := t.TempDir()
	session := filepath.Join(controller, "data", "camp", "sessions", "session-test")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(session, "snapshot.json"),
		[]byte(`{"workspace":{"id":"camp-owned-test"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	tracker := newCreatedWorkspaceTracker()
	if err := tracker.TrackController(controller); err != nil {
		t.Fatal(err)
	}
	var deleted []string
	if err := tracker.DeleteAll(func(id string) error {
		deleted = append(deleted, id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"camp-owned-test"}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted workspace IDs = %#v, want %#v", deleted, want)
	}
}

func TestDevPodTestIsolationOwnsHomeConfigAndContext(t *testing.T) {
	root := t.TempDir()
	isolation := newDevPodTestIsolation(root)

	gotEnvironment := map[string]string{}
	for _, entry := range isolation.Environment() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			gotEnvironment[name] = value
		}
	}
	wantHome := filepath.Join(root, "devpod-home")
	if gotEnvironment["DEVPOD_HOME"] != wantHome {
		t.Fatalf("DEVPOD_HOME = %q, want %q", gotEnvironment["DEVPOD_HOME"], wantHome)
	}
	if want := filepath.Join(wantHome, "config.yaml"); gotEnvironment["DEVPOD_CONFIG"] != want {
		t.Fatalf("DEVPOD_CONFIG = %q, want %q", gotEnvironment["DEVPOD_CONFIG"], want)
	}
	if want := filepath.Join(wantHome, "ssh", "config"); gotEnvironment["SSH_CONFIG_PATH"] != want {
		t.Fatalf("SSH_CONFIG_PATH = %q, want %q", gotEnvironment["SSH_CONFIG_PATH"], want)
	}
	if gotEnvironment["CAMP_DEVPOD_CONTEXT"] != isolation.context {
		t.Fatalf("CAMP_DEVPOD_CONTEXT = %q, want %q", gotEnvironment["CAMP_DEVPOD_CONTEXT"], isolation.context)
	}
	if gotEnvironment["DEVPOD_DISABLE_TELEMETRY"] != "true" {
		t.Fatalf("DEVPOD_DISABLE_TELEMETRY = %q, want true", gotEnvironment["DEVPOD_DISABLE_TELEMETRY"])
	}

	if got, want := isolation.CommandArgs("list", "--output", "json"), []string{"list", "--context", isolation.context, "--output", "json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DevPod command argv = %#v, want %#v", got, want)
	}
}

func TestDevPodTestIsolationUsesDistinctValidContexts(t *testing.T) {
	first := newDevPodTestIsolation("/tmp/camp-isolation-a")
	second := newDevPodTestIsolation("/tmp/camp-isolation-b")
	if first.context == second.context {
		t.Fatalf("distinct scenarios share DevPod context %q", first.context)
	}
	for _, contextName := range []string{first.context, second.context} {
		if !strings.HasPrefix(contextName, "camp-test-") || len(contextName) > 48 {
			t.Fatalf("invalid private DevPod context %q", contextName)
		}
		for _, character := range contextName {
			isLower := character >= 'a' && character <= 'z'
			isDigit := character >= '0' && character <= '9'
			if !isLower && !isDigit && character != '-' {
				t.Fatalf("private DevPod context %q contains invalid character %q", contextName, character)
			}
		}
	}
}

func TestBootstrapDevPodDockerProviderUsesPrivateContextBeforeScenarioActivity(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	devPod := filepath.Join(bin, "devpod")
	if err := os.WriteFile(devPod, []byte(`#!/bin/sh
set -eu
printf 'bootstrap\n' >> "$CAMP_TEST_ORDER_LOG"
printf 'argv'
for argument in "$@"; do
	printf ' <%s>' "$argument"
done
printf '\nDEVPOD_HOME=<%s>\nDEVPOD_CONFIG=<%s>\nSSH_CONFIG_PATH=<%s>\nCAMP_DEVPOD_CONTEXT=<%s>\n' \
	"$DEVPOD_HOME" "$DEVPOD_CONFIG" "$SSH_CONFIG_PATH" "$CAMP_DEVPOD_CONTEXT"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	orderLog := filepath.Join(root, "order.log")
	t.Setenv("CAMP_TEST_ORDER_LOG", orderLog)

	isolation := newDevPodTestIsolation(root)
	output, err := bootstrapDevPodDockerProvider(context.Background(), isolation)
	if err != nil {
		t.Fatalf("bootstrap private Docker provider: %v\n%s", err, output)
	}
	if body, err := os.ReadFile(orderLog); err != nil || string(body) != "bootstrap\n" {
		t.Fatalf("order before scenario activity = %q, %v", body, err)
	}
	if err := os.WriteFile(orderLog, []byte("bootstrap\nscenario\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	wantOutput := fmt.Sprintf(
		"argv <provider> <add> <docker> <--context> <%s> <--use> <--silent>\nDEVPOD_HOME=<%s>\nDEVPOD_CONFIG=<%s>\nSSH_CONFIG_PATH=<%s>\nCAMP_DEVPOD_CONTEXT=<%s>\n",
		isolation.context,
		filepath.Join(root, "devpod-home"),
		filepath.Join(root, "devpod-home", "config.yaml"),
		filepath.Join(root, "devpod-home", "ssh", "config"),
		isolation.context,
	)
	if string(output) != wantOutput {
		t.Fatalf("bootstrap output = %q, want %q", output, wantOutput)
	}
}

func TestLifecycleScenarioReadsEndpointsOnlyFromDurableSessionEvidence(t *testing.T) {
	root := t.TempDir()
	controller := filepath.Join(root, "controller")
	session := filepath.Join(controller, "data", "camp", "sessions", "session-test")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, "snapshot.json"), []byte(`{"recovery":{"forwarding":[{"name":"registry","localEndpoint":"127.0.0.1:41001"},{"name":"fileserver","localEndpoint":"127.0.0.1:41002"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scenario := newLifecycleScenario(t, root, newDevPodTestIsolation(root), controller)
	if got := scenario.Endpoints(t, controller, "session-test"); got != (lifecycleEndpoints{Registry: "127.0.0.1:41001", Fileserver: "127.0.0.1:41002"}) {
		t.Fatalf("endpoints = %#v", got)
	}
}

func TestLifecycleScenarioCleanupConsumesInterruptedLedger(t *testing.T) {
	root := t.TempDir()
	controller := filepath.Join(root, "controller")
	session := filepath.Join(controller, "data", "camp", "sessions", "session-test")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := fmt.Sprintf(`{"workspace":{"id":"camp-owned"},"materialization":{"canonicalPath":%q,"cleanupPermitted":true},"recovery":{"forwarding":[{"name":"registry","localEndpoint":%q,"evidencePath":%q,"process":{"identity":{"pid":2147483647,"bootId":"missing","startTicks":1}}}]}}`, filepath.Join(root, "absent-materialization"), endpoint, filepath.Join(root, "absent-forward.json"))
	if err := os.WriteFile(filepath.Join(session, "snapshot.json"), []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "commands.log")
	t.Setenv("CAMP_TEST_CLEANUP_LOG", logPath)
	writeTestExecutable(t, filepath.Join(bin, "devpod"), `#!/bin/sh
printf 'devpod %s\n' "$*" >> "$CAMP_TEST_CLEANUP_LOG"
if test "$1" = list; then printf '[]\n'; fi
`)
	candidate := filepath.Join(bin, "camp")
	writeTestExecutable(t, candidate, `#!/bin/sh
printf 'camp %s\n' "$*" >> "$CAMP_TEST_CLEANUP_LOG"
exit 1
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	scenario := newLifecycleScenario(t, root, newDevPodTestIsolation(root), controller)
	scenario.RegisterController(controller, lifecycleEnvironment(controller, filepath.Join(root, "backend"), scenario.devPod))
	if err := scenario.cleanup(candidate); err != nil {
		t.Fatalf("cleanup interrupted ledger: %v", err)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"camp --json close", "devpod delete --context", "camp-owned", "devpod list --context"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("cleanup log omitted %q: %s", required, body)
		}
	}
}

func TestLifecycleScenarioCleanupContinuesVerificationAfterDeleteFailure(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "commands.log")
	t.Setenv("CAMP_TEST_CLEANUP_LOG", logPath)
	writeTestExecutable(t, filepath.Join(bin, "devpod"), `#!/bin/sh
printf 'devpod %s\n' "$*" >> "$CAMP_TEST_CLEANUP_LOG"
if test "$1" = delete; then exit 1; fi
if test "$1" = list; then printf '[]\n'; fi
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	scenario := newLifecycleScenario(t, root, newDevPodTestIsolation(root))
	scenario.workspaces.Track("camp-owned")
	if err := scenario.cleanup(filepath.Join(bin, "unused")); err == nil {
		t.Fatal("cleanup succeeded after exact workspace delete failure")
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "devpod list --context") {
		t.Fatalf("verification did not run after cleanup failure: %s", body)
	}
}

func writeTestExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleEnvironmentIncludesDevPodIsolation(t *testing.T) {
	root := t.TempDir()
	controller := filepath.Join(root, "controller")
	isolation := newDevPodTestIsolation(root)
	_ = newLifecycleScenario(t, root, isolation, controller)
	fileEnvironment := lifecycleEnvironment(
		controller,
		filepath.Join(root, "backend"),
		isolation,
	)
	minioEnvironment := minioLifecycleEnvironment(
		controller,
		"http://127.0.0.1:9000",
		"access",
		"secret",
		isolation,
	)

	for name, environment := range map[string][]string{
		"file":  fileEnvironment,
		"minio": minioEnvironment,
	} {
		got := map[string]string{}
		for _, entry := range environment {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				got[key] = value
			}
		}
		if got["DEVPOD_HOME"] != filepath.Join(root, "devpod-home") {
			t.Fatalf("%s lifecycle DEVPOD_HOME = %q", name, got["DEVPOD_HOME"])
		}
		if got["DEVPOD_CONFIG"] != filepath.Join(root, "devpod-home", "config.yaml") {
			t.Fatalf("%s lifecycle DEVPOD_CONFIG = %q", name, got["DEVPOD_CONFIG"])
		}
		if got["CAMP_DEVPOD_CONTEXT"] != isolation.context {
			t.Fatalf("%s lifecycle CAMP_DEVPOD_CONTEXT = %q", name, got["CAMP_DEVPOD_CONTEXT"])
		}
		if got["CAMP_DEVPOD_PROVIDER"] != "docker" {
			t.Fatalf("%s lifecycle CAMP_DEVPOD_PROVIDER = %q, want docker", name, got["CAMP_DEVPOD_PROVIDER"])
		}
		if got["XDG_RUNTIME_DIR"] != scenarioRuntimeDirectory(controller) {
			t.Fatalf("%s lifecycle XDG_RUNTIME_DIR = %q, want %q", name, got["XDG_RUNTIME_DIR"], scenarioRuntimeDirectory(controller))
		}
	}
	if info, err := os.Stat(scenarioRuntimeDirectory(controller)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private XDG runtime directory mode = %v, %v; want 0700", info, err)
	}
}

func TestParseWorkspaceImageDigestRequiresOneExactSHA256(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("a", 64)
	got, err := parseWorkspaceImageDigest([]byte(digest + "\n"))
	if err != nil || got != digest {
		t.Fatalf("parseWorkspaceImageDigest() = %q, %v", got, err)
	}
	for _, invalid := range [][]byte{
		[]byte(""),
		[]byte("sha256:short\n"),
		[]byte("devpod noise\n" + digest + "\n"),
		[]byte(digest + "\n" + digest + "\n"),
	} {
		if got, err := parseWorkspaceImageDigest(invalid); err == nil {
			t.Fatalf("parseWorkspaceImageDigest(%q) = %q, want error", invalid, got)
		}
	}
}

func TestNamedImageReopenProofIsDigestQualifiedValidShell(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("b", 64)
	command := namedImageReopenProofCommand(digest)
	for _, required := range []string{
		`$CAMP_REGISTRY/camp/acceptance:named`,
		`image_id=$("$engine" image inspect`,
		`image rm -f`,
		`$CAMP_REGISTRY/camp/acceptance@$expected_digest`,
		`pull "$digest_reference"`,
		`json .RepoDigests`,
		`run --rm "$digest_reference"`,
	} {
		if !strings.Contains(command, required) {
			t.Fatalf("named image proof omitted %q: %s", required, command)
		}
	}
	if strings.Contains(command, "if image_id=") {
		t.Fatalf("named image proof may skip cache eviction when the restored tag is missing: %s", command)
	}
	if err := exec.Command("sh", "-n", "-c", command).Run(); err != nil {
		t.Fatalf("named image proof is invalid POSIX shell: %v\n%s", err, command)
	}
}
