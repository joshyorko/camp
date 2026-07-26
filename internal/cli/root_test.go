package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/presentation"
	"github.com/spf13/cobra"
)

func TestExecuteRejectsUnavailableAndUnknownCommands(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"unknown-token"} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(context.Background(), NewRoot(), []string{token}, Streams{
				Out:    &stdout,
				ErrOut: &stderr,
			})

			if exitCode != int(ExitUsage) {
				t.Fatalf("Execute(%q) exit code = %d, want %d", token, exitCode, ExitUsage)
			}
			if stdout.Len() != 0 {
				t.Fatalf("Execute(%q) stdout = %q, want empty", token, stdout.String())
			}
			if !strings.Contains(stderr.String(), "unknown command") {
				t.Fatalf("Execute(%q) stderr = %q, want unknown-command error", token, stderr.String())
			}
		})
	}
}

func TestConfigCommandsDispatchEffectiveShowAndSet(t *testing.T) {
	var gotShow bool
	var gotSet string
	lifecycle := &recordingLifecycle{}
	root := NewRootWithLifecycle(configLifecycle{
		Lifecycle: lifecycle,
		show: func(_ context.Context, effective, redact bool, _ OutputMode, _ io.Writer) error {
			gotShow = effective && redact
			return nil
		},
		set: func(_ context.Context, key, value string, _ OutputMode, _ io.Writer) error {
			gotSet = key + "=" + value
			return nil
		},
	})

	if code := Execute(context.Background(), root, []string{"config", "show", "--effective"}, Streams{}); code != int(ExitSuccess) || !gotShow {
		t.Fatalf("config show code=%d gotShow=%v", code, gotShow)
	}
	if code := Execute(context.Background(), root, []string{"config", "set", "backend", "file:///srv/camp"}, Streams{}); code != int(ExitSuccess) || gotSet != "backend=file:///srv/camp" {
		t.Fatalf("config set code=%d gotSet=%q", code, gotSet)
	}
	var help bytes.Buffer
	if code := Execute(context.Background(), root, []string{"config", "show", "--help"}, Streams{Out: &help}); code != int(ExitSuccess) {
		t.Fatalf("config show help code=%d", code)
	}
	if strings.Contains(help.String(), "--redact") {
		t.Fatalf("config show help exposes secret-dump toggle:\n%s", help.String())
	}
}

type configLifecycle struct {
	Lifecycle
	show func(context.Context, bool, bool, OutputMode, io.Writer) error
	set  func(context.Context, string, string, OutputMode, io.Writer) error
}

func (c configLifecycle) ConfigShow(ctx context.Context, effective, redact bool, mode OutputMode, out io.Writer) error {
	return c.show(ctx, effective, redact, mode, out)
}

func (c configLifecycle) ConfigSet(ctx context.Context, key, value string, mode OutputMode, out io.Writer) error {
	return c.set(ctx, key, value, mode, out)
}

func TestLifecycleCommandsDelegateWithStrictArgumentsAndInheritedMode(t *testing.T) {
	t.Parallel()

	lifecycle := &recordingLifecycle{}
	tmp := t.TempDir()
	inspectFile := tmp + "/input.campkit"
	verifyFile := tmp + "/verify.campkit"
	if err := os.WriteFile(inspectFile, []byte("inspect"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verifyFile, []byte("verify"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "init", args: []string{"--json", "init", "/brain", "--name", "brain"}, want: "init:/brain:json"},
		{name: "configured init", args: []string{"init", "/brain", "--name", "brain", "--backend", "file:///srv/camp", "--workspace-provider", "room-of-requirement", "--workspace-context", "ror"}, want: "init:/brain:file:///srv/camp:brain:room-of-requirement:ror:human"},
		{name: "setup", args: []string{"setup"}, want: "setup::human"},
		{name: "open", args: []string{"open", "memoryd"}, want: "open:memoryd:human"},
		{name: "sync", args: []string{"sync"}, want: "sync::human"},
		{name: "close", args: []string{"close"}, want: "close:false:human"},
		{name: "close-discard", args: []string{"close", "--discard"}, want: "close:true:human"},
		{name: "reopen", args: []string{"reopen", "memoryd"}, want: "reopen:memoryd:human"},
		{name: "recover", args: []string{"recover", "session-1"}, want: "recover:session-1:human"},
		{name: "supervise", args: []string{"supervise", "session-1"}, want: "supervise:session-1:human"},
		{name: "kit inspect", args: []string{"kit", "inspect", inspectFile}, want: "kit-inspect:" + inspectFile + ":human"},
		{name: "kit inspect json", args: []string{"--json", "kit", "inspect", inspectFile}, want: "kit-inspect:" + inspectFile + ":json"},
		{name: "kit verify", args: []string{"kit", "verify", verifyFile}, want: "kit-verify:" + verifyFile + ":human"},
		{name: "kit verify json", args: []string{"--json", "kit", "verify", verifyFile}, want: "kit-verify:" + verifyFile + ":json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := NewRootWithLifecycle(lifecycle)
			var stdout, stderr bytes.Buffer
			if code := Execute(context.Background(), root, test.args, Streams{Out: &stdout, ErrOut: &stderr}); code != int(ExitSuccess) {
				t.Fatalf("Execute(%q) = %d, stderr=%q", test.args, code, stderr.String())
			}
			if got := lifecycle.calls[len(lifecycle.calls)-1]; got != test.want {
				t.Fatalf("call = %q, want %q", got, test.want)
			}
		})
	}

	for _, args := range [][]string{
		{"init", "a", "b"},
		{"open", "a", "b"},
		{"sync", "x"},
		{"close", "x"},
		{"reopen", "a", "b"},
		{"recover", "a", "b"},
		{"doctor", "x"},
		{"supervise", "a", "b"},
		{"kit", "inspect"},
		{"kit", "inspect", inspectFile, "extra"},
		{"kit", "verify", "extra", "path"},
		{"kit", "inspect", tmp},
		{"kit", "verify", tmp},
	} {
		var stderr bytes.Buffer
		if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), args, Streams{ErrOut: &stderr}); code != int(ExitUsage) {
			t.Fatalf("Execute(%q) = %d, want usage; stderr=%q", args, code, stderr.String())
		}
	}
}

