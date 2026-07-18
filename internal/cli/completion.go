package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

type Shell string

const (
	ShellBash Shell = "bash"
	ShellZsh  Shell = "zsh"
	ShellFish Shell = "fish"
)

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	command := &cobra.Command{
		Use:       "completion [bash|zsh|fish]",
		Short:     "Generate shell completion",
		Args:      exactArgs(1),
		ValidArgs: []string{string(ShellBash), string(ShellZsh), string(ShellFish)},
		RunE: func(command *cobra.Command, args []string) error {
			return GenerateCompletion(root, Shell(args[0]), command.OutOrStdout())
		},
	}
	command.Flags().SetInterspersed(false)
	return command
}

func GenerateCompletion(root *cobra.Command, shell Shell, output io.Writer) error {
	switch shell {
	case ShellBash:
		return root.GenBashCompletionV2(output, true)
	case ShellZsh:
		return root.GenZshCompletion(output)
	case ShellFish:
		return root.GenFishCompletion(output, true)
	default:
		return UsageError(fmt.Errorf("unsupported completion shell %q", shell))
	}
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if len(args) != count {
			return UsageError(fmt.Errorf("%s accepts %d arg(s), received %d", command.CommandPath(), count, len(args)))
		}
		return nil
	}
}
