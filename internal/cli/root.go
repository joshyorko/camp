package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// NewRoot constructs Camp's production command root. Lifecycle commands are
// registered only when their production dependencies are available.
func NewRoot() *cobra.Command {
	return NewRootWithLifecycle(NewProductionLifecycle())
}

type Lifecycle interface {
	Init(context.Context, InitRequest, OutputMode, io.Writer) error
	Open(context.Context, string, OutputMode, io.Writer) error
	Attach(context.Context, AttachRequest, OutputMode, io.Writer) error
	Sync(context.Context, OutputMode, io.Writer) error
	Close(context.Context, CloseRequest, OutputMode, io.Writer) error
	Reopen(context.Context, string, OutputMode, io.Writer) error
	Recover(context.Context, string, OutputMode, io.Writer) error
	Supervise(context.Context, string, OutputMode, io.Writer) error
}

type AttachRequest struct {
	Target               string
	IDE                  string
	User                 string
	ForwardPorts         []string
	ReverseForwardPorts  []string
	SendEnv              []string
	SetEnv               []string
	ForwardPortsTimeout  string
	AgentForwarding      *bool
	GPGAgentForwarding   *bool
	Stdio                *bool
	SSHKeepAliveInterval string
	GitSSHSigningKey     string
	TermMode             string
	InstallTerminfo      *bool
	DevPodArgs           []string
}

type InitRequest struct {
	Root           string
	Source         string
	Backend        string
	Capsule        string
	DevPodProvider string
	DevPodContext  string
	Migrate        bool
}

type CloseRequest struct {
	Discard bool
}

type Selection struct {
	Camp    string
	Session string
}

type selectionContextKey struct{}

func SelectionFromContext(ctx context.Context) Selection {
	selection, _ := ctx.Value(selectionContextKey{}).(Selection)
	return selection
}

func withSelection(ctx context.Context, selection Selection) context.Context {
	return context.WithValue(ctx, selectionContextKey{}, selection)
}

type doctorLifecycle interface {
	Doctor(context.Context, OutputMode, io.Writer) error
}

type Setup interface {
	Setup(context.Context, OutputMode, io.Reader, io.Writer) error
}

type CampLister interface {
	List(context.Context, OutputMode, io.Writer) error
}

type CampStatus interface {
	Status(context.Context, OutputMode, io.Writer) error
}

type StrikeRequest struct {
	Purge bool
	Yes   bool
}

type CampStriker interface {
	Strike(context.Context, StrikeRequest, OutputMode, io.Writer) error
}

func NewRootWithLifecycle(lifecycle Lifecycle) *cobra.Command {
	root := &cobra.Command{
		Use:           "camp",
		Short:         "Recoverable capsule workspaces",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			return UsageError(fmt.Errorf("unknown command %q for %q", args[0], "camp"))
		},
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	root.PersistentFlags().Bool("json", false, "emit stable JSON output")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return UsageError(err)
	})
	root.DisableAutoGenTag = true
	root.AddCommand(newCompletionCommand(root))
	if diagnostics, ok := lifecycle.(doctorLifecycle); ok {
		root.AddCommand(noArgumentCommand("doctor", "Diagnose required host capabilities", diagnostics.Doctor))
	}
	if setup, ok := lifecycle.(Setup); ok {
		root.AddCommand(setupCommand(setup.Setup))
	}
	if camps, ok := lifecycle.(CampLister); ok {
		root.AddCommand(noArgumentCommand("list", "List stored camps", camps.List))
	}
	if status, ok := lifecycle.(CampStatus); ok {
		root.AddCommand(selectionCommand("status", "Show the selected camp session", status.Status))
	}
	if striker, ok := lifecycle.(CampStriker); ok {
		root.AddCommand(newStrikeCommand(striker.Strike))
	}
	root.AddCommand(
		newInitCommand(lifecycle.Init),
		optionalArgumentCommand("open", "Open a capsule workspace", lifecycle.Open),
		newAttachCommand(lifecycle.Attach),
		noArgumentCommand("sync", "Publish a checkpoint and remain open", lifecycle.Sync),
		newCloseCommand(lifecycle.Close),
		optionalArgumentCommand("reopen", "Reopen a closed capsule workspace", lifecycle.Reopen),
		optionalArgumentCommand("recover", "Recover an interrupted lifecycle", lifecycle.Recover),
		hiddenRequiredArgumentCommand("supervise", lifecycle.Supervise),
	)
	return root
}