func TestLifecycleKitCommandRejectsInvalidPaths(t *testing.T) {
	t.Parallel()

	lifecycle := &recordingLifecycle{}
	tmp := t.TempDir()
	dir := tmp + "/dir"
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := tmp + "/target.campkit"
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := tmp + "/symlink.campkit"
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	missing := tmp + "/missing.campkit"

	for _, args := range [][]string{
		{"kit", "inspect", missing},
		{"kit", "verify", missing},
		{"kit", "inspect", dir},
		{"kit", "verify", dir},
		{"kit", "inspect", symlink},
		{"kit", "verify", symlink},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stderr bytes.Buffer
			code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), args, Streams{ErrOut: &stderr})
			if code != int(ExitUsage) {
				t.Fatalf("Execute(%q) = %d, stderr=%q", args, code, stderr.String())
			}
			if len(lifecycle.calls) != 0 {
				t.Fatalf("kit path check bypassed for %q: %#v", args, lifecycle.calls)
			}
		})
	}
}

func TestLifecycleCommandsDelegateSetupStdin(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	input := strings.NewReader("setup answers")
	root := NewRootWithLifecycle(lifecycle)
	root.SetIn(input)
	root.SetOut(io.Discard)
	root.SetArgs([]string{"setup"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if lifecycle.setupInput != input {
		t.Fatalf("setup input = %#v, want command stdin", lifecycle.setupInput)
	}
}

type recordingLifecycle struct {
	calls          []string
	attachRequests []AttachRequest
	initRequests   []InitRequest
	selections     []Selection
	setupInput     io.Reader
	initInput      io.Reader
}

func (r *recordingLifecycle) KitInspect(_ context.Context, file string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "kit-inspect:"+file+":"+string(mode))
	return nil
}

func (r *recordingLifecycle) KitVerify(_ context.Context, file string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "kit-verify:"+file+":"+string(mode))
	return nil
}

func (r *recordingLifecycle) InitInteractive(_ context.Context, request InitRequest, mode OutputMode, in io.Reader, _ io.Writer) error {
	r.initInput = in
	r.initRequests = append(r.initRequests, request)
	r.calls = append(r.calls, "init-interactive:"+request.Root+":"+string(mode))
	return nil
}

type recordingStriker struct {
	*recordingLifecycle
	request StrikeRequest
}

func (r *recordingLifecycle) Status(ctx context.Context, mode OutputMode, _ io.Writer) error {
	r.selections = append(r.selections, SelectionFromContext(ctx))
	r.calls = append(r.calls, "status::"+string(mode))
	return nil
}

type recordingOperationalLifecycle struct {
	*recordingLifecycle
	imageListRequests    []SessionRequest
	imageRestoreRequests []SessionRequest
	serveStatusRequests  []ServeRequest
	serveLogsRequests    []ServeLogsRequest
	serveRestartRequests []ServeRestartRequest
	providerListCalls    int
}

func (r *recordingOperationalLifecycle) ImagesList(_ context.Context, request SessionRequest, mode OutputMode, _ io.Writer) error {
	r.imageListRequests = append(r.imageListRequests, request)
	r.calls = append(r.calls, "images-list:"+string(mode))
	return nil
}

func (r *recordingOperationalLifecycle) ImagesRestore(_ context.Context, request SessionRequest, mode OutputMode, _ io.Writer) error {
	r.imageRestoreRequests = append(r.imageRestoreRequests, request)
	r.calls = append(r.calls, "images-restore:"+string(mode))
	return nil
}

