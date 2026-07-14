package devpod

import (
	"context"
	"errors"
	"reflect"
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
			name: "terminal SSH preserves separate user input argv",
			run: func(ctx context.Context, client *Client) error {
				_, err := client.SSH(ctx, SSHOptions{WorkspaceID: "camp-second-brain", Workdir: "/workspaces/root; echo unsafe", User: "dev"})
				return err
			},
			want: []string{"ssh", "--workdir", "/workspaces/root; echo unsafe", "--user", "dev", "camp-second-brain"},
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
	runner := &recordingRunner{result: ports.Result{Stdout: []byte(`[{"id":"camp","uid":"uid-1","provider":{"name":"docker"},"source":{"localFolder":"/tmp/root"},"context":"default"}]`)}}
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