func newStrikeCommand(run func(context.Context, StrikeRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := StrikeRequest{}
	command := &cobra.Command{
		Use: "strike", Short: "Archive local Camp state and start fresh", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().BoolVar(&request.Purge, "purge", false, "permanently remove verified local Camp state")
	command.Flags().BoolVar(&request.Yes, "yes", false, "confirm permanent purge")
	return command
}

func newCloseCommand(run func(context.Context, CloseRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := CloseRequest{}
	selection := Selection{}
	command := &cobra.Command{
		Use: "close", Short: "Publish a checkpoint and close", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(withSelection(command.Context(), selection), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().BoolVar(&request.Discard, "discard", false, "close without publishing the open session")
	addSelectionFlags(command, &selection)
	return command
}

func setupCommand(run func(context.Context, OutputMode, io.Reader, io.Writer) error) *cobra.Command {
	return &cobra.Command{
		Use: "setup", Short: "Install or reuse pinned DevPod and Hauler tools", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command.Context(), OutputModeFrom(command), command.InOrStdin(), command.OutOrStdout())
		},
	}
}

func newAttachCommand(run func(context.Context, AttachRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := AttachRequest{IDE: "none"}
	selection := Selection{}
	var insiders, agentForwarding, gpgAgentForwarding, stdio, installTerminfo bool
	command := &cobra.Command{
		Use: "attach [target]", Short: "Attach to an open capsule workspace",
		Args: func(command *cobra.Command, args []string) error {
			positional := args
			if index := command.ArgsLenAtDash(); index >= 0 {
				positional = args[:index]
			}
			return usageArgs(cobra.MaximumNArgs(1))(command, positional)
		},
		RunE: func(command *cobra.Command, args []string) error {
			positional := args
			if index := command.ArgsLenAtDash(); index >= 0 {
				positional = args[:index]
				request.DevPodArgs = append(request.DevPodArgs, args[index:]...)
			}
			if len(positional) == 1 {
				request.Target = positional[0]
			}
			if insiders {
				if command.Flags().Changed("ide") && request.IDE != "vscode-insiders" {
					return UsageError(errors.New("--insiders conflicts with --ide unless --ide=vscode-insiders"))
				}
				request.IDE = "vscode-insiders"
			}
			if command.Flags().Changed("agent-forwarding") {
				request.AgentForwarding = &agentForwarding
			}
			if command.Flags().Changed("gpg-agent-forwarding") {
				request.GPGAgentForwarding = &gpgAgentForwarding
			}
			if command.Flags().Changed("stdio") {
				request.Stdio = &stdio
			}
			if command.Flags().Changed("install-terminfo") {
				request.InstallTerminfo = &installTerminfo
			}
			return run(withSelection(command.Context(), selection), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	flags := command.Flags()
	flags.StringVar(&request.IDE, "ide", "none", "entry mode: none, vscode, vscode-insiders, or t3-code")
	flags.BoolVar(&insiders, "insiders", false, "alias for --ide=vscode-insiders")
	flags.StringVar(&request.User, "user", "", "SSH user")
	flags.StringSliceVarP(&request.ForwardPorts, "forward-ports", "L", nil, "forward a local port through DevPod SSH")
	flags.StringSliceVarP(&request.ReverseForwardPorts, "reverse-forward-ports", "R", nil, "reverse-forward a port through DevPod SSH")
	flags.StringSliceVar(&request.SendEnv, "send-env", nil, "send an environment variable through DevPod SSH")
	flags.StringSliceVar(&request.SetEnv, "set-env", nil, "set an environment variable in DevPod SSH")
	flags.StringVar(&request.ForwardPortsTimeout, "forward-ports-timeout", "", "DevPod forward-port timeout")
	flags.BoolVar(&agentForwarding, "agent-forwarding", false, "forward the SSH agent")
	flags.BoolVar(&gpgAgentForwarding, "gpg-agent-forwarding", false, "forward the GPG agent")
	flags.BoolVar(&stdio, "stdio", false, "attach SSH to standard I/O")
	flags.StringVar(&request.SSHKeepAliveInterval, "ssh-keepalive-interval", "", "SSH keepalive interval")
	flags.StringVar(&request.GitSSHSigningKey, "git-ssh-signing-key", "", "Git SSH signing key path")
	flags.StringVar(&request.TermMode, "term-mode", "", "terminal mode")
	flags.BoolVar(&installTerminfo, "install-terminfo", false, "install local terminal information")
	flags.StringSliceVar(&request.DevPodArgs, "devpod-arg", nil, "append one raw DevPod SSH argument")
	addSelectionFlags(command, &selection)
	return command
}

func newInitCommand(run func(context.Context, InitRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := InitRequest{}
	command := &cobra.Command{
		Use: "init [root]", Short: "Initialize a capsule root", Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				request.Root = args[0]
			}
			if request.Migrate {
				if request.Root != "" || request.Capsule != "" || command.Flags().Changed("backend") || command.Flags().Changed("workspace-provider") || command.Flags().Changed("workspace-context") {
					return UsageError(errors.New("--migrate cannot be combined with a root or camp settings"))
				}
			} else if request.Capsule == "" {
				return UsageError(errors.New("init requires --name"))
			}
			return run(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&request.Capsule, "name", "", "stable camp ID")
	command.Flags().StringVar(&request.Backend, "backend", "", "camp backend URL (defaults to machine setup)")
	command.Flags().StringVar(&request.DevPodProvider, "workspace-provider", "", "workspace runtime provider (defaults to machine setup)")
	command.Flags().StringVar(&request.DevPodContext, "workspace-context", "", "workspace runtime context (defaults to machine setup)")
	command.Flags().BoolVar(&request.Migrate, "migrate", false, "migrate the legacy singleton configuration")
	return command
}

func optionalArgumentCommand(use, short string, run func(context.Context, string, OutputMode, io.Writer) error) *cobra.Command {
	selection := Selection{}
	command := &cobra.Command{
		Use: use + " [target]", Short: short, Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			value := ""
			if len(args) == 1 {
				value = args[0]
			}
			if (use == "reopen" || use == "recover") && value != "" {
				if selection.Session != "" && selection.Session != value {
					return UsageError(errors.New("positional session conflicts with --session"))
				}
				selection.Session = value
			}
			return run(withSelection(command.Context(), selection), value, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSelectionFlags(command, &selection)
	return command
}

func noArgumentCommand(use, short string, run func(context.Context, OutputMode, io.Writer) error) *cobra.Command {
	selection := Selection{}
	command := &cobra.Command{Use: use, Short: short, Args: usageArgs(cobra.NoArgs), RunE: func(command *cobra.Command, _ []string) error {
		return run(withSelection(command.Context(), selection), OutputModeFrom(command), command.OutOrStdout())
	}}
	if use == "sync" {
		addSelectionFlags(command, &selection)
	}
	return command
}

func addSelectionFlags(command *cobra.Command, selection *Selection) {
	command.Flags().StringVar(&selection.Camp, "camp", "", "select a camp by stable ID")
	command.Flags().StringVar(&selection.Session, "session", "", "select a session by ID")
}

func selectionCommand(use, short string, run func(context.Context, OutputMode, io.Writer) error) *cobra.Command {
	selection := Selection{}
	command := &cobra.Command{
		Use: use, Short: short, Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(withSelection(command.Context(), selection), OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSelectionFlags(command, &selection)
	return command
}

func hiddenRequiredArgumentCommand(use string, run func(context.Context, string, OutputMode, io.Writer) error) *cobra.Command {
	return &cobra.Command{
		Use:    use + " [session]",
		Hidden: true,
		Args:   usageArgs(cobra.ExactArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			return run(command.Context(), args[0], OutputModeFrom(command), command.OutOrStdout())
		},
	}
}

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if err := validate(command, args); err != nil {
			return UsageError(err)
		}
		return nil
	}
}