func (r *recordingOperationalLifecycle) ServeStatus(_ context.Context, request ServeRequest, mode OutputMode, _ io.Writer) error {
	r.serveStatusRequests = append(r.serveStatusRequests, request)
	r.calls = append(r.calls, "serve-status:"+string(mode))
	return nil
}

func (r *recordingOperationalLifecycle) ServeLogs(_ context.Context, request ServeLogsRequest, mode OutputMode, _ io.Writer) error {
	r.serveLogsRequests = append(r.serveLogsRequests, request)
	r.calls = append(r.calls, "serve-logs:"+string(mode))
	return nil
}

func (r *recordingOperationalLifecycle) ServeRestart(_ context.Context, request ServeRestartRequest, mode OutputMode, _ io.Writer) error {
	r.serveRestartRequests = append(r.serveRestartRequests, request)
	r.calls = append(r.calls, "serve-restart:"+string(mode))
	return nil
}

func (r *recordingOperationalLifecycle) ProvidersList(_ context.Context, mode OutputMode, _ io.Writer) error {
	r.providerListCalls++
	r.calls = append(r.calls, "provider-list:"+string(mode))
	return nil
}

func TestOperationalCommandsMapStableBoundaryRequests(t *testing.T) {
	t.Parallel()

	lifecycle := &recordingOperationalLifecycle{recordingLifecycle: &recordingLifecycle{}}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "images list", args: []string{"images", "list", "--session", "session-2"}, want: "images-list:human"},
		{name: "images restore", args: []string{"images", "restore", "--branch", "main"}, want: "images-restore:human"},
		{name: "serve status", args: []string{"serve", "status", "registry", "--session", "session-3"}, want: "serve-status:human"},
		{name: "serve logs", args: []string{"serve", "logs", "fileserver", "--tail-bytes", "4096"}, want: "serve-logs:human"},
		{name: "serve restart", args: []string{"serve", "restart", "registry", "--launch-token", "token-1"}, want: "serve-restart:human"},
		{name: "provider list", args: []string{"provider", "list"}, want: "provider-list:human"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), test.args, Streams{Out: io.Discard, ErrOut: &stderr}); code != int(ExitSuccess) {
				t.Fatalf("Execute(%q) = %d, stderr=%q", test.args, code, stderr.String())
			}
			if got := lifecycle.calls[len(lifecycle.calls)-1]; got != test.want {
				t.Fatalf("call = %q, want %q", got, test.want)
			}
		})
	}

	if got := lifecycle.serveLogsRequests[0]; got.Service != "fileserver" || got.TailBytes != 4096 {
		t.Fatalf("serve logs request = %#v", got)
	}
	if got := lifecycle.serveRestartRequests[0]; got.Service != "registry" || got.LaunchToken != "token-1" {
		t.Fatalf("serve restart request = %#v", got)
	}
}

func TestImagesCaptureFailsWithCheckpointGuidanceBeforeEffects(t *testing.T) {
	t.Parallel()
	lifecycle := &recordingOperationalLifecycle{recordingLifecycle: &recordingLifecycle{}}
	var stderr bytes.Buffer

	code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"images", "capture", "--session", "session-2"}, Streams{ErrOut: &stderr})
	if code != int(ExitFailure) {
		t.Fatalf("Execute(images capture) = %d, stderr=%q; want failure", code, stderr.String())
	}
	if want := "camp images capture does not inspect workspace engines; push images through CAMP_REGISTRY, then run camp sync or camp close"; !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want stable checkpoint guidance %q", stderr.String(), want)
	}
	if len(lifecycle.calls) != 0 {
		t.Fatalf("images capture dispatched effects: %#v", lifecycle.calls)
	}
}

func TestOperationalCommandsRejectInvalidRequestsBeforeEffects(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"status", "extra"},
		{"images", "capture", "--exclude-tag", "volatile"},
		{"serve", "logs", "registry", "--tail-bytes", "0"},
		{"serve", "restart", "registry"},
		{"provider", "update"},
		{"config"},
	} {
		lifecycle := &recordingOperationalLifecycle{recordingLifecycle: &recordingLifecycle{}}
		var stderr bytes.Buffer
		code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), args, Streams{ErrOut: &stderr})
		if code != int(ExitUsage) {
			t.Fatalf("Execute(%q) = %d, stderr=%q; want usage", args, code, stderr.String())
		}
		if len(lifecycle.calls) != 0 {
			t.Fatalf("Execute(%q) invoked effects: %v", args, lifecycle.calls)
		}
	}
}

func (r *recordingStriker) Strike(_ context.Context, request StrikeRequest, _ OutputMode, _ io.Writer) error {
	r.request = request
	return nil
}

func TestStrikeCommandMapsPurgeConfirmation(t *testing.T) {
	lifecycle := &recordingStriker{recordingLifecycle: &recordingLifecycle{}}
	if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"strike", "--purge", "--yes"}, Streams{Out: io.Discard, ErrOut: io.Discard}); code != int(ExitSuccess) {
		t.Fatalf("strike exit = %d", code)
	}
	if !lifecycle.request.Purge || !lifecycle.request.Yes {
		t.Fatalf("strike request = %+v", lifecycle.request)
	}
}

