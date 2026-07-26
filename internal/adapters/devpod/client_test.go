package devpod

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type recordingRunner struct {
	commands []ports.Command
	result   ports.Result
	err      error
}

func (r *recordingRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.commands = append(r.commands, command)
	return r.result, r.err
}

type startedRecordingRunner struct {
	commands []ports.Command
	runCalls int
	result   ports.Result
	err      error
}

func (r *startedRecordingRunner) Run(_ context.Context, _ ports.Command) (ports.Result, error) {
	r.runCalls++
	return ports.Result{}, errors.New("Run called instead of RunStarted")
}

func (r *startedRecordingRunner) RunStarted(_ context.Context, command ports.Command, started func() error) (ports.Result, error) {
	r.commands = append(r.commands, command)
	if err := started(); err != nil {
		return ports.Result{}, err
	}
	return r.result, r.err
}

func TestSSHWithStartPreservesExactArgvThroughStartedRunner(t *testing.T) {
	t.Parallel()
	runner := &startedRecordingRunner{result: ports.Result{ExitCode: 23}}
	client := NewClient("/opt/devpod", runner)
	callbackCalls := 0
	got, err := client.SSHWithStart(context.Background(), SSHOptions{
		WorkspaceID:     "camp-second-brain",
		Context:         "default",
		Workdir:         "/workspaces/Memory D",
		User:            "vscode",
		ForwardPorts:    []string{"127.0.0.1:3000:3000"},
		ReverseForwards: []string{"127.0.0.1:5000:127.0.0.1:5000"},
		SetEnv:          []string{"CAMP_CAPSULE=second-brain"},
		StartServices:   true,
		ForwardedArgv:   []string{"--log-output", "plain"},
	}, func() error {
		callbackCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("SSHWithStart() error = %v", err)
	}
	if !reflect.DeepEqual(got, runner.result) {
		t.Fatalf("result = %#v, want %#v", got, runner.result)
	}
	if callbackCalls != 1 {
		t.Fatalf("started callback calls = %d, want 1", callbackCalls)
	}
	if runner.runCalls != 0 {
		t.Fatalf("Run calls = %d, want 0", runner.runCalls)
	}
	want := ports.Command{Executable: "/opt/devpod", Argv: []string{
		"ssh", "--context", "default", "--workdir", "/workspaces/Memory D", "--user", "vscode",
		"--forward-ports", "127.0.0.1:3000:3000",
		"--reverse-forward-ports", "127.0.0.1:5000:127.0.0.1:5000",
		"--set-env", "CAMP_CAPSULE=second-brain", "--log-output", "plain",
		"--start-services=true", "camp-second-brain",
	}}
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0], want) {
		t.Fatalf("commands = %#v, want [%#v]", runner.commands, want)
	}
}

