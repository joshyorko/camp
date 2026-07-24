// Command scenerun runs the real full-screen setup program (setupui.Run — the
// production model, compositor, and terminal lifecycle) against a scripted
// development pipeline, so PTY captures and manual review exercise the exact
// code path `camp setup` uses without touching a real machine.
//
// It is a development harness only: the events it emits are fixed sample data
// (the same facts SampleFrame uses), it prints a "scenerun" exit line so its
// output can never be mistaken for a real provisioning run, and the production
// CLI never links this package. The production adapter (richSetupPipeline) is
// exercised separately by the CLI tests.
//
// Usage:
//
//	go run ./internal/setupui/scenerun -mode ready|failure|hold [-step 350ms]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/joshyorko/camp/internal/setupui"
)

func main() {
	mode := flag.String("mode", "ready", "ready|failure|hold (hold pauses at RUNTIME active until ctrl+c)")
	step := flag.Duration("step", 350*time.Millisecond, "pause between scripted events")
	flag.Parse()

	sprites, err := setupui.LoadSprites()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defaults := map[string]string{
		"source":   "~/SecondBrain",
		"capsule":  "SecondBrain",
		"backend":  "file://…",
		"provider": "docker",
		"context":  "default",
	}
	result, err := setupui.Run(context.Background(), os.Stdin, os.Stdout,
		setupui.DefaultPalette(), sprites, defaults, scriptedPipeline{mode: *mode, step: *step})
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenerun: %v\n", err)
		os.Exit(1)
	}
	switch {
	case result.Canceled:
		fmt.Println("scenerun exit: canceled (terminal restored)")
		os.Exit(130)
	case result.Failed:
		fmt.Printf("scenerun exit: failed (%s) — next: %s\n", result.FailMsg, result.Recovery)
		os.Exit(1)
	default:
		fmt.Println("scenerun exit: done (terminal restored)")
	}
}

// scriptedPipeline satisfies setupui.Pipeline with deterministic sample events.
type scriptedPipeline struct {
	mode string
	step time.Duration
}

func (p scriptedPipeline) Start(values map[string]string) <-chan tea.Msg {
	out := make(chan tea.Msg, 8)
	go func() {
		defer close(out)
		emit := func(m tea.Msg) {
			time.Sleep(p.step)
			out <- m
		}
		out <- setupui.ConfigAcceptedMsg{
			Waypoints: [4]setupui.Waypoint{
				{Label: "TOOLCHAIN", Landmark: "crate", Meta: []string{"DevPod v0.26.1", "Hauler v2.0.2"}},
				{Label: "RUNTIME", Landmark: "helm", Meta: []string{values["provider"] + " · docker", "context " + values["context"]}},
				{Label: "CAPSULE", Landmark: "tent", Meta: []string{values["capsule"], values["source"]}},
				{Label: "STORAGE", Landmark: "campfire", Meta: []string{"file backend", "generation ready"}},
			},
			NextCmd:   "camp open " + values["source"],
			ReadyLine: values["capsule"] + " · " + values["provider"] + " · file backend",
		}
		emit(setupui.WaypointCompletedMsg{Stage: setupui.StageToolchain, Meta: []string{"DevPod v0.26.1", "Hauler v2.0.2"}})
		switch p.mode {
		case "hold":
			// Stay at RUNTIME active until the process is canceled.
			select {}
		case "failure":
			emit(setupui.WaypointFailedMsg{
				Stage:    setupui.StageRuntime,
				Message:  "devpod provider docker is unreachable",
				Recovery: "camp setup",
			})
		default:
			emit(setupui.WaypointCompletedMsg{Stage: setupui.StageRuntime})
			emit(setupui.WaypointCompletedMsg{Stage: setupui.StageCapsule})
			emit(setupui.WaypointCompletedMsg{Stage: setupui.StageStorage})
			emit(setupui.AllReadyMsg{})
		}
	}()
	return out
}
