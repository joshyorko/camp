package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "camp",
		Short:         "Recoverable capsule workspaces",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
