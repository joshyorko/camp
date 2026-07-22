package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

func TestLifecycleCommandsDelegateWithStrictArgumentsAndInheritedMode(t *testing.T) {
	t.Parallel()

	lifecycle := &recordingLifecycle{}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "init", args: []string{"--json", "init", "/brain"}, want: "init:/brain:json"},
		{name: "setup", args: []string{"setup"}, want: "setup::human"},
		{name: "open", args: []string{"open", "memoryd"}, want: "open:memoryd:human"},
		{name: "sync", args: []string{"sync"}, want: "sync::human"},
		{name: "close", args: []string{"close"}, want: "close::human"},
		{name: "reopen", args: []string{"reopen", "memoryd"}, want: "reopen:memoryd:human"},
		{name: "recover", args: []string{"recover", "session-1"}, want: "recover:session-1:human"},
		{name: "supervise", args: []string{"supervise", "session-1"}, want: "supervise:session-1:human"},
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

	for _, args := range [][]string{{"init", "a", "b"}, {"open", "a", "b"}, {"sync", "x"}, {"close", "x"}, {"reopen", "a", "b"}, {"recover", "a", "b"}, {"supervise", "a", "b"}} {
		var stderr bytes.Buffer
		if code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), args, Streams{ErrOut: &stderr}); code != int(ExitUsage) {
			t.Fatalf("Execute(%q) = %d, want usage; stderr=%q", args, code, stderr.String())
		}
	}
}

type recordingLifecycle struct{ calls []string }

func (r *recordingLifecycle) Init(_ context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "init:"+value+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Setup(_ context.Context, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "setup::"+string(mode))
	return nil
}
func (r *recordingLifecycle) Open(_ context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "open:"+value+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Sync(_ context.Context, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "sync::"+string(mode))
	return nil
}
func (r *recordingLifecycle) Close(_ context.Context, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "close::"+string(mode))
	return nil
}
func (r *recordingLifecycle) Reopen(_ context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "reopen:"+value+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Recover(_ context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "recover:"+value+":"+string(mode))
	return nil
}
func (r *recordingLifecycle) Supervise(_ context.Context, value string, mode OutputMode, _ io.Writer) error {
	r.calls = append(r.calls, "supervise:"+value+":"+string(mode))
	return nil
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
	want := "Recoverable capsule workspaces\n\nUsage:\n  camp [flags]\n  camp [command]\n\nAvailable Commands:\n  close       Publish a checkpoint and close\n  completion  Generate shell completion\n  help        Help about any command\n  init        Initialize a capsule root\n  open        Open a capsule workspace\n  recover     Recover an interrupted lifecycle\n  reopen      Reopen a closed capsule workspace\n  sync        Publish a checkpoint and remain open\n\nFlags:\n  -h, --help   help for camp\n      --json   emit stable JSON output\n\nUse \"camp [command] --help\" for more information about a command.\n"
	if first != want {
		t.Fatalf("help:\n%s\nwant:\n%s", first, want)
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
