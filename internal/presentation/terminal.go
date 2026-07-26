package presentation

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type TerminalExperience string

const (
	TerminalPlain TerminalExperience = "plain"
	TerminalColor TerminalExperience = "color"
)

type TerminalInput struct {
	TTY       bool
	Width     int
	TERM      string
	COLORTERM string
	JSON      bool
	CI        bool
}

func SelectTerminalExperience(input TerminalInput) TerminalExperience {
	trueColor := strings.EqualFold(input.COLORTERM, "truecolor") || strings.EqualFold(input.COLORTERM, "24bit")
	if input.JSON || input.CI || !input.TTY || input.Width < 80 || strings.EqualFold(input.TERM, "dumb") || !trueColor {
		return TerminalPlain
	}
	return TerminalColor
}

type LifecycleStage string

const (
	StageStarted             LifecycleStage = "started"
	StageToolReady           LifecycleStage = "tool-ready"
	StageWorkspaceReady      LifecycleStage = "workspace-ready"
	StageGenerationPublished LifecycleStage = "generation-published"
	StageCleanupComplete     LifecycleStage = "cleanup-complete"
	StageComplete            LifecycleStage = "complete"
	StageFailed              LifecycleStage = "failed"
)

type LifecycleEvent struct {
	Stage           LifecycleStage
	Message         string
	RecoveryCommand string
}

type TerminalSession struct {
	writer     io.Writer
	experience TerminalExperience
	operation  string
}

func NewTerminalSession(writer io.Writer, experience TerminalExperience, operation string) *TerminalSession {
	return &TerminalSession{writer: writer, experience: experience, operation: operation}
}

func (s *TerminalSession) Emit(ctx context.Context, event LifecycleEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for name, value := range map[string]string{"operation": s.operation, "message": event.Message, "recovery command": event.RecoveryCommand} {
		if value != "" && unsafeCampsiteValue(value) {
			return fmt.Errorf("terminal event contains unsafe %s", name)
		}
	}
	if strings.TrimSpace(s.operation) == "" || strings.TrimSpace(event.Message) == "" {
		return fmt.Errorf("terminal event is incomplete")
	}
	prefix, suffix := "", ""
	if s.experience == TerminalColor {
		prefix, suffix = "\x1b[38;2;255;171;45m◆ ", "\x1b[0m"
	}
	if event.Stage == StageFailed {
		if _, err := fmt.Fprintf(s.writer, "%s%s: stopped: %s%s\n", prefix, s.operation, event.Message, suffix); err != nil {
			return err
		}
		if event.RecoveryCommand != "" {
			_, err := fmt.Fprintf(s.writer, "%s%s: recover: %s%s\n", prefix, s.operation, event.RecoveryCommand, suffix)
			return err
		}
		return nil
	}
	_, err := fmt.Fprintf(s.writer, "%s%s: %s%s\n", prefix, s.operation, event.Message, suffix)
	return err
}