func TestSSHWithStartRejectsRunnerWithoutStartObservationWithoutRunning(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	callbackCalls := 0
	_, err := NewClient("/opt/devpod", runner).SSHWithStart(context.Background(), SSHOptions{
		WorkspaceID: "camp-second-brain",
	}, func() error {
		callbackCalls++
		return nil
	})
	if !errors.Is(err, ErrStartObservationRequired) {
		t.Fatalf("SSHWithStart() error = %v, want ErrStartObservationRequired", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("runner commands = %#v, want none", runner.commands)
	}
	if callbackCalls != 0 {
		t.Fatalf("started callback calls = %d, want 0", callbackCalls)
	}
}

func TestExactDevPodArgv(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *Client) error
		want []string
	}{
		{
			name: "terminal up disables IDE opening",
			run: func(ctx context.Context, client *Client) error {
				_, err := client.Up(ctx, UpOptions{
					WorkspacePath: "/tmp/capsule root", WorkspaceID: "camp-second-brain", Provider: "docker",
					InitEnv: []string{"CAMP_CAPSULE=second-brain", "CAMP_CHECKPOINT=41"},
				})
				return err
			},
			want: []string{"up", "--ide", "none", "--open-ide=false", "--id", "camp-second-brain", "--provider", "docker", "--init-env", "CAMP_CAPSULE=second-brain", "--init-env", "CAMP_CHECKPOINT=41", "/tmp/capsule root"},
		},
		{
			name: "terminal SSH explicitly disables services by default",
			run: func(ctx context.Context, client *Client) error {
				_, err := client.SSH(ctx, SSHOptions{WorkspaceID: "camp-second-brain", Workdir: "/workspaces/root; echo unsafe", User: "dev"})
				return err
			},
			want: []string{"ssh", "--workdir", "/workspaces/root; echo unsafe", "--user", "dev", "--start-services=false", "camp-second-brain"},
		},
		{
			name: "reverse forwards remain repeated argv",
			run: func(ctx context.Context, client *Client) error {
				_, err := client.SSH(ctx, SSHOptions{WorkspaceID: "camp-second-brain", ReverseForwards: []string{"127.0.0.1:5000:127.0.0.1:5000", "127.0.0.1:8080:127.0.0.1:8080"}, StartServices: true})
				return err
			},
			want: []string{"ssh", "--reverse-forward-ports", "127.0.0.1:5000:127.0.0.1:5000", "--reverse-forward-ports", "127.0.0.1:8080:127.0.0.1:8080", "--start-services=true", "camp-second-brain"},
		},
		{
			name: "workspace folder resolution uses a constant command",
			run: func(ctx context.Context, client *Client) error {
				_, err := client.ResolveWorkspaceFolder(ctx, "camp-second-brain")
				return err
			},
			want: []string{"ssh", "--start-services=false", "--command", "pwd", "camp-second-brain"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{result: ports.Result{Stdout: []byte("/workspaces/root\n")}}
			client := NewClient("/opt/devpod", runner)
			if err := tt.run(context.Background(), client); err != nil {
				t.Fatal(err)
			}
			if len(runner.commands) != 1 {
				t.Fatalf("commands = %d, want 1", len(runner.commands))
			}
			want := ports.Command{Executable: "/opt/devpod", Argv: tt.want}
			if got := runner.commands[0]; !reflect.DeepEqual(got, want) {
				t.Fatalf("command = %#v, want %#v", got, want)
			}
		})
	}
}

func TestTask3ScopedWorkspaceEnvironmentAndArgvExecution(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{result: ports.Result{Stdout: []byte("ok")}}
	client := NewClient("/opt/devpod", runner)
	_, err := client.Up(context.Background(), UpOptions{
		WorkspacePath: "/tmp/root", WorkspaceID: "camp-abcd", Context: "default", Provider: "docker",
		DevcontainerPath: "/tmp/root/.camp/runtime/devcontainer.json",
		CampEnvironment: &CampEnvironment{
			Registry: "127.0.0.1:5000", Fileserver: "127.0.0.1:8080", Capsule: "second-brain", Checkpoint: "42",
		},
	})
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	wantUp := []string{
		"up", "--ide", "none", "--open-ide=false", "--context", "default", "--id", "camp-abcd", "--provider", "docker",
		"--devcontainer-path", "/tmp/root/.camp/runtime/devcontainer.json",
		"--workspace-env", "CAMP_REGISTRY=127.0.0.1:5000", "--workspace-env", "CAMP_FILESERVER=127.0.0.1:8080",
		"--workspace-env", "CAMP_CAPSULE=second-brain", "--workspace-env", "CAMP_CHECKPOINT=42", "/tmp/root",
	}
	if !reflect.DeepEqual(runner.commands[0].Argv, wantUp) {
		t.Fatalf("Up argv = %#v, want %#v", runner.commands[0].Argv, wantUp)
	}

	_, err = client.Execute(context.Background(), WorkspaceCommand{
		Context: "default", WorkspaceID: "camp-abcd", Workdir: "/workspaces/root/Memory D",
		Argv: []string{"printf", "%s", "$(touch /tmp/nope); quote' here"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	wantExec := []string{"ssh", "--context", "default", "--workdir", "/workspaces/root/Memory D", "--start-services=false", "--command", `'printf' '%s' '$(touch /tmp/nope); quote'"'"' here'`, "camp-abcd"}
	if !reflect.DeepEqual(runner.commands[1].Argv, wantExec) {
		t.Fatalf("Execute argv = %#v, want %#v", runner.commands[1].Argv, wantExec)
	}

	command, err := client.SSHCommand(SSHOptions{Context: "default", WorkspaceID: "camp-abcd", ReverseForwards: []string{"127.0.0.1:5000:127.0.0.1:5000"}, StartServices: true})
	if err != nil {
		t.Fatalf("SSHCommand() error = %v", err)
	}
	if command.Executable != "/opt/devpod" || !reflect.DeepEqual(command.Argv, []string{"ssh", "--context", "default", "--reverse-forward-ports", "127.0.0.1:5000:127.0.0.1:5000", "--start-services=true", "camp-abcd"}) {
		t.Fatalf("SSHCommand = %#v", command)
	}
}

func TestEnsureProviderConfiguresAbsentDockerThenVerifiesExactIdentity(t *testing.T) {
	t.Parallel()
	runner := &providerSequenceRunner{results: []ports.Result{
		{Stdout: []byte(`{}`)},
		{},
		{Stdout: []byte(`{"docker":{"default":true}}`)},
	}}
	if err := NewClient("/opt/devpod", runner).EnsureProvider(context.Background(), "default", "docker"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"provider", "list", "--context", "default", "--output", "json"},
		{"provider", "add", "docker", "--context", "default", "--use"},
		{"provider", "list", "--context", "default", "--output", "json"},
	}
	if !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("provider argv = %#v, want %#v", runner.argv, want)
	}
}

func TestAddProviderUsesTypedDockerContextAndRepeatedOptionsThenVerifies(t *testing.T) {
	t.Parallel()
	runner := &providerSequenceRunner{results: []ports.Result{
		{Stdout: []byte(`{}`)},
		{},
		{Stdout: []byte(`{"docker":{"default":true}}`)},
	}}
	request := ProviderRequest{Context: "work", Name: "docker", Options: []string{"DOCKER_PATH=/run/docker.sock", "HELPER=false"}}
	if err := NewClient("/opt/devpod", runner).AddProvider(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"provider", "list", "--context", "work", "--output", "json"},
		{"provider", "add", "docker", "--context", "work", "--use", "--option", "DOCKER_PATH=/run/docker.sock", "--option", "HELPER=false"},
		{"provider", "list", "--context", "work", "--output", "json"},
	}
	if !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("provider argv = %#v, want %#v", runner.argv, want)
	}
}

