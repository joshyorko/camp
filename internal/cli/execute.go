package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/presentation"
	"github.com/spf13/cobra"
)

type ExitCode int

const (
	ExitSuccess ExitCode = 0
	ExitFailure ExitCode = 1
	ExitUsage   ExitCode = 2
)

type Streams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
	Mode   OutputMode
}

type OutputMode string

const (
	ModeHuman OutputMode = "human"
	ModeJSON  OutputMode = "json"
)

func OutputModeFrom(command *cobra.Command) OutputMode {
	jsonMode, err := command.Flags().GetBool("json")
	if err == nil && jsonMode {
		return ModeJSON
	}
	return ModeHuman
}

type ExitError struct {
	Code    ExitCode
	Failure presentation.Failure
	Cause   error
}

func (e *ExitError) Error() string {
	if e.Failure.Message != "" {
		return e.Failure.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "camp command failed"
}

func (e *ExitError) Unwrap() error { return e.Cause }

func UsageError(err error) *ExitError {
	return &ExitError{
		Code: ExitUsage,
		Failure: presentation.Failure{
			Code:    "usage",
			Message: err.Error(),
		},
		Cause: err,
	}
}

func lifecycleFailure(err error, recoveryCommand string) *ExitError {
	next := []string(nil)
	if recoveryCommand != "" {
		next = []string{recoveryCommand}
	}
	code := "lifecycle_failed"
	if errors.Is(err, ports.ErrAmbiguous) {
		code = "lifecycle_ambiguous"
	}
	return &ExitError{Code: ExitFailure, Failure: presentation.Failure{Code: code, Message: err.Error(), NextCommands: next}, Cause: err}
}

func renderedLifecycleFailure(err error) *ExitError {
	return &ExitError{Code: ExitFailure, Cause: err}
}

type RootExecutor interface {
	ExecuteContext(context.Context) error
}

func Execute(ctx context.Context, root *cobra.Command, args []string, streams Streams) int {
	if streams.In != nil {
		root.SetIn(streams.In)
	}
	if streams.Out != nil {
		root.SetOut(streams.Out)
	}
	if streams.ErrOut != nil {
		root.SetErr(streams.ErrOut)
	}
	root.SetArgs(args)
	err := validateHelpArguments(root, args)
	if err == nil {
		err = root.ExecuteContext(ctx)
	} else if preflightJSONMode(args) {
		streams.Mode = ModeJSON
	}
	if jsonMode, flagErr := root.Flags().GetBool("json"); flagErr == nil && jsonMode {
		streams.Mode = ModeJSON
	}
	return finishExecution(err, streams)
}

func validateHelpArguments(root *cobra.Command, args []string) error {
	if hasInvalidJSONFlag(args) {
		return nil
	}
	if argument, ok := argumentAfterRootHelp(args); ok {
		return UsageError(fmt.Errorf("unexpected argument %q after help flag", argument))
	}

	filtered := helpArguments(args)
	if len(filtered) > 0 && filtered[0] == "help" {
		path := filtered[1:]
		if len(path) == 0 {
			return nil
		}
		_, remaining, err := root.Find(path)
		if err != nil {
			return UsageError(err)
		}
		if len(remaining) != 0 {
			return UsageError(fmt.Errorf("unknown help topic %q", strings.Join(path, " ")))
		}
		return nil
	}

	return nil
}

func hasInvalidJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if strings.HasPrefix(arg, "--json=") {
			if _, err := strconv.ParseBool(strings.TrimPrefix(arg, "--json=")); err != nil {
				return true
			}
		}
	}
	return false
}

func preflightJSONMode(args []string) bool {
	jsonMode := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		switch {
		case arg == "--json":
			jsonMode = true
		case strings.HasPrefix(arg, "--json="):
			value, err := strconv.ParseBool(strings.TrimPrefix(arg, "--json="))
			if err == nil {
				jsonMode = value
			}
		}
	}
	return jsonMode
}

func argumentAfterRootHelp(args []string) (string, bool) {
	beforeCommand := true
	rootHelp := false
	afterTerminator := false
	for _, arg := range args {
		if afterTerminator {
			if rootHelp {
				return arg, true
			}
			beforeCommand = false
			continue
		}
		if arg == "--" {
			afterTerminator = true
			continue
		}
		if isValidJSONFlag(arg) {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if beforeCommand {
				if help, ok := explicitHelpFlagValue(arg); ok {
					rootHelp = help
				}
			}
			continue
		}
		if rootHelp {
			return arg, true
		}
		beforeCommand = false
	}
	return "", false
}

func explicitHelpFlagValue(arg string) (bool, bool) {
	if arg == "--help" || arg == "-h" {
		return true, true
	}
	for _, prefix := range []string{"--help=", "-h="} {
		if strings.HasPrefix(arg, prefix) {
			value, err := strconv.ParseBool(strings.TrimPrefix(arg, prefix))
			return value, err == nil
		}
	}
	return false, false
}

func isValidJSONFlag(arg string) bool {
	if arg == "--json" {
		return true
	}
	if !strings.HasPrefix(arg, "--json=") {
		return false
	}
	_, err := strconv.ParseBool(strings.TrimPrefix(arg, "--json="))
	return err == nil
}

func helpArguments(args []string) []string {
	filtered := make([]string, 0, len(args))
	afterTerminator := false
	for _, arg := range args {
		if arg == "--" {
			afterTerminator = true
			continue
		}
		if !afterTerminator {
			_, validHelp := explicitHelpFlagValue(arg)
			if isValidJSONFlag(arg) || validHelp {
				continue
			}
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func ExecuteRoot(ctx context.Context, root RootExecutor, streams Streams) int {
	return finishExecution(root.ExecuteContext(ctx), streams)
}

func finishExecution(err error, streams Streams) int {
	if err == nil {
		return int(ExitSuccess)
	}

	exitErr := classifyError(err)
	if exitErr.Failure.Code != "" || exitErr.Failure.Message != "" || len(exitErr.Failure.NextCommands) != 0 {
		writeFailure(streams, exitErr.Failure)
	}
	return int(exitErr.Code)
}

func classifyError(err error) *ExitError {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		normalized := *exitErr
		if normalized.Code == ExitSuccess {
			normalized.Code = ExitFailure
		}
		return &normalized
	}
	return &ExitError{
		Code: ExitFailure,
		Failure: presentation.Failure{
			Code:    "command_failed",
			Message: err.Error(),
		},
		Cause: err,
	}
}

func writeFailure(streams Streams, failure presentation.Failure) {
	if streams.Mode == ModeJSON {
		if streams.Out == nil {
			return
		}
		body, err := presentation.MarshalFailureJSON(failure)
		if err != nil {
			return
		}
		_, _ = streams.Out.Write(body)
		return
	}
	if streams.ErrOut == nil {
		return
	}
	_, _ = fmt.Fprint(streams.ErrOut, presentation.RenderFailureHuman(failure))
}
