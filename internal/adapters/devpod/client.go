package devpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrLifecycleActionNotAllowed = errors.New("DevPod lifecycle action not allowed")
	ErrUnknownWorkspaceState     = errors.New("unknown DevPod workspace state")
)

type WorkspaceState string

const (
	StateRunning  WorkspaceState = "Running"
	StateBusy     WorkspaceState = "Busy"
	StateStopped  WorkspaceState = "Stopped"
	StateNotFound WorkspaceState = "NotFound"
)

type WorkspaceStatus struct {
	ID       string         `json:"id,omitempty"`
	Context  string         `json:"context,omitempty"`
	Provider string         `json:"provider,omitempty"`
	State    WorkspaceState `json:"state,omitempty"`
}

type Workspace struct {
	ID       string            `json:"id,omitempty"`
	UID      string            `json:"uid,omitempty"`
	Provider WorkspaceProvider `json:"provider"`
	Source   WorkspaceSource   `json:"source"`
	Context  string            `json:"context,omitempty"`
}

type WorkspaceProvider struct {
	Name string `json:"name,omitempty"`
}

type WorkspaceSource struct {
	LocalFolder string `json:"localFolder,omitempty"`
}

type UpOptions struct {
	WorkspacePath string
	WorkspaceID   string
	Provider      string
	InitEnv       []string
	ForwardedArgv []string
}

type SSHOptions struct {
	WorkspaceID     string
	Workdir         string
	User            string
	ForwardPorts    []string
	ReverseForwards []string
	SetEnv          []string
	StartServices   bool
	ForwardedArgv   []string
}

type Client struct {
	executable string
	runner     ports.Runner
}

func NewClient(executable string, runner ports.Runner) *Client {
	return &Client{executable: executable, runner: runner}
}

func (c *Client) Up(ctx context.Context, options UpOptions) (ports.Result, error) {
	argv := []string{"up", "--ide", "none", "--open-ide=false"}
	if options.WorkspaceID != "" {
		argv = append(argv, "--id", options.WorkspaceID)
	}
	if options.Provider != "" {
		argv = append(argv, "--provider", options.Provider)
	}
	for _, value := range options.InitEnv {
		argv = append(argv, "--init-env", value)
	}
	argv = append(argv, options.ForwardedArgv...)
	argv = append(argv, options.WorkspacePath)
	return c.run(ctx, argv)
}

func (c *Client) SSH(ctx context.Context, options SSHOptions) (ports.Result, error) {
	argv := []string{"ssh"}
	if options.Workdir != "" {
		argv = append(argv, "--workdir", options.Workdir)
	}
	if options.User != "" {
		argv = append(argv, "--user", options.User)
	}
	for _, value := range options.ForwardPorts {
		argv = append(argv, "--forward-ports", value)
	}
	for _, value := range options.ReverseForwards {
		argv = append(argv, "--reverse-forward-ports", value)
	}
	for _, value := range options.SetEnv {
		argv = append(argv, "--set-env", value)
	}
	argv = append(argv, options.ForwardedArgv...)
	argv = append(argv, "--start-services="+strconv.FormatBool(options.StartServices))
	argv = append(argv, options.WorkspaceID)
	return c.run(ctx, argv)
}

func (c *Client) Status(ctx context.Context, workspaceID string) (WorkspaceStatus, error) {
	result, err := c.run(ctx, []string{"status", "--output", "json", workspaceID})
	if err != nil {
		return WorkspaceStatus{}, err
	}
	var status WorkspaceStatus
	if err := json.Unmarshal(result.Stdout, &status); err != nil {
		return WorkspaceStatus{}, fmt.Errorf("decode DevPod status JSON: %w", err)
	}
	if !validState(status.State) {
		return WorkspaceStatus{}, fmt.Errorf("%w: %q", ErrUnknownWorkspaceState, status.State)
	}
	return status, nil
}

func (c *Client) List(ctx context.Context) ([]Workspace, error) {
	result, err := c.run(ctx, []string{"list", "--output", "json", "--skip-pro"})
	if err != nil {
		return nil, err
	}
	var workspaces []Workspace
	if err := json.Unmarshal(result.Stdout, &workspaces); err != nil {
		return nil, fmt.Errorf("decode DevPod list JSON: %w", err)
	}
	return workspaces, nil
}

func (c *Client) Stop(ctx context.Context, workspaceID string, allowed bool) (ports.Result, error) {
	if !allowed {
		return ports.Result{}, ErrLifecycleActionNotAllowed
	}
	return c.run(ctx, []string{"stop", workspaceID})
}

func (c *Client) Delete(ctx context.Context, workspaceID string, allowed bool) (ports.Result, error) {
	if !allowed {
		return ports.Result{}, ErrLifecycleActionNotAllowed
	}
	return c.run(ctx, []string{"delete", "--ignore-not-found", workspaceID})
}

func (c *Client) ResolveWorkspaceFolder(ctx context.Context, workspaceID string) (string, error) {
	result, err := c.run(ctx, []string{"ssh", "--start-services=false", "--command", "pwd", workspaceID})
	if err != nil {
		return "", err
	}
	folder := strings.TrimSpace(string(result.Stdout))
	if folder == "" {
		return "", errors.New("DevPod returned an empty workspace folder")
	}
	return folder, nil
}

func (c *Client) run(ctx context.Context, argv []string) (ports.Result, error) {
	return c.runner.Run(ctx, ports.Command{Executable: c.executable, Argv: argv})
}

func validState(state WorkspaceState) bool {
	switch state {
	case StateRunning, StateBusy, StateStopped, StateNotFound:
		return true
	default:
		return false
	}
}