func TestUseProviderUsesTypedContextAndRepeatedOptionsThenVerifies(t *testing.T) {
	t.Parallel()
	runner := &providerSequenceRunner{results: []ports.Result{
		{Stdout: []byte(`{"docker":{"default":false}}`)},
		{},
		{Stdout: []byte(`{"docker":{"default":true}}`)},
	}}
	request := ProviderRequest{Context: "work", Name: "docker", Options: []string{"DOCKER_PATH=/run/docker.sock"}}
	if err := NewClient("/opt/devpod", runner).UseProvider(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"provider", "list", "--context", "work", "--output", "json"},
		{"provider", "use", "docker", "--context", "work", "--reconfigure", "--option", "DOCKER_PATH=/run/docker.sock"},
		{"provider", "list", "--context", "work", "--output", "json"},
	}
	if !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("provider argv = %#v, want %#v", runner.argv, want)
	}
}

func TestEnsureProviderFailsClosedWhenConfiguredIdentityDoesNotMatch(t *testing.T) {
	t.Parallel()
	runner := &providerSequenceRunner{results: []ports.Result{{Stdout: []byte(`{}`)}, {}, {Stdout: []byte(`{"other":{"default":true}}`)}}}
	if err := NewClient("/opt/devpod", runner).EnsureProvider(context.Background(), "default", "docker"); err == nil {
		t.Fatal("EnsureProvider() error = nil")
	}
}

func TestEnsureProviderAcceptsExistingInitializedNamedProviderWithoutMutation(t *testing.T) {
	t.Parallel()
	runner := &providerSequenceRunner{results: []ports.Result{{Stdout: []byte(`{"room-of-requirement":{"state":{"initialized":true}}}`)}}}
	if err := NewClient("/opt/devpod", runner).EnsureProvider(context.Background(), "default", "room-of-requirement"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"provider", "list", "--context", "default", "--output", "json"}}
	if !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("provider argv = %#v, want %#v", runner.argv, want)
	}
}