func (r *recordingLifecycle) Init(_ context.Context, request InitRequest, mode OutputMode, _ io.Writer) error {
	r.initRequests = append(r.initRequests, request)
	if request.Backend == "" && request.DevPodProvider == "" {
		r.calls = append(r.calls, "init:"+request.Root+":"+string(mode))
		return nil
	}
	r.calls = append(r.calls, "init:"+request.Root+":"+request.Backend+":"+request.Capsule+":"+request.DevPodProvider+":"+request.DevPodContext+":"+string(mode))
	return nil
}
func TestInitRequiresNameOutsideMigration(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--json", "init"},
		{"--json", "init", "/brain"},
		{"--json", "init", "/brain", "--backend", "file:///srv/camp"},
	} {
		var output bytes.Buffer
		code := Execute(context.Background(), NewRootWithLifecycle(&recordingLifecycle{}), args, Streams{Out: &output, ErrOut: &output})
		if code != int(ExitUsage) || !strings.Contains(output.String(), "requires --name") {
			t.Fatalf("Execute(%q) code=%d output=%q, want name requirement", args, code, output.String())
		}
	}
}

func TestInitMigrationRejectsCampArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"init", "/other", "--migrate"},
		{"init", "--migrate", "--name", "brain"},
	} {
		var stderr bytes.Buffer
		code := Execute(context.Background(), NewRootWithLifecycle(&recordingLifecycle{}), args, Streams{ErrOut: &stderr})
		if code != int(ExitUsage) {
			t.Fatalf("Execute(%q) code=%d stderr=%q, want usage failure", args, code, stderr.String())
		}
	}
}

func TestInitUsesCampNameAndMigrationContracts(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"init", "/brain", "--name", "brain", "--backend", "file:///srv/camp", "--workspace-provider", "docker", "--workspace-context", "default"}, Streams{Out: io.Discard, ErrOut: io.Discard}); code != int(ExitSuccess) {
		t.Fatalf("named init exit = %d", code)
	}
	request := lifecycle.initRequests[len(lifecycle.initRequests)-1]
	if request.Root != "/brain" || request.Capsule != "brain" || request.Backend != "file:///srv/camp" || request.DevPodProvider != "docker" || request.DevPodContext != "default" {
		t.Fatalf("named init request = %#v", request)
	}
	if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"init", "--migrate"}, Streams{Out: io.Discard, ErrOut: io.Discard}); code != int(ExitSuccess) {
		t.Fatalf("migration init exit = %d", code)
	}
	if !lifecycle.initRequests[len(lifecycle.initRequests)-1].Migrate {
		t.Fatal("init --migrate did not reach lifecycle")
	}
}

func TestHumanInitWithoutNameDelegatesInteractiveInput(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	input := strings.NewReader("alpha\n")
	if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"init", "/brain"}, Streams{In: input, Out: io.Discard, ErrOut: io.Discard}); code != int(ExitSuccess) {
		t.Fatalf("interactive init exit = %d", code)
	}
	if lifecycle.initInput != input {
		t.Fatalf("interactive input = %#v, want command stdin", lifecycle.initInput)
	}
	var stderr bytes.Buffer
	if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"--json", "init", "/brain"}, Streams{Out: io.Discard, ErrOut: &stderr}); code != int(ExitUsage) {
		t.Fatalf("JSON init without name exit = %d, stderr=%q", code, stderr.String())
	}
}

