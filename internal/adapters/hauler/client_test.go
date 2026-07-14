package hauler

import (
	"context"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type recordingRunner struct{ commands []ports.Command }

func (r *recordingRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.commands = append(r.commands, command)
	return ports.Result{}, nil
}

func TestExactHaulerV201Argv(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *Client) error
		want []string
	}{
		{"load", func(ctx context.Context, c *Client) error {
			_, err := c.Load(ctx, "/tmp/store", []string{"/tmp/one.tar.zst", "/tmp/two.tar.zst"})
			return err
		}, []string{"store", "--store", "/tmp/store", "load", "--filename", "/tmp/one.tar.zst", "--filename", "/tmp/two.tar.zst"}},
		{"extract", func(ctx context.Context, c *Client) error {
			_, err := c.Extract(ctx, "/tmp/store", "hauler/capsule-root.tar.zst", "/tmp/out")
			return err
		}, []string{"store", "--store", "/tmp/store", "extract", "hauler/capsule-root.tar.zst", "--output", "/tmp/out"}},
		{"sync", func(ctx context.Context, c *Client) error {
			_, err := c.Sync(ctx, "/tmp/store", []string{"manifest-a.yaml", "manifest-b.yaml"})
			return err
		}, []string{"store", "--store", "/tmp/store", "sync", "--filename", "manifest-a.yaml", "--filename", "manifest-b.yaml"}},
		{"add remote image", func(ctx context.Context, c *Client) error {
			_, err := c.AddImage(ctx, "/tmp/store", AddImageOptions{Reference: "ghcr.io/team/app:v1", Platform: "linux/amd64", Rewrite: "camp.local/app:v1"})
			return err
		}, []string{"store", "--store", "/tmp/store", "add", "image", "ghcr.io/team/app:v1", "--platform", "linux/amd64", "--rewrite", "camp.local/app:v1"}},
		{"add daemon image", func(ctx context.Context, c *Client) error {
			_, err := c.AddImage(ctx, "/tmp/store", AddImageOptions{Reference: "team/app:dev", Local: true})
			return err
		}, []string{"store", "--store", "/tmp/store", "add", "image", "team/app:dev", "--local"}},
		{"save", func(ctx context.Context, c *Client) error {
			_, err := c.Save(ctx, "/tmp/store", "/tmp/capsule.tar.zst")
			return err
		}, []string{"store", "--store", "/tmp/store", "save", "--filename", "/tmp/capsule.tar.zst"}},
		{"info JSON", func(ctx context.Context, c *Client) error { _, err := c.Info(ctx, "/tmp/store"); return err }, []string{"store", "--store", "/tmp/store", "info", "--output", "json", "--digests"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{}
			client := NewClient("/opt/hauler", runner)
			if err := tt.run(context.Background(), client); err != nil {
				t.Fatal(err)
			}
			if len(runner.commands) != 1 {
				t.Fatalf("commands = %d, want 1", len(runner.commands))
			}
			got := runner.commands[0]
			want := ports.Command{Executable: "/opt/hauler", Argv: tt.want}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("command = %#v, want %#v", got, want)
			}
		})
	}
}

func TestSyncFromUsesStructuredWorkingDirectory(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	client := NewClient("/opt/hauler", runner)
	if _, err := client.SyncFrom(context.Background(), "/tmp/store", []string{"/tmp/root/.camp/hauler-manifest.yaml"}, "/tmp/root"); err != nil {
		t.Fatal(err)
	}
	want := ports.Command{
		Executable: "/opt/hauler", Directory: "/tmp/root",
		Argv: []string{"store", "--store", "/tmp/store", "sync", "--filename", "/tmp/root/.camp/hauler-manifest.yaml"},
	}
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0], want) {
		t.Fatalf("command = %#v, want %#v", runner.commands, want)
	}
}

func TestExactHaulerV201ServiceArgv(t *testing.T) {
	client := NewClient("hauler", &recordingRunner{})
	tests := []struct {
		name string
		got  ports.Command
		want []string
	}{
		{"registry", client.Registry("/tmp/store", RegistryOptions{Directory: "/tmp/registry", Port: 5001, ReadOnly: false}), []string{"store", "--store", "/tmp/store", "serve", "registry", "--directory", "/tmp/registry", "--port", "5001", "--readonly=false"}},
		{"fileserver", client.Fileserver("/tmp/store", FileserverOptions{Directory: "/tmp/files", Port: 8081, TimeoutSeconds: 90}), []string{"store", "--store", "/tmp/store", "serve", "fileserver", "--directory", "/tmp/files", "--port", "8081", "--timeout", "90"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := ports.Command{Executable: "hauler", Argv: tt.want}
			if !reflect.DeepEqual(tt.got, want) {
				t.Fatalf("command = %#v, want %#v", tt.got, want)
			}
		})
	}
}
