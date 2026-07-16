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
	ErrStartObservationRequired  = errors.New("DevPod runner cannot observe subprocess start")
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
	WorkspacePath    string
	WorkspaceID      string
	Context          string
	Provider         string
	DevcontainerPath string
	CampEnvironment  *CampEnvironment
	InitEnv          []string
	ForwardedArgv    []string
}

type CampEnvironment struct {
	Registry   string
	Fileserver string
	Capsule    string
	Checkpoint string
}

type SSHOptions struct {
	WorkspaceID     string
	Context         string
	Workdir         string
	User            string
	ForwardPorts    []string
	ReverseForwards []string
	SetEnv          []string
	StartServices   bool
	ForwardedArgv   []string
}

type WorkspaceCommand = ports.WorkspaceCommand

type Client struct {
	executable string
	runner     ports.Runner
}

func NewClient(executable string, runner ports.Runner) *Client {
	return &Client{executable: executable, runner: runner}
}

func (c *Client) Up(ctx context.Context, options UpOptions) (ports.Result, error) {
	argv := []string{"up", "--ide", "none", "--open-ide=false"}
	if options.Context != "" {
		argv = append(argv, "--context", options.Context)
	}
	if options.WorkspaceID != "" {
		argv = append(argv, "--id", options.WorkspaceID)
	}
	if options.Provider != "" {
		argv = append(argv, "--provider", options.Provider)
	}
	if options.DevcontainerPath != "" {
		argv = append(argv, "--devcontainer-path", options.DevcontainerPath)
	}
	if options.CampEnvironment != nil {
		values, err := options.CampEnvironment.values()
		if err != nil {
			return ports.Result{}, err
		}
		for _, value := range values {
			argv = append(argv, "--workspace-env", value)
		}
	}
	for _, value := range options.InitEnv {
		argv = append(argv, "--init-env", value)
	}
	argv = append(argv, options.ForwardedArgv...)
	argv = append(argv, options.WorkspacePath)
	return c.run(ctx, argv)
}

func (c *Client) SSH(ctx context.Context, options SSHOptions) (ports.Result, error) {
	command, err := c.SSHCommand(options)
	if err != nil {
		return ports.Result{}, err
	}
	return c.runner.Run(ctx, command)
}

func (c *Client) SSHWithStart(ctx context.Context, options SSHOptions, started func() error) (ports.Result, error) {
	command, err := c.SSHCommand(options)
	if err != nil {
		return ports.Result{}, err
	}
	runner, ok := c.runner.(ports.StartedRunner)
	if !ok {
		return ports.Result{}, ErrStartObservationRequired
	}
	return runner.RunStarted(ctx, command, started)
}

func (c *Client) SSHCommand(options SSHOptions) (ports.Command, error) {
	if options.WorkspaceID == "" || unsafeArgument(options.WorkspaceID) {
		return ports.Command{}, errors.New("DevPod workspace ID is invalid")
	}
	argv := []string{"ssh"}
	if options.Context != "" {
		argv = append(argv, "--context", options.Context)
	}
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
	return ports.Command{Executable: c.executable, Argv: argv}, nil
}

func (c *Client) Execute(ctx context.Context, command WorkspaceCommand) (ports.Result, error) {
	if command.WorkspaceID == "" || len(command.Argv) == 0 || unsafeArgument(command.WorkspaceID) || unsafeArgument(command.Context) || unsafeArgument(command.Workdir) {
		return ports.Result{}, errors.New("invalid structured DevPod workspace command")
	}
	argv := []string{"ssh"}
	if command.Context != "" {
		argv = append(argv, "--context", command.Context)
	}
	if command.Workdir != "" {
		argv = append(argv, "--workdir", command.Workdir)
	}
	argv = append(argv, "--start-services=false", "--command", encodeCommand(command.Argv), command.WorkspaceID)
	return c.run(ctx, argv)
}

func (c *Client) Status(ctx context.Context, workspaceID string) (WorkspaceStatus, error) {
	return c.StatusInContext(ctx, "", workspaceID)
}

func (c *Client) StatusInContext(ctx context.Context, devpodContext, workspaceID string) (WorkspaceStatus, error) {
	argv := []string{"status"}
	if devpodContext != "" {
		argv = append(argv, "--context", devpodContext)
	}
	argv = append(argv, "--output", "json", workspaceID)
	result, err := c.run(ctx, argv)
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
	return c.ListInContext(ctx, "")
}

func (c *Client) ListInContext(ctx context.Context, devpodContext string) ([]Workspace, error) {
	argv := []string{"list"}
	if devpodContext != "" {
		argv = append(argv, "--context", devpodContext)
	}
	argv = append(argv, "--output", "json", "--skip-pro")
	result, err := c.run(ctx, argv)
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
	return c.StopInContext(ctx, "", workspaceID, allowed)
}

func (c *Client) StopInContext(ctx context.Context, devpodContext, workspaceID string, allowed bool) (ports.Result, error) {
	if !allowed {
		return ports.Result{}, ErrLifecycleActionNotAllowed
	}
	argv := []string{"stop"}
	if devpodContext != "" {
		argv = append(argv, "--context", devpodContext)
	}
	return c.run(ctx, append(argv, workspaceID))
}

func (c *Client) Delete(ctx context.Context, workspaceID string, allowed bool) (ports.Result, error) {
	return c.DeleteInContext(ctx, "", workspaceID, allowed)
}

func (c *Client) DeleteInContext(ctx context.Context, devpodContext, workspaceID string, allowed bool) (ports.Result, error) {
	if !allowed {
		return ports.Result{}, ErrLifecycleActionNotAllowed
	}
	argv := []string{"delete"}
	if devpodContext != "" {
		argv = append(argv, "--context", devpodContext)
	}
	argv = append(argv, "--ignore-not-found", workspaceID)
	return c.run(ctx, argv)
}

func (c *Client) ResolveWorkspaceFolder(ctx context.Context, workspaceID string) (string, error) {
	return c.ResolveWorkspaceFolderInContext(ctx, "", workspaceID)
}

func (c *Client) ResolveWorkspaceFolderInContext(ctx context.Context, devpodContext, workspaceID string) (string, error) {
	argv := []string{"ssh"}
	if devpodContext != "" {
		argv = append(argv, "--context", devpodContext)
	}
	argv = append(argv, "--start-services=false", "--command", "pwd", workspaceID)
	result, err := c.run(ctx, argv)
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

func (e CampEnvironment) values() ([]string, error) {
	values := []struct {
		key   string
		value string
	}{
		{"CAMP_REGISTRY", e.Registry},
		{"CAMP_FILESERVER", e.Fileserver},
		{"CAMP_CAPSULE", e.Capsule},
		{"CAMP_CHECKPOINT", e.Checkpoint},
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if unsafeArgument(value.value) || value.value == "" && value.key != "CAMP_CHECKPOINT" {
			return nil, fmt.Errorf("%s workspace environment is invalid", value.key)
		}
		result = append(result, value.key+"="+value.value)
	}
	return result, nil
}

func encodeCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for index, argument := range argv {
		quoted[index] = "'" + strings.ReplaceAll(argument, "'", `'"'"'`) + "'"
	}
	return strings.Join(quoted, " ")
}

func unsafeArgument(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}