func (r *recordingLifecycle) Setup(_ context.Context, mode OutputMode, in io.Reader, _ io.Writer) error {
	r.setupInput = in
	r.calls = append(r.calls, "setup::"+string(mode))
	return nil
}
func (r *recordingLifecycle) Open(ctx context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.selections = append(r.selections, SelectionFromContext(ctx))
	r.calls = append(r.calls, "open:"+value+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Attach(ctx context.Context, request AttachRequest, mode OutputMode, _ io.Writer) error {
	r.selections = append(r.selections, SelectionFromContext(ctx))
	r.attachRequests = append(r.attachRequests, request)
	r.calls = append(r.calls, "attach:"+request.Target+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Sync(ctx context.Context, mode OutputMode, _ io.Writer) error {
	r.selections = append(r.selections, SelectionFromContext(ctx))
	r.calls = append(r.calls, "sync::"+string(mode))
	return nil
}
func (r *recordingLifecycle) Close(ctx context.Context, request CloseRequest, mode OutputMode, _ io.Writer) error {
	r.selections = append(r.selections, SelectionFromContext(ctx))
	r.calls = append(r.calls, fmt.Sprintf("close:%t:%s", request.Discard, mode))
	return nil
}

func TestLifecycleSelectorsAreSharedAndPositionalsKeepCommandMeaning(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	tests := []struct {
		args      []string
		selection Selection
	}{
		{args: []string{"open", "/work/alpha", "--camp", "alpha", "--session", "session-a"}, selection: Selection{Camp: "alpha", Session: "session-a"}},
		{args: []string{"attach", "src", "--camp", "alpha"}, selection: Selection{Camp: "alpha"}},
		{args: []string{"sync", "--session", "session-a"}, selection: Selection{Session: "session-a"}},
		{args: []string{"close", "--camp", "alpha"}, selection: Selection{Camp: "alpha"}},
		{args: []string{"status", "--camp", "alpha"}, selection: Selection{Camp: "alpha"}},
		{args: []string{"reopen", "session-a", "--camp", "alpha"}, selection: Selection{Camp: "alpha", Session: "session-a"}},
		{args: []string{"recover", "session-a", "--camp", "alpha"}, selection: Selection{Camp: "alpha", Session: "session-a"}},
	}
	for _, test := range tests {
		if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), test.args, Streams{Out: io.Discard, ErrOut: io.Discard}); code != int(ExitSuccess) {
			t.Fatalf("Execute(%q) = %d", test.args, code)
		}
		got := lifecycle.selections[len(lifecycle.selections)-1]
		if got != test.selection {
			t.Fatalf("Execute(%q) selection = %#v, want %#v", test.args, got, test.selection)
		}
	}
	if got := lifecycle.attachRequests[len(lifecycle.attachRequests)-1].Target; got != "src" {
		t.Fatalf("attach target = %q, want src", got)
	}
}
func (r *recordingLifecycle) Reopen(ctx context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.selections = append(r.selections, SelectionFromContext(ctx))
	r.calls = append(r.calls, "reopen:"+value+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Recover(ctx context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.selections = append(r.selections, SelectionFromContext(ctx))
	r.calls = append(r.calls, "recover:"+value+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Supervise(_ context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "supervise:"+value+":"+string(mode))
	return nil
}

func (r *recordingLifecycle) Doctor(_ context.Context, mode OutputMode, output io.Writer) error {
	r.calls = append(r.calls, "doctor:"+string(mode))
	_, _ = io.WriteString(output, "doctor output\n")
	return nil
}

func TestDoctorCommandUsesInheritedOutputModeAndRejectsArguments(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"doctor", "--json"}, Streams{Out: &stdout, ErrOut: &stderr}); code != int(ExitSuccess) {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.Join(lifecycle.calls, ","); got != "doctor:json" || stdout.String() != "doctor output\n" {
		t.Fatalf("calls = %q, stdout = %q", got, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"doctor", "extra"}, Streams{Out: &stdout, ErrOut: &stderr}); code != int(ExitUsage) {
		t.Fatalf("argument exit = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestExecuteRejectsInvalidCompletionArguments(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"completion"},
		{"completion", "powershell"},
		{"completion", "bash", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			exitCode := Execute(context.Background(), NewRoot(), args, Streams{ErrOut: &stderr})
			if exitCode != int(ExitUsage) {
				t.Fatalf("Execute(%q) exit code = %d, want %d", args, exitCode, ExitUsage)
			}
			if !strings.Contains(stderr.String(), "[usage]") {
				t.Fatalf("Execute(%q) stderr = %q, want usage error", args, stderr.String())
			}
		})
	}
}

func TestExecuteRejectsInvalidHelpArguments(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		args    []string
		message string
	}{
		{name: "unknown topic", args: []string{"help", "unknown-token"}, message: "unknown help topic \"unknown-token\""},
		{name: "extra topic component", args: []string{"help", "completion", "extra"}, message: "unknown help topic \"completion extra\""},
		{name: "garbage after help flag", args: []string{"--help", "garbage"}, message: "unexpected argument \"garbage\" after help flag"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []OutputMode{ModeHuman, ModeJSON} {
				t.Run(string(mode), func(t *testing.T) {
					t.Parallel()
					args := append([]string(nil), test.args...)
					if mode == ModeJSON {
						args = append([]string{"--json"}, args...)
					}

					var stdout bytes.Buffer
					var stderr bytes.Buffer
					exitCode := Execute(context.Background(), NewRoot(), args, Streams{
						Out:    &stdout,
						ErrOut: &stderr,
					})

					if exitCode != int(ExitUsage) {
						t.Fatalf("Execute(%q) exit code = %d, want %d", args, exitCode, ExitUsage)
					}
					if mode == ModeJSON {
						if stderr.Len() != 0 {
							t.Fatalf("Execute(%q) stderr = %q, want empty", args, stderr.String())
						}
						want := fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"kind\": \"error\",\n  \"error\": {\n    \"code\": \"usage\",\n    \"message\": %q\n  }\n}\n", test.message)
						if stdout.String() != want {
							t.Fatalf("Execute(%q) stdout:\n%s\nwant:\n%s", args, stdout.String(), want)
						}
						return
					}
					if stdout.Len() != 0 {
						t.Fatalf("Execute(%q) stdout = %q, want empty", args, stdout.String())
					}
					want := fmt.Sprintf("error [usage]: %s\n", test.message)
					if stderr.String() != want {
						t.Fatalf("Execute(%q) stderr = %q, want %q", args, stderr.String(), want)
					}
				})
			}
		})
	}
}

func TestExecutePreservesValidHelp(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--help"},
		{"help"},
		{"help", "completion"},
		{"completion", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(context.Background(), NewRoot(), args, Streams{Out: &stdout, ErrOut: &stderr})
			if exitCode != int(ExitSuccess) {
				t.Fatalf("Execute(%q) exit code = %d, want %d; stderr=%q", args, exitCode, ExitSuccess, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatalf("Execute(%q) stdout is empty, want help", args)
			}
			if stderr.Len() != 0 {
				t.Fatalf("Execute(%q) stderr = %q, want empty", args, stderr.String())
			}
		})
	}
}

