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
	Init(context.Context, string, OutputMode, io.Writer) error
	Open(context.Context, string, OutputMode, io.Writer) error
	Sync(context.Context, OutputMode, io.Writer) error
	Close(context.Context, OutputMode, io.Writer) error
	Reopen(context.Context, string, OutputMode, io.Writer) error
	Recover(context.Context, string, OutputMode, io.Writer) error
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
		optionalArgumentCommand("init", "Initialize a capsule root", lifecycle.Init),
		optionalArgumentCommand("open", "Open a capsule workspace", lifecycle.Open),
		noArgumentCommand("sync", "Publish a checkpoint and remain open", lifecycle.Sync),
		noArgumentCommand("close", "Publish a checkpoint and close", lifecycle.Close),
		optionalArgumentCommand("reopen", "Reopen a closed capsule workspace", lifecycle.Reopen),
		optionalArgumentCommand("recover", "Recover an interrupted lifecycle", lifecycle.Recover),
	)
	return root
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

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if err := validate(command, args); err != nil {
			return UsageError(err)
		}
		return nil
	}
}