func TestProbeProviderVerifiesConfiguredIdentityWithoutMutation(t *testing.T) {
	t.Parallel()
	runner := &providerSequenceRunner{results: []ports.Result{{Stdout: []byte(`{"room-of-requirement":{"state":{"initialized":true}}}`)}}}
	if err := NewClient("/opt/devpod", runner).ProbeProvider(context.Background(), "default", "room-of-requirement"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"provider", "list", "--context", "default", "--output", "json"}}
	if !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("provider argv = %#v, want read-only %#v", runner.argv, want)
	}
}

func TestProbeProviderNeverAddsMissingDocker(t *testing.T) {
	t.Parallel()
	runner := &providerSequenceRunner{results: []ports.Result{{Stdout: []byte(`{}`)}}}
	if err := NewClient("/opt/devpod", runner).ProbeProvider(context.Background(), "default", "docker"); err == nil {
		t.Fatal("ProbeProvider accepted missing docker")
	}
	if len(runner.argv) != 1 || runner.argv[0][0] != "provider" || runner.argv[0][1] != "list" {
		t.Fatalf("provider argv = %#v, want one list", runner.argv)
	}
}

func TestListProviderNamesUsesReadOnlyContextScopedOutput(t *testing.T) {
	t.Parallel()

	runner := &providerSequenceRunner{results: []ports.Result{{Stdout: []byte(`{"ssh":{"state":{"initialized":true}},"docker":{"default":true}}`)}}}
	got, err := NewClient("/opt/devpod", runner).ListProviderNames(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"docker", "ssh"}) {
		t.Fatalf("provider names = %#v", got)
	}
	want := [][]string{{"provider", "list", "--context", "work", "--output", "json"}}
	if !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("provider argv = %#v, want %#v", runner.argv, want)
	}
}

type providerSequenceRunner struct {
	results []ports.Result
	argv    [][]string
}

func (r *providerSequenceRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.argv = append(r.argv, append([]string(nil), command.Argv...))
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func TestTask3ReconciliationCallsAreContextScoped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*Client) error
		want []string
	}{
		{"status", func(c *Client) error {
			_, err := c.StatusInContext(context.Background(), "default", "camp")
			return err
		}, []string{"status", "--context", "default", "--output", "json", "camp"}},
		{"list", func(c *Client) error { _, err := c.ListInContext(context.Background(), "default"); return err }, []string{"list", "--context", "default", "--output", "json", "--skip-pro"}},
		{"folder", func(c *Client) error {
			_, err := c.ResolveWorkspaceFolderInContext(context.Background(), "default", "camp")
			return err
		}, []string{"ssh", "--context", "default", "--start-services=false", "--command", "pwd", "camp"}},
		{"stop", func(c *Client) error {
			_, err := c.StopInContext(context.Background(), "default", "camp", true)
			return err
		}, []string{"stop", "--context", "default", "camp"}},
		{"delete", func(c *Client) error {
			_, err := c.DeleteInContext(context.Background(), "default", "camp", true)
			return err
		}, []string{"delete", "--context", "default", "--ignore-not-found", "camp"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{result: ports.Result{Stdout: []byte(`{"id":"camp","state":"Running"}`)}}
			if test.name == "list" {
				runner.result.Stdout = []byte(`[]`)
			}
			if test.name == "folder" {
				runner.result.Stdout = []byte("/workspaces/root\n")
			}
			if err := test.run(NewClient("/opt/devpod", runner)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runner.commands[0].Argv, test.want) {
				t.Fatalf("argv = %#v, want %#v", runner.commands[0].Argv, test.want)
			}
		})
	}
}

func TestStatusParsesEveryPinnedJSONState(t *testing.T) {
	for _, state := range []WorkspaceState{StateRunning, StateBusy, StateStopped, StateNotFound} {
		t.Run(string(state), func(t *testing.T) {
			runner := &recordingRunner{result: ports.Result{Stdout: []byte(`{"id":"camp","context":"default","provider":"docker","state":"` + string(state) + `"}`)}}
			client := NewClient("devpod", runner)
			got, err := client.Status(context.Background(), "camp")
			if err != nil {
				t.Fatal(err)
			}
			want := WorkspaceStatus{ID: "camp", Context: "default", Provider: "docker", State: state}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("status = %#v, want %#v", got, want)
			}
			wantCommand := ports.Command{Executable: "devpod", Argv: []string{"status", "--output", "json", "camp"}}
			if !reflect.DeepEqual(runner.commands[0], wantCommand) {
				t.Fatalf("command = %#v, want %#v", runner.commands[0], wantCommand)
			}
		})
	}
}