func TestExecuteNormalizesJSONFlagSemantics(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		args        []string
		wantExit    ExitCode
		wantJSON    bool
		wantMessage string
	}{
		{
			name:        "alternate true before invalid help topic",
			args:        []string{"--json=1", "help", "unknown-token"},
			wantExit:    ExitUsage,
			wantJSON:    true,
			wantMessage: "unknown help topic",
		},
		{
			name:        "invalid bool value is usage",
			args:        []string{"--json=maybe", "open"},
			wantExit:    ExitUsage,
			wantMessage: "invalid argument",
		},
		{
			name:        "unknown flag is usage",
			args:        []string{"--unknown"},
			wantExit:    ExitUsage,
			wantMessage: "unknown flag",
		},
		{
			name:        "last bool value wins",
			args:        []string{"--json", "--json=false", "unknown-token"},
			wantExit:    ExitUsage,
			wantMessage: "unknown command",
		},
		{
			name:        "terminator keeps json positional",
			args:        []string{"--", "--json"},
			wantExit:    ExitUsage,
			wantMessage: "unknown command",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(context.Background(), NewRoot(), test.args, Streams{Out: &stdout, ErrOut: &stderr})
			if exitCode != int(test.wantExit) {
				t.Fatalf("Execute(%q) exit code = %d, want %d", test.args, exitCode, test.wantExit)
			}
			if test.wantJSON {
				if stderr.Len() != 0 {
					t.Fatalf("Execute(%q) stderr = %q, want empty", test.args, stderr.String())
				}
				if !strings.Contains(stdout.String(), `"code": "usage"`) || !strings.Contains(stdout.String(), test.wantMessage) {
					t.Fatalf("Execute(%q) stdout = %q, want JSON usage containing %q", test.args, stdout.String(), test.wantMessage)
				}
				return
			}
			if stdout.Len() != 0 {
				t.Fatalf("Execute(%q) stdout = %q, want empty", test.args, stdout.String())
			}
			if !strings.Contains(stderr.String(), "[usage]") || !strings.Contains(stderr.String(), test.wantMessage) {
				t.Fatalf("Execute(%q) stderr = %q, want human usage containing %q", test.args, stderr.String(), test.wantMessage)
			}
		})
	}
}

func TestExecuteRejectsArgumentsAfterRootHelpFlag(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--help", "completion"},
		{"--json=1", "--help", "completion"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(context.Background(), NewRoot(), args, Streams{Out: &stdout, ErrOut: &stderr})
			if exitCode != int(ExitUsage) {
				t.Fatalf("Execute(%q) exit code = %d, want %d", args, exitCode, ExitUsage)
			}
			if args[0] == "--json=1" {
				if stderr.Len() != 0 || !strings.Contains(stdout.String(), `"code": "usage"`) {
					t.Fatalf("Execute(%q) stdout=%q stderr=%q, want JSON usage", args, stdout.String(), stderr.String())
				}
				return
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "[usage]") {
				t.Fatalf("Execute(%q) stdout=%q stderr=%q, want human usage", args, stdout.String(), stderr.String())
			}
		})
	}
}

func TestExecuteNormalizesExplicitHelpBoolValues(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--help=true", "garbage"},
		{"--help=1", "garbage"},
		{"--help=t", "garbage"},
		{"--help=TRUE", "garbage"},
		{"-h=true", "garbage"},
		{"-h=1", "garbage"},
		{"--json", "--help=1", "garbage"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(context.Background(), NewRoot(), args, Streams{Out: &stdout, ErrOut: &stderr})
			if exitCode != int(ExitUsage) {
				t.Fatalf("Execute(%q) exit code = %d, want %d", args, exitCode, ExitUsage)
			}
			if args[0] == "--json" {
				if stderr.Len() != 0 || !strings.Contains(stdout.String(), `"code": "usage"`) {
					t.Fatalf("Execute(%q) stdout=%q stderr=%q, want JSON usage", args, stdout.String(), stderr.String())
				}
				return
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "unexpected argument") {
				t.Fatalf("Execute(%q) stdout=%q stderr=%q, want human help-argument usage", args, stdout.String(), stderr.String())
			}
		})
	}

	for _, args := range [][]string{
		{"--help=false", "garbage"},
		{"--help=0", "garbage"},
		{"-h=false", "garbage"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(context.Background(), NewRoot(), args, Streams{Out: &stdout, ErrOut: &stderr})
			if exitCode != int(ExitUsage) {
				t.Fatalf("Execute(%q) exit code = %d, want %d", args, exitCode, ExitUsage)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "unknown command") {
				t.Fatalf("Execute(%q) stdout=%q stderr=%q, want non-help positional usage", args, stdout.String(), stderr.String())
			}
		})
	}
}

