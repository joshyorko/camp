package setupui

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"
)

// Result reports how the rich setup program exited so the caller can preserve
// the original error/recovery semantics after the alternate screen is torn
// down.
type Result struct {
	Canceled bool
	Failed   bool
	FailMsg  string
	Recovery string
}

// Run drives the full rich setup experience inside the alternate screen and
// returns once the user finishes, cancels, or provisioning fails. Bubble Tea
// restores the terminal (leaves the alt screen, shows the cursor) on any exit
// path — normal quit, error, or a panic inside the program — so the shell is
// never left dirty.
//
// The pipeline supplies real setup operations; this function contains no
// lifecycle logic. out/in are the program's TTY.
func Run(ctx context.Context, in io.Reader, out io.Writer, pal Palette, sprites map[string]Sprite, defaults map[string]string, pipeline Pipeline) (Result, error) {
	return RunWithExit(ctx, in, out, pal, sprites, defaults, pipeline, nil)
}

// RunWithExit is the same as Run with an additional user-exit callback. The
// model invokes it once when cancellation or terminal-state dismissal requests
// shutdown; callers still perform unconditional cleanup after RunWithExit
// returns to cover terminal initialization errors.
func RunWithExit(ctx context.Context, in io.Reader, out io.Writer, pal Palette, sprites map[string]Sprite, defaults map[string]string, pipeline Pipeline, onExit func()) (Result, error) {
	return runModel(ctx, in, out, NewModel(pal, sprites, defaults, pipeline).OnExit(onExit))
}

func RunWorkflowWithExit(ctx context.Context, in io.Reader, out io.Writer, pal Palette, sprites map[string]Sprite, defaults map[string]string, pipeline Pipeline, workflow Workflow, onExit func()) (Result, error) {
	return runModel(ctx, in, out, NewWorkflowModel(pal, sprites, defaults, pipeline, workflow).OnExit(onExit))
}

func runModel(ctx context.Context, in io.Reader, out io.Writer, model Model) (Result, error) {
	opts := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithOutput(out),
	}
	if in != nil {
		opts = append(opts, tea.WithInput(in))
	}
	prog := tea.NewProgram(model, opts...)
	final, err := prog.Run()
	if err != nil {
		return Result{}, err
	}
	fm, ok := final.(Model)
	if !ok {
		return Result{}, nil
	}
	failed, msg, recovery := fm.Failed()
	return Result{
		Canceled: fm.Canceled(),
		Failed:   failed,
		FailMsg:  msg,
		Recovery: recovery,
	}, nil
}
