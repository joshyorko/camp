package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/joshyorko/camp/internal/presentation"
)

type setupPromptDefaults struct {
	Source  string
	Backend string
}

func promptSetupRequest(in io.Reader, out io.Writer, defaults setupPromptDefaults, experience presentation.TerminalExperience, size presentation.ScreenSize) (InitRequest, error) {
	reader := bufio.NewReader(in)
	var answers []presentation.ConfigureAnswer

	read := func(label, defaultValue string) (string, error) {
		if experience == presentation.TerminalColor {
			frame := "\x1b[2J\x1b[H" + presentation.ComposeConfigureFrame(answers, label, defaultValue, size)
			if _, err := io.WriteString(out, frame); err != nil {
				return "", err
			}
		} else if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultValue); err != nil {
			return "", err
		}
		value, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = strings.TrimSpace(defaultValue)
		}
		answers = append(answers, presentation.ConfigureAnswer{Label: label, Value: value})
		return value, nil
	}

	source, err := read("Source path", defaults.Source)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read source path: %w", err)
	}
	capsuleDefault := filepath.Base(filepath.Clean(source))
	capsule, err := read("Capsule name", capsuleDefault)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read capsule name: %w", err)
	}
	backend, err := read("Backend URL", defaults.Backend)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read backend URL: %w", err)
	}
	provider, err := read("DevPod provider", "docker")
	if err != nil {
		return InitRequest{}, fmt.Errorf("read DevPod provider: %w", err)
	}
	devpodContext, err := read("DevPod context", "default")
	if err != nil {
		return InitRequest{}, fmt.Errorf("read DevPod context: %w", err)
	}
	request := InitRequest{
		Source: source, Capsule: capsule, Backend: backend,
		DevPodProvider: provider, DevPodContext: devpodContext,
	}
	if request.Source == "" || request.Capsule == "" || request.Backend == "" || request.DevPodProvider == "" || request.DevPodContext == "" {
		return InitRequest{}, errors.New("setup values cannot be empty")
	}
	return request, nil
}