func TestOutputModeIsAvailableToInjectedHandlers(t *testing.T) {
	t.Parallel()

	root := NewRoot()
	var got OutputMode
	root.AddCommand(&cobra.Command{
		Use:  "probe",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			got = OutputModeFrom(command)
			return nil
		},
	})
	exitCode := Execute(context.Background(), root, []string{"--json", "probe"}, Streams{})
	if exitCode != int(ExitSuccess) {
		t.Fatalf("exit code = %d, want %d", exitCode, ExitSuccess)
	}
	if got != ModeJSON {
		t.Fatalf("mode = %q, want %q", got, ModeJSON)
	}
}

func TestExecuteJSONFailureUsesStdoutAndStableUsageExit(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(context.Background(), NewRoot(), []string{"--json", "unknown-token"}, Streams{
		Out:    &stdout,
		ErrOut: &stderr,
	})

	if exitCode != int(ExitUsage) {
		t.Fatalf("exit code = %d, want %d", exitCode, ExitUsage)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := "{\n  \"schemaVersion\": 1,\n  \"kind\": \"error\",\n  \"error\": {\n    \"code\": \"usage\",\n    \"message\": \"unknown command \\\"unknown-token\\\" for \\\"camp\\\"\"\n  }\n}\n"
	if stdout.String() != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", stdout.String(), want)
	}
}

func TestExecuteRootMapsTypedAndUnexpectedFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		err      error
		wantExit ExitCode
		wantCode string
	}{
		{
			name: "typed",
			err: &ExitError{
				Code:    ExitCode(17),
				Failure: presentation.Failure{Code: "lease_conflict", Message: "lease is held"},
			},
			wantExit: ExitCode(17),
			wantCode: "lease_conflict",
		},
		{
			name:     "typed zero cannot report success",
			err:      &ExitError{Failure: presentation.Failure{Code: "invalid_exit", Message: "failed without an exit"}},
			wantExit: ExitFailure,
			wantCode: "invalid_exit",
		},
		{name: "unexpected", err: errors.New("boom"), wantExit: ExitFailure, wantCode: "command_failed"},
		{name: "application requires message", err: errors.New("session requires recovery before entry"), wantExit: ExitFailure, wantCode: "command_failed"},
		{name: "application accepts message", err: errors.New("handler accepts delayed results"), wantExit: ExitFailure, wantCode: "command_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			exitCode := ExecuteRoot(context.Background(), stubRootExecutor{err: test.err}, Streams{ErrOut: &stderr})
			if exitCode != int(test.wantExit) {
				t.Fatalf("exit code = %d, want %d", exitCode, test.wantExit)
			}
			if !strings.Contains(stderr.String(), fmt.Sprintf("[%s]", test.wantCode)) {
				t.Fatalf("stderr = %q, want code %q", stderr.String(), test.wantCode)
			}
		})
	}
}