func TestStatusRejectsUnknownJSONState(t *testing.T) {
	runner := &recordingRunner{result: ports.Result{Stdout: []byte(`{"id":"camp","state":"Paused"}`)}}
	client := NewClient("devpod", runner)
	if _, err := client.Status(context.Background(), "camp"); !errors.Is(err, ErrUnknownWorkspaceState) {
		t.Fatalf("error = %v, want ErrUnknownWorkspaceState", err)
	}
}

func TestListParsesPinnedJSONShape(t *testing.T) {
	runner := &recordingRunner{result: ports.Result{Stdout: []byte(`[
  {
    "id": "camp",
    "uid": "uid-1",
    "picture": "https://example.test/camp.png",
    "provider": {
      "name": "docker",
      "options": {
        "DOCKER_PATH": {
          "value": "/var/run/docker.sock",
          "userProvided": true,
          "filled": "2026-07-14T12:00:00Z",
          "children": ["DOCKER_HOST"]
        }
      }
    },
    "machine": {
      "machineId": "bluefin",
      "autoDelete": true
    },
    "ide": {
      "name": "none",
      "options": {
        "DISABLE_TELEMETRY": {
          "value": "true",
          "userProvided": true,
          "filled": "2026-07-14T12:01:00Z",
          "children": ["TELEMETRY_LEVEL"]
        }
      }
    },
    "source": {
      "gitRepository": "https://github.com/joshyorko/camp.git",
      "gitBranch": "main",
      "gitCommit": "86b6f9f5d6713fecdeff5dd240e775a8c7e8d44e",
      "gitPRReference": "refs/pull/1/head",
      "gitSubDir": "capsule",
      "localFolder": "/tmp/root",
      "image": "ghcr.io/example/camp:dev",
      "container": "camp-container"
    },
    "devContainerImage": "ghcr.io/example/devcontainer:locked",
    "devContainerPath": ".devcontainer/devcontainer.json",
    "devContainerConfig": {
      "name": "Camp fixture",
      "workspaceFolder": "/workspaces/camp"
    },
    "creationTimestamp": "2026-07-14T12:02:00Z",
    "lastUsed": "2026-07-14T12:03:00Z",
    "context": "default",
    "imported": true,
    "pro": {
      "instanceName": "camp-instance",
      "project": "camp-project",
      "displayName": "Camp Workspace"
    },
    "sshConfigPath": "/tmp/devpod/ssh/config",
    "sshConfigIncludePath": "/tmp/devpod/ssh/include"
  }
]`)}}
	client := NewClient("devpod", runner)
	got, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Workspace{{ID: "camp", UID: "uid-1", Provider: WorkspaceProvider{Name: "docker"}, Source: WorkspaceSource{LocalFolder: "/tmp/root"}, Context: "default"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %#v, want %#v", got, want)
	}
	wantCommand := ports.Command{Executable: "devpod", Argv: []string{"list", "--output", "json", "--skip-pro"}}
	if !reflect.DeepEqual(runner.commands[0], wantCommand) {
		t.Fatalf("command = %#v, want %#v", runner.commands[0], wantCommand)
	}
}

func TestStopAndDeleteRequireExplicitGates(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *Client, bool) error
		want []string
	}{
		{"stop", func(ctx context.Context, client *Client, allow bool) error {
			_, err := client.Stop(ctx, "camp", allow)
			return err
		}, []string{"stop", "camp"}},
		{"delete", func(ctx context.Context, client *Client, allow bool) error {
			_, err := client.Delete(ctx, "camp", allow)
			return err
		}, []string{"delete", "--ignore-not-found", "camp"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{}
			client := NewClient("devpod", runner)
			if err := tt.run(context.Background(), client, false); !errors.Is(err, ErrLifecycleActionNotAllowed) {
				t.Fatalf("denied error = %v, want ErrLifecycleActionNotAllowed", err)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("denied action invoked runner: %#v", runner.commands)
			}
			if err := tt.run(context.Background(), client, true); err != nil {
				t.Fatal(err)
			}
			wantCommand := ports.Command{Executable: "devpod", Argv: tt.want}
			if got := runner.commands[0]; !reflect.DeepEqual(got, wantCommand) {
				t.Fatalf("allowed command = %#v, want %#v", got, wantCommand)
			}
		})
	}
}

