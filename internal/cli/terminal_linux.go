//go:build linux

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joshyorko/camp/internal/presentation"
	"golang.org/x/sys/unix"
)

type terminalProbe func(uintptr) (bool, int)

func resolveTerminalExperience(mode OutputMode, out io.Writer, environment map[string]string, probe terminalProbe) presentation.TerminalExperience {
	file, ok := out.(*os.File)
	if !ok {
		return presentation.TerminalPlain
	}
	tty, width := probe(file.Fd())
	_, noColor := environment["NO_COLOR"]
	ci := strings.TrimSpace(environment["CI"])
	return presentation.SelectTerminalExperience(presentation.TerminalInput{
		TTY: tty, Width: width, TERM: environment["TERM"], COLORTERM: environment["COLORTERM"],
		JSON: mode == ModeJSON, NoColor: noColor, CI: ci != "" && !strings.EqualFold(ci, "false") && ci != "0",
	})
}

func probeTerminal(fd uintptr) (bool, int) {
	winsize, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil || winsize.Col == 0 {
		return false, 0
	}
	return true, int(winsize.Col)
}

func writeLifecycleEvents(out io.Writer, experience presentation.TerminalExperience, operation string, events ...presentation.LifecycleEvent) error {
	session := presentation.NewTerminalSession(out, experience, operation)
	for _, event := range events {
		if err := session.Emit(context.Background(), event); err != nil {
			return err
		}
	}
	return nil
}

func writeHumanLifecycleResult(out io.Writer, mode OutputMode, operation string, events []presentation.LifecycleEvent, legacy string) error {
	if mode != ModeHuman {
		return nil
	}
	experience := resolveTerminalExperience(mode, out, environmentMap(os.Environ()), probeTerminal)
	if len(events) == 0 {
		_, err := io.WriteString(out, legacy)
		return err
	}
	return writeLifecycleEvents(out, experience, operation, events...)
}

func openTerminalEvents(capsule, sessionID string) []presentation.LifecycleEvent {
	return []presentation.LifecycleEvent{
		{Stage: presentation.StageWorkspaceReady, Message: fmt.Sprintf("workspace %s is ready", capsule)},
		{Stage: presentation.StageComplete, Message: fmt.Sprintf("opened %s (%s)", capsule, sessionID)},
	}
}

func syncTerminalEvents(generation uint64) []presentation.LifecycleEvent {
	return []presentation.LifecycleEvent{
		{Stage: presentation.StageGenerationPublished, Message: fmt.Sprintf("published generation %d", generation)},
		{Stage: presentation.StageComplete, Message: "sync complete"},
	}
}

func closeTerminalEvents(generation uint64, cleanupSucceeded bool) []presentation.LifecycleEvent {
	events := []presentation.LifecycleEvent{{Stage: presentation.StageGenerationPublished, Message: fmt.Sprintf("published generation %d", generation)}}
	if cleanupSucceeded {
		events = append(events, presentation.LifecycleEvent{Stage: presentation.StageCleanupComplete, Message: "cleanup complete"})
	}
	return append(events, presentation.LifecycleEvent{Stage: presentation.StageComplete, Message: "session closed"})
}