func TestRootHelpIsDeterministic(t *testing.T) {
	t.Parallel()

	first := renderHelp(t)
	second := renderHelp(t)
	if first != second {
		t.Fatalf("help changed between identical roots:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	want := "Recoverable capsule workspaces\n\nUsage:\n  camp [flags]\n  camp [command]\n\nAvailable Commands:\n  attach      Attach to an open capsule workspace\n  close       Publish a checkpoint and close\n  completion  Generate shell completion\n  doctor      Diagnose required host capabilities\n  help        Help about any command\n  images      Inspect and reconcile workspace images\n  init        Initialize a capsule root\n  kit         Inspect and verify CampKit archives\n  list        List stored camps\n  open        Open a capsule workspace\n  provider    Inspect configured DevPod providers\n  recover     Recover an interrupted lifecycle\n  reopen      Reopen a closed capsule workspace\n  serve       Inspect and restart Camp-managed services\n  setup       Install or reuse pinned DevPod and Hauler tools\n  status      Show the selected camp session\n  strike      Archive local Camp state and start fresh\n  sync        Publish a checkpoint and remain open\n\nFlags:\n  -h, --help   help for camp\n      --json   emit stable JSON output\n\nUse \"camp [command] --help\" for more information about a command.\n"
	want = strings.Replace(want, "  doctor", "  config      Inspect and update Camp configuration"+"\n"+"  doctor", 1)
	if first != want {
		t.Fatalf("help:\n%s\nwant:\n%s", first, want)
	}
}

func TestAttachMapsTypedSSHFlagsAliasAndRawArguments(t *testing.T) {
	t.Parallel()
	lifecycle := &recordingLifecycle{}
	args := []string{
		"attach", "Memory D", "--insiders", "--user", "coder",
		"-L", "127.0.0.1:3000:127.0.0.1:3000", "-R", "127.0.0.1:5000:127.0.0.1:5000",
		"--send-env", "TERM", "--set-env", "CAMP_CHECKPOINT=42", "--agent-forwarding=false",
		"--gpg-agent-forwarding=true", "--term-mode", "strict", "--devpod-arg=--log-output", "--", "plain",
	}
	code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), args, Streams{})
	if code != int(ExitSuccess) {
		t.Fatalf("Execute(attach) code = %d, want %d", code, ExitSuccess)
	}
	if len(lifecycle.attachRequests) != 1 {
		t.Fatalf("attach requests = %#v", lifecycle.attachRequests)
	}
	got := lifecycle.attachRequests[0]
	if got.Target != "Memory D" || got.IDE != "vscode-insiders" || got.User != "coder" || got.AgentForwarding == nil || *got.AgentForwarding || got.GPGAgentForwarding == nil || !*got.GPGAgentForwarding || got.TermMode != "strict" {
		t.Fatalf("attach request = %#v", got)
	}
	if strings.Join(got.DevPodArgs, ",") != "--log-output,plain" {
		t.Fatalf("DevPod args = %#v", got.DevPodArgs)
	}
}

func TestAttachRejectsInsidersAndDifferentIDEWithoutEffects(t *testing.T) {
	t.Parallel()
	lifecycle := &recordingLifecycle{}
	var stderr bytes.Buffer
	code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"attach", "--insiders", "--ide", "vscode"}, Streams{ErrOut: &stderr})
	if code != int(ExitUsage) || len(lifecycle.attachRequests) != 0 || !strings.Contains(stderr.String(), "--insiders") {
		t.Fatalf("code=%d requests=%#v stderr=%q", code, lifecycle.attachRequests, stderr.String())
	}
}

func TestInitHelpTruthfullyListsCampManifestFlags(t *testing.T) {
	t.Parallel()
	root := NewRootWithLifecycle(&recordingLifecycle{})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"init", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "Initialize a capsule root\n\nUsage:\n  camp init [root] [flags]\n\nFlags:\n      --backend string              camp backend URL (defaults to machine setup)\n  -h, --help                        help for init\n      --migrate                     migrate the legacy singleton configuration\n      --name string                 stable camp ID\n      --workspace-context string    workspace runtime context (defaults to machine setup)\n      --workspace-provider string   workspace runtime provider (defaults to machine setup)\n\nGlobal Flags:\n      --json   emit stable JSON output\n"
	if output.String() != want {
		t.Fatalf("init help:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestHiddenSupervisorCommandIsPresentButNotVisible(t *testing.T) {
	t.Parallel()

	root := NewRoot()
	cmd, _, err := root.Find([]string{"supervise"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || !cmd.Hidden {
		t.Fatalf("supervise command = %#v, want hidden", cmd)
	}
	if help := renderHelp(t); strings.Contains(help, "supervise") {
		t.Fatalf("help unexpectedly mentioned supervise:\n%s", help)
	}

	lifecycle := &recordingLifecycle{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"supervise", "session-1"}, Streams{Out: &stdout, ErrOut: &stderr})
	if exitCode != int(ExitSuccess) {
		t.Fatalf("Execute(supervise) exit code = %d, want %d; stderr=%q", exitCode, ExitSuccess, stderr.String())
	}
	if got := lifecycle.calls[len(lifecycle.calls)-1]; got != "supervise:session-1:human" {
		t.Fatalf("call = %q, want supervise:session-1:human", got)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want empty", stdout.String(), stderr.String())
	}
}

func TestCompletionGenerationIsDeterministic(t *testing.T) {
	t.Parallel()

	for _, shell := range []Shell{ShellBash, ShellZsh, ShellFish} {
		t.Run(string(shell), func(t *testing.T) {
			t.Parallel()
			var first bytes.Buffer
			var second bytes.Buffer
			if err := GenerateCompletion(NewRoot(), shell, &first); err != nil {
				t.Fatalf("first generation: %v", err)
			}
			if err := GenerateCompletion(NewRoot(), shell, &second); err != nil {
				t.Fatalf("second generation: %v", err)
			}
			if first.String() != second.String() {
				t.Fatal("completion changed between identical roots")
			}
			if !strings.Contains(first.String(), "camp") {
				t.Fatalf("completion for %s does not name camp", shell)
			}
		})
	}
}

func renderHelp(t *testing.T) string {
	t.Helper()
	root := NewRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("render help: %v", err)
	}
	return output.String()
}

type stubRootExecutor struct{ err error }

func (s stubRootExecutor) ExecuteContext(context.Context) error { return s.err }