func TestUpPreservesEveryTypedRepeatedPublicFlag(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	client := NewClient("/opt/devpod", runner)
	openIDE := false
	configureSSH := false
	gpgForwarding := true
	_, err := client.Up(context.Background(), UpOptions{
		WorkspacePath:        "/tmp/capsule root",
		WorkspaceID:          "camp-second-brain",
		Context:              "default",
		Provider:             "ssh",
		Machine:              "harvester-dev",
		ProviderOptions:      []string{"HOST=dev-a", "PORT=2222"},
		DevcontainerImage:    "ghcr.io/example/dev:locked",
		DevcontainerPath:     ".devcontainer/devcontainer.json",
		DevcontainerID:       "primary",
		FallbackImage:        "ubuntu@sha256:abc",
		AdditionalFeatures:   `{"ghcr.io/devcontainers/features/git:1":{}}`,
		Mounts:               []string{"type=bind,source=/state,target=/state", "type=volume,source=cache,target=/cache"},
		WorkspaceEnv:         []string{"EDITOR=code", "LANG=C.UTF-8"},
		WorkspaceEnvFiles:    []string{"/etc/camp/base.env", "/etc/camp/user.env"},
		InitEnv:              []string{"CAMP_INIT=1", "CAMP_MODE=remote"},
		Dotfiles:             "https://example.test/dotfiles.git",
		Recreate:             true,
		PrebuildRepositories: []string{"ghcr.io/example/prebuild-a", "ghcr.io/example/prebuild-b"},
		IDE:                  IDEVSCodeInsiders,
		IDEOptions:           []string{"DISABLE_TELEMETRY=true", "DEFAULT_EXTENSIONS=one,two"},
		OpenIDE:              &openIDE,
		ConfigureSSH:         &configureSSH,
		GPGAgentForwarding:   &gpgForwarding,
		SSHConfigPath:        "/tmp/camp ssh/config",
		ForwardedArgv:        []string{"--log-output", "plain"},
	})
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	want := []string{
		"up", "--ide", "vscode-insiders", "--open-ide=false",
		"--context", "default", "--id", "camp-second-brain", "--provider", "ssh", "--machine", "harvester-dev",
		"--provider-option", "HOST=dev-a", "--provider-option", "PORT=2222",
		"--devcontainer-image", "ghcr.io/example/dev:locked", "--devcontainer-path", ".devcontainer/devcontainer.json", "--devcontainer-id", "primary",
		"--fallback-image", "ubuntu@sha256:abc", "--additional-features", `{"ghcr.io/devcontainers/features/git:1":{}}`,
		"--mount", "type=bind,source=/state,target=/state", "--mount", "type=volume,source=cache,target=/cache",
		"--workspace-env", "EDITOR=code", "--workspace-env", "LANG=C.UTF-8",
		"--workspace-env-file", "/etc/camp/base.env", "--workspace-env-file", "/etc/camp/user.env",
		"--init-env", "CAMP_INIT=1", "--init-env", "CAMP_MODE=remote",
		"--dotfiles", "https://example.test/dotfiles.git", "--recreate=true",
		"--prebuild-repository", "ghcr.io/example/prebuild-a", "--prebuild-repository", "ghcr.io/example/prebuild-b",
		"--ide-option", "DISABLE_TELEMETRY=true", "--ide-option", "DEFAULT_EXTENSIONS=one,two",
		"--configure-ssh=false", "--gpg-agent-forwarding=true", "--ssh-config", "/tmp/camp ssh/config",
		"--log-output", "plain", "/tmp/capsule root",
	}
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0].Argv, want) {
		t.Fatalf("commands = %#v, want argv %#v", runner.commands, want)
	}
}

func TestT3CodeUpUsesSafeTerminalDevPodSetup(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	_, err := NewClient("devpod", runner).Up(context.Background(), UpOptions{
		WorkspacePath: "/tmp/root", WorkspaceID: "camp", IDE: IDET3Code,
	})
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	want := []string{"up", "--ide", "none", "--open-ide=false", "--id", "camp", "/tmp/root"}
	if !reflect.DeepEqual(runner.commands[0].Argv, want) {
		t.Fatalf("argv = %#v, want %#v", runner.commands[0].Argv, want)
	}
}

