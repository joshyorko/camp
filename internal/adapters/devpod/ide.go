package devpod

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"

	"github.com/joshyorko/camp/internal/ports"
)

var ErrInvalidIDEEntry = errors.New("invalid IDE entry")

type IDE string

const (
	IDETerminal       IDE = "none"
	IDEVSCode         IDE = "vscode"
	IDEVSCodeInsiders IDE = "vscode-insiders"
	IDET3Code         IDE = "t3-code"
)

type IDEEntry struct {
	IDE   IDE
	Sites bool
}

func (e IDEEntry) Validate() error {
	switch e.IDE {
	case IDETerminal, IDEVSCode, IDEVSCodeInsiders, IDET3Code:
	default:
		return fmt.Errorf("%w: unsupported IDE %q", ErrInvalidIDEEntry, e.IDE)
	}
	if e.Sites && e.IDE != IDET3Code {
		return fmt.Errorf("%w: Sites requires IDE %q", ErrInvalidIDEEntry, IDET3Code)
	}
	return nil
}

func (e IDEEntry) DevPodSetup() (IDE, bool, error) {
	if err := e.Validate(); err != nil {
		return "", false, err
	}
	switch e.IDE {
	case IDETerminal, IDET3Code:
		return IDETerminal, false, nil
	default:
		return e.IDE, true, nil
	}
}

type IDEOpenOptions struct {
	IDE             IDEEntry
	WorkspaceID     string
	ContainerTarget string
}

var workspaceHostIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)

func WorkspaceSSHHost(workspaceID string) (string, error) {
	if !workspaceHostIDPattern.MatchString(workspaceID) {
		return "", fmt.Errorf("%w: invalid DevPod workspace host ID", ErrInvalidIDEEntry)
	}
	return workspaceID + ".devpod", nil
}

func NestedIDECommand(options IDEOpenOptions) (ports.Command, error) {
	if err := options.IDE.Validate(); err != nil {
		return ports.Command{}, err
	}
	var executable string
	switch options.IDE.IDE {
	case IDEVSCode:
		executable = "code"
	case IDEVSCodeInsiders:
		executable = "code-insiders"
	default:
		return ports.Command{}, fmt.Errorf("%w: %q does not support a nested VS Code URI", ErrInvalidIDEEntry, options.IDE.IDE)
	}
	host, err := WorkspaceSSHHost(options.WorkspaceID)
	if err != nil {
		return ports.Command{}, err
	}
	if !path.IsAbs(options.ContainerTarget) || unsafeArgument(options.ContainerTarget) || path.Clean(options.ContainerTarget) != options.ContainerTarget {
		return ports.Command{}, fmt.Errorf("%w: container target must be a clean absolute path", ErrInvalidIDEEntry)
	}
	uri := (&url.URL{
		Scheme: "vscode-remote",
		Host:   "ssh-remote+" + host,
		Path:   options.ContainerTarget,
	}).String()
	return ports.Command{Executable: executable, Argv: []string{"--reuse-window", "--folder-uri", uri}}, nil
}

func (c *Client) OpenNestedIDE(ctx context.Context, options IDEOpenOptions) (ports.Result, error) {
	command, err := NestedIDECommand(options)
	if err != nil {
		return ports.Result{}, err
	}
	return c.runner.Run(ctx, command)
}
