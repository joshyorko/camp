package cli

import (
	"context"
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
	Sync(context.Context, OutputMode, io.Writer) error
	Close(context.Context, OutputMode, io.Writer) error
	Reopen(context.Context, string, OutputMode, io.Writer) error
	Recover(context.Context, string, OutputMode, io.Writer) error
	Supervise(context.Context, string, OutputMode, io.Writer) error
}

type InitRequest struct {
	Root           string
	Source         string
	Backend        string
	Capsule        string
	DevPodProvider string
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
	root.AddCommand(
		newInitCommand(lifecycle.Init),
		optionalArgumentCommand("open", "Open a capsule workspace", lifecycle.Open),
		noArgumentCommand("sync", "Publish a checkpoint and remain open", lifecycle.Sync),
		noArgumentCommand("close", "Publish a checkpoint and close", lifecycle.Close),
		optionalArgumentCommand("reopen", "Reopen a closed capsule workspace", lifecycle.Reopen),
		optionalArgumentCommand("recover", "Recover an interrupted lifecycle", lifecycle.Recover),
		hiddenRequiredArgumentCommand("supervise", lifecycle.Supervise),
	)
	return root
}

func newInitCommand(run func(context.Context, InitRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := InitRequest{}
	command := &cobra.Command{
		Use: "init [root]", Short: "Initialize a capsule root", Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				request.Root = args[0]
			}
			configured := 0
			for _, name := range []string{"source", "backend", "capsule", "devpod-provider"} {
				if command.Flags().Changed(name) {
					configured++
				}
			}
			if configured != 0 && configured != 4 {
				return UsageError(fmt.Errorf("--source, --backend, --capsule, and --devpod-provider must be provided together"))
			}
			if configured == 4 {
				if request.Root != "" {
					return UsageError(fmt.Errorf("init root and --source cannot be used together"))
				}
				if request.Source == "" || request.Backend == "" || request.Capsule == "" || request.DevPodProvider == "" {
					return UsageError(fmt.Errorf("persistent init values cannot be empty"))
				}
			}
			return run(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&request.Source, "source", "", "persist the default source path")
	command.Flags().StringVar(&request.Backend, "backend", "", "persist the default backend URL")
	command.Flags().StringVar(&request.Capsule, "capsule", "", "persist the default capsule name")
	command.Flags().StringVar(&request.DevPodProvider, "devpod-provider", "", "persist the default DevPod provider")
	return command
}

func optionalArgumentCommand(use, short string, run func(context.Context, string, OutputMode, io.Writer) error) *cobra.Command {
	return &cobra.Command{
		Use: use + " [target]", Short: short, Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			value := ""
			if len(args) == 1 {
				value = args[0]
			}
			return run(command.Context(), value, OutputModeFrom(command), command.OutOrStdout())
		},
	}
}

func noArgumentCommand(use, short string, run func(context.Context, OutputMode, io.Writer) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: usageArgs(cobra.NoArgs), RunE: func(command *cobra.Command, _ []string) error {
		return run(command.Context(), OutputModeFrom(command), command.OutOrStdout())
	}}
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