func TestTerminalAndT3UpRejectDevPodOpenIDETrue(t *testing.T) {
	t.Parallel()
	openIDE := true
	for _, ide := range []IDE{IDETerminal, IDET3Code} {
		runner := &recordingRunner{}
		_, err := NewClient("devpod", runner).Up(context.Background(), UpOptions{
			WorkspacePath: "/tmp/root", WorkspaceID: "camp", IDE: ide, OpenIDE: &openIDE,
		})
		if !errors.Is(err, ErrInvalidIDEEntry) {
			t.Fatalf("Up(IDE %q) error = %v, want ErrInvalidIDEEntry", ide, err)
		}
		if len(runner.commands) != 0 {
			t.Fatalf("Up(IDE %q) commands = %#v, want none", ide, runner.commands)
		}
	}
}

func TestUpForwardsExplicitReset(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	_, err := NewClient("devpod", runner).Up(context.Background(), UpOptions{
		WorkspacePath: "/tmp/root", WorkspaceID: "camp", Reset: true,
	})
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	want := []string{"up", "--ide", "none", "--open-ide=false", "--id", "camp", "--reset=true", "/tmp/root"}
	if !reflect.DeepEqual(runner.commands[0].Argv, want) {
		t.Fatalf("argv = %#v, want %#v", runner.commands[0].Argv, want)
	}
}

func TestSSHPreservesEveryTypedRepeatedPublicFlag(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	client := NewClient("/opt/devpod", runner)
	agent := false
	gpg := true
	stdio := false
	installTerminfo := true
	_, err := client.SSH(context.Background(), SSHOptions{
		WorkspaceID:          "camp-second-brain",
		Context:              "default",
		Workdir:              "/workspaces/Second Brain",
		User:                 "vscode",
		ForwardPorts:         []string{"127.0.0.1:3773:127.0.0.1:3773", "127.0.0.1:8080:127.0.0.1:8080"},
		ReverseForwards:      []string{"127.0.0.1:5000:127.0.0.1:5000", "127.0.0.1:8081:127.0.0.1:8081"},
		SendEnv:              []string{"SSH_AUTH_SOCK", "TERM"},
		SetEnv:               []string{"CAMP_CAPSULE=second-brain", "CAMP_CHECKPOINT=42"},
		ForwardPortsTimeout:  "10m",
		AgentForwarding:      &agent,
		GPGAgentForwarding:   &gpg,
		Stdio:                &stdio,
		SSHKeepAliveInterval: "30s",
		GitSSHSigningKey:     "/home/vscode/.ssh/signing key.pub",
		TermMode:             "strict",
		InstallTerminfo:      &installTerminfo,
		StartServices:        true,
		ForwardedArgv:        []string{"--log-output", "plain"},
	})
	if err != nil {
		t.Fatalf("SSH() error = %v", err)
	}
	want := []string{
		"ssh", "--context", "default", "--workdir", "/workspaces/Second Brain", "--user", "vscode",
		"--forward-ports", "127.0.0.1:3773:127.0.0.1:3773", "--forward-ports", "127.0.0.1:8080:127.0.0.1:8080",
		"--reverse-forward-ports", "127.0.0.1:5000:127.0.0.1:5000", "--reverse-forward-ports", "127.0.0.1:8081:127.0.0.1:8081",
		"--send-env", "SSH_AUTH_SOCK", "--send-env", "TERM",
		"--set-env", "CAMP_CAPSULE=second-brain", "--set-env", "CAMP_CHECKPOINT=42",
		"--forward-ports-timeout", "10m", "--agent-forwarding=false", "--gpg-agent-forwarding=true", "--stdio=false",
		"--ssh-keepalive-interval", "30s", "--git-ssh-signing-key", "/home/vscode/.ssh/signing key.pub",
		"--term-mode", "strict", "--install-terminfo=true", "--log-output", "plain", "--start-services=true", "camp-second-brain",
	}
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0].Argv, want) {
		t.Fatalf("commands = %#v, want argv %#v", runner.commands, want)
	}
}

