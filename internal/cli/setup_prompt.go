package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type setupPromptDefaults struct {
	Source  string
	Backend string
}

func promptSetupRequest(in io.Reader, out io.Writer, defaults setupPromptDefaults) (InitRequest, error) {
	reader := bufio.NewReader(in)
	source, err := promptSetupValue(reader, out, "Source path", defaults.Source)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read source path: %w", err)
	}
	capsuleDefault := filepath.Base(filepath.Clean(source))
	capsule, err := promptSetupValue(reader, out, "Capsule name", capsuleDefault)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read capsule name: %w", err)
	}
	backend, err := promptSetupValue(reader, out, "Backend URL", defaults.Backend)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read backend URL: %w", err)
	}
	provider, err := promptSetupValue(reader, out, "DevPod provider", "docker")
	if err != nil {
		return InitRequest{}, fmt.Errorf("read DevPod provider: %w", err)
	}
	devpodContext, err := promptSetupValue(reader, out, "DevPod context", "default")
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

func promptSetupValue(reader *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultValue); err != nil {
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
	return value, nil
}