func TestSSHCommandKeepsInteractiveStreamsAttached(t *testing.T) {
	t.Parallel()
	stdin := strings.NewReader("input")
	var stdout, stderr bytes.Buffer
	command, err := NewClient("devpod", &recordingRunner{}).SSHCommand(SSHOptions{
		WorkspaceID: "camp", Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Stdin != stdin || command.Stdout != &stdout || command.Stderr != &stderr {
		t.Fatalf("command streams = (%#v, %#v, %#v)", command.Stdin, command.Stdout, command.Stderr)
	}
}

func TestTypedAndRawDevPodFlagConflictsFailWithoutRunning(t *testing.T) {
	t.Parallel()
	agent := false
	tests := []struct {
		name string
		run  func(*Client) error
	}{
		{"up provider equals form", func(client *Client) error {
			_, err := client.Up(context.Background(), UpOptions{WorkspacePath: "/tmp/root", Provider: "docker", ForwardedArgv: []string{"--provider=ssh"}})
			return err
		}},
		{"up invariant IDE", func(client *Client) error {
			_, err := client.Up(context.Background(), UpOptions{WorkspacePath: "/tmp/root", ForwardedArgv: []string{"--ide", "vscode"}})
			return err
		}},
		{"up repeated mount", func(client *Client) error {
			_, err := client.Up(context.Background(), UpOptions{WorkspacePath: "/tmp/root", Mounts: []string{"type=volume,source=a,target=/a"}, ForwardedArgv: []string{"--mount", "type=volume,source=b,target=/b"}})
			return err
		}},
		{"ssh reverse short form", func(client *Client) error {
			_, err := client.SSH(context.Background(), SSHOptions{WorkspaceID: "camp", ReverseForwards: []string{"127.0.0.1:1:127.0.0.1:1"}, ForwardedArgv: []string{"-R", "127.0.0.1:2:127.0.0.1:2"}})
			return err
		}},
		{"ssh explicit agent bool", func(client *Client) error {
			_, err := client.SSH(context.Background(), SSHOptions{WorkspaceID: "camp", AgentForwarding: &agent, ForwardedArgv: []string{"--agent-forwarding=true"}})
			return err
		}},
		{"ssh invariant services", func(client *Client) error {
			_, err := client.SSH(context.Background(), SSHOptions{WorkspaceID: "camp", ForwardedArgv: []string{"--start-services", "true"}})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingRunner{}
			err := test.run(NewClient("devpod", runner))
			if !errors.Is(err, ErrDevPodArgumentConflict) {
				t.Fatalf("error = %v, want ErrDevPodArgumentConflict", err)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("commands = %#v, want none", runner.commands)
			}
		})
	}
}

func TestDevPodPassthroughPolicyFailsClosedBeforeEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		argv []string
		want error
	}{
		{name: "lifecycle", argv: []string{"up", "."}, want: ErrDevPodPassthroughDenied},
		{name: "session identity", argv: []string{"status", "camp-session"}, want: ErrDevPodPassthroughDenied},
		{name: "reserved context", argv: []string{"version", "--context", "other"}, want: ErrDevPodPassthroughConflict},
		{name: "reserved environment", argv: []string{"version", "--env", "CAMP_CAPSULE=other"}, want: ErrDevPodPassthroughConflict},
		{name: "argv boundary", argv: []string{"version\nup"}, want: ErrDevPodPassthroughInvalid},
		{name: "unknown", argv: []string{"future-command"}, want: ErrDevPodPassthroughUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{}
			_, err := NewClient("devpod", runner).Passthrough(context.Background(), test.argv)
			if !errors.Is(err, test.want) {
				t.Fatalf("Passthrough(%#v) error = %v, want %v", test.argv, err, test.want)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("commands = %#v, want none", runner.commands)
			}
		})
	}
}

func TestDevPodPassthroughAllowsOnlyExactEffectFreeCommands(t *testing.T) {
	t.Parallel()
	for _, argv := range [][]string{{"version"}, {"help"}, {"--help"}} {
		runner := &recordingRunner{}
		_, err := NewClient("/opt/devpod", runner).Passthrough(context.Background(), argv)
		if err != nil {
			t.Fatalf("Passthrough(%#v) error = %v", argv, err)
		}
		want := ports.Command{Executable: "/opt/devpod", Argv: argv}
		if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0], want) {
			t.Fatalf("commands = %#v, want %#v", runner.commands, want)
		}
	}
}
