package devpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrLifecycleActionNotAllowed = errors.New("DevPod lifecycle action not allowed")
	ErrUnknownWorkspaceState     = errors.New("unknown DevPod workspace state")
	ErrStartObservationRequired  = errors.New("DevPod runner cannot observe subprocess start")
	ErrDevPodArgumentConflict    = errors.New("typed DevPod option conflicts with raw argument")
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

type ProviderRequest struct {
	Context string
	Name    string
	Options []string
}

type UpOptions struct {
	WorkspacePath        string
	BootstrapPath        string
	SourceMode           SourceMode
	WorkspaceID          string
	Context              string
	Provider             string
	Machine              string
	ProviderOptions      []string
	DevcontainerImage    string
	DevcontainerPath     string
	DevcontainerID       string
	FallbackImage        string
	AdditionalFeatures   string
	Mounts               []string
	WorkspaceEnv         []string
	WorkspaceEnvFiles    []string
	CampEnvironment      *CampEnvironment
	InitEnv              []string
	Dotfiles             string
	Recreate             bool
	Reset                bool
	PrebuildRepositories []string
	IDE                  IDE
	IDEOptions           []string
	OpenIDE              *bool
	ConfigureSSH         *bool
	GPGAgentForwarding   *bool
	SSHConfigPath        string
	ForwardedArgv        []string
}

type SourceMode string

const (
	SourceModeCapsule   SourceMode = "capsule"
	SourceModeBootstrap SourceMode = "bootstrap"
)

type CampEnvironment struct {
	Registry   string
	Fileserver string
	Capsule    string
	Checkpoint string
}

type SSHOptions struct {
	WorkspaceID          string
	Context              string
	Workdir              string
	User                 string
	ForwardPorts         []string
	ReverseForwards      []string
	SendEnv              []string
	SetEnv               []string
	ForwardPortsTimeout  string
	AgentForwarding      *bool
	GPGAgentForwarding   *bool
	Stdio                *bool
	SSHKeepAliveInterval string
	GitSSHSigningKey     string
	TermMode             string
	InstallTerminfo      *bool
	StartServices        bool
	ForwardedArgv        []string
	Stdin                io.Reader
	Stdout               io.Writer
	Stderr               io.Writer
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
	entry := IDEEntry{IDE: options.IDE}
	if entry.IDE == "" {
		entry.IDE = IDETerminal
	}
	devPodIDE, openIDE, err := entry.DevPodSetup()
	if err != nil {
		return ports.Result{}, err
	}
	if options.OpenIDE != nil {
		openIDE = *options.OpenIDE
	}
	if (entry.IDE == IDETerminal || entry.IDE == IDET3Code) && openIDE {
		return ports.Result{}, fmt.Errorf("%w: %q cannot request DevPod IDE opening", ErrInvalidIDEEntry, entry.IDE)
	}
	conflicts := []string{"--ide", "--open-ide"}
	conflicts = appendActiveFlag(conflicts, options.Context != "", "--context")
	conflicts = appendActiveFlag(conflicts, options.WorkspaceID != "", "--id")
	conflicts = appendActiveFlag(conflicts, options.Provider != "", "--provider")
	conflicts = appendActiveFlag(conflicts, options.Machine != "", "--machine")
	conflicts = appendActiveFlag(conflicts, len(options.ProviderOptions) > 0, "--provider-option")
	conflicts = appendActiveFlag(conflicts, options.DevcontainerImage != "", "--devcontainer-image")
	conflicts = appendActiveFlag(conflicts, options.DevcontainerPath != "", "--devcontainer-path")
	conflicts = appendActiveFlag(conflicts, options.DevcontainerID != "", "--devcontainer-id")
	conflicts = appendActiveFlag(conflicts, options.FallbackImage != "", "--fallback-image")
	conflicts = appendActiveFlag(conflicts, options.AdditionalFeatures != "", "--additional-features")
	conflicts = appendActiveFlag(conflicts, len(options.Mounts) > 0, "--mount")
	conflicts = appendActiveFlag(conflicts, len(options.WorkspaceEnv) > 0 || options.CampEnvironment != nil, "--workspace-env")
	conflicts = appendActiveFlag(conflicts, len(options.WorkspaceEnvFiles) > 0, "--workspace-env-file")
	conflicts = appendActiveFlag(conflicts, len(options.InitEnv) > 0, "--init-env")
	conflicts = appendActiveFlag(conflicts, options.Dotfiles != "", "--dotfiles")
	conflicts = appendActiveFlag(conflicts, options.Recreate, "--recreate")
	conflicts = appendActiveFlag(conflicts, options.Reset, "--reset")
	conflicts = appendActiveFlag(conflicts, len(options.PrebuildRepositories) > 0, "--prebuild-repository")
	conflicts = appendActiveFlag(conflicts, len(options.IDEOptions) > 0, "--ide-option")
	conflicts = appendActiveFlag(conflicts, options.ConfigureSSH != nil, "--configure-ssh")
	conflicts = appendActiveFlag(conflicts, options.GPGAgentForwarding != nil, "--gpg-agent-forwarding")
	conflicts = appendActiveFlag(conflicts, options.SSHConfigPath != "", "--ssh-config")
	if err := rejectRawConflicts(options.ForwardedArgv, conflicts...); err != nil {
		return ports.Result{}, err
	}

	argv := []string{"up", "--ide", string(devPodIDE), "--open-ide=" + strconv.FormatBool(openIDE)}
	if options.Context != "" {
		argv = append(argv, "--context", options.Context)
	}
	if options.WorkspaceID != "" {
		argv = append(argv, "--id", options.WorkspaceID)
	}
	if options.Provider != "" {
		argv = append(argv, "--provider", options.Provider)
	}
	if options.Machine != "" {
		argv = append(argv, "--machine", options.Machine)
	}
	argv = appendRepeated(argv, "--provider-option", options.ProviderOptions)
	if options.DevcontainerImage != "" {
		argv = append(argv, "--devcontainer-image", options.DevcontainerImage)
	}
	if options.DevcontainerPath != "" {
		argv = append(argv, "--devcontainer-path", options.DevcontainerPath)
	}
	if options.DevcontainerID != "" {
		argv = append(argv, "--devcontainer-id", options.DevcontainerID)
	}
	if options.FallbackImage != "" {
		argv = append(argv, "--fallback-image", options.FallbackImage)
	}
	if options.AdditionalFeatures != "" {
		argv = append(argv, "--additional-features", options.AdditionalFeatures)
	}
	argv = appendRepeated(argv, "--mount", options.Mounts)
	argv = appendRepeated(argv, "--workspace-env", options.WorkspaceEnv)
	argv = appendRepeated(argv, "--workspace-env-file", options.WorkspaceEnvFiles)
	if options.CampEnvironment != nil {
		values, err := options.CampEnvironment.values()
		if err != nil {
			return ports.Result{}, err
		}
		for _, value := range values {
			argv = append(argv, "--workspace-env", value)
		}
	}
	argv = appendRepeated(argv, "--init-env", options.InitEnv)
	if options.Dotfiles != "" {
		argv = append(argv, "--dotfiles", options.Dotfiles)
	}
	if options.Recreate {
		argv = append(argv, "--recreate=true")
	}
	if options.Reset {
		argv = append(argv, "--reset=true")
	}
	argv = appendRepeated(argv, "--prebuild-repository", options.PrebuildRepositories)
	argv = appendRepeated(argv, "--ide-option", options.IDEOptions)
	argv = appendOptionalBool(argv, "--configure-ssh", options.ConfigureSSH)
	argv = appendOptionalBool(argv, "--gpg-agent-forwarding", options.GPGAgentForwarding)
	if options.SSHConfigPath != "" {
		argv = append(argv, "--ssh-config", options.SSHConfigPath)
	}
	argv = append(argv, options.ForwardedArgv...)
	sourcePath := options.WorkspacePath
	if options.SourceMode == SourceModeBootstrap {
		sourcePath = options.BootstrapPath
	}
	argv = append(argv, sourcePath)
	return c.run(ctx, argv)
}

func (c *Client) EnsureProvider(ctx context.Context, devpodContext, provider string) error {
	if c == nil || c.runner == nil || strings.TrimSpace(devpodContext) == "" || strings.TrimSpace(provider) == "" {
		return errors.New("unsupported or incomplete DevPod provider request")
	}
	providers, err := c.listProviders(ctx, devpodContext)
	if err != nil {
		return err
	}
	configured, exists := providers[provider]
	if provider != "docker" {
		if !exists || !configured.State.Initialized {
			return fmt.Errorf("configured DevPod provider identity %q was not verified as initialized", provider)
		}
		return nil
	}
	if !exists {
		if _, err := c.run(ctx, []string{"provider", "add", provider, "--context", devpodContext, "--use"}); err != nil {
			return fmt.Errorf("add DevPod provider %q: %w", provider, err)
		}
	} else if !configured.Default {
		if _, err := c.run(ctx, []string{"provider", "use", provider, "--context", devpodContext, "--reconfigure"}); err != nil {
			return fmt.Errorf("configure DevPod provider %q: %w", provider, err)
		}
	}
	providers, err = c.listProviders(ctx, devpodContext)
	if err != nil {
		return err
	}
	if configured, ok := providers[provider]; !ok || !configured.Default {
		return fmt.Errorf("configured DevPod provider identity %q was not verified", provider)
	}
	return nil
}

// AddProvider ensures the built-in Docker provider exists and is selected in
// one DevPod context. DevPod remains authoritative for provider configuration.
func (c *Client) AddProvider(ctx context.Context, request ProviderRequest) error {
	if err := validateProviderRequest(c, request); err != nil {
		return err
	}
	if request.Name != "docker" {
		return fmt.Errorf("adding DevPod provider %q is unsupported; only the built-in docker provider is supported", request.Name)
	}
	providers, err := c.listProviders(ctx, request.Context)
	if err != nil {
		return err
	}
	configured, exists := providers[request.Name]
	if exists && configured.Default && len(request.Options) == 0 {
		return nil
	}
	argv := []string{"provider", "add", request.Name, "--context", request.Context, "--use"}
	if exists {
		argv = []string{"provider", "use", request.Name, "--context", request.Context, "--reconfigure"}
	}
	argv = appendRepeated(argv, "--option", request.Options)
	if _, err := c.run(ctx, argv); err != nil {
		return fmt.Errorf("configure DevPod provider %q: %w", request.Name, err)
	}
	return c.verifyDefaultProvider(ctx, request.Context, request.Name)
}

// UseProvider selects and reconfigures one existing provider through DevPod's
// typed provider command surface.
func (c *Client) UseProvider(ctx context.Context, request ProviderRequest) error {
	if err := validateProviderRequest(c, request); err != nil {
		return err
	}
	providers, err := c.listProviders(ctx, request.Context)
	if err != nil {
		return err
	}
	if _, exists := providers[request.Name]; !exists {
		return fmt.Errorf("DevPod provider %q was not found in context %q", request.Name, request.Context)
	}
	argv := []string{"provider", "use", request.Name, "--context", request.Context, "--reconfigure"}
	argv = appendRepeated(argv, "--option", request.Options)
	if _, err := c.run(ctx, argv); err != nil {
		return fmt.Errorf("select DevPod provider %q: %w", request.Name, err)
	}
	return c.verifyDefaultProvider(ctx, request.Context, request.Name)
}

func validateProviderRequest(c *Client, request ProviderRequest) error {
	if c == nil || c.runner == nil || strings.TrimSpace(request.Context) != request.Context || request.Context == "" ||
		strings.TrimSpace(request.Name) != request.Name || request.Name == "" || strings.ContainsAny(request.Context+request.Name, "/\\\t\r\n ") {
		return errors.New("unsupported or incomplete DevPod provider request")
	}
	for _, option := range request.Options {
		key, value, ok := strings.Cut(option, "=")
		if !ok || key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(option, "\x00\r\n") {
			return errors.New("DevPod provider options must use KEY=VALUE without control characters")
		}
		if request.Name != "docker" {
			return fmt.Errorf("DevPod provider option %q is unsupported for provider %q", key, request.Name)
		}
		switch key {
		case "DOCKER_PATH":
			if !filepath.IsAbs(value) {
				return errors.New("DevPod docker option DOCKER_PATH must be an absolute path")
			}
		case "HELPER":
			if _, err := strconv.ParseBool(value); err != nil {
				return errors.New("DevPod docker option HELPER must be true or false")
			}
		default:
			return fmt.Errorf("DevPod docker option %q is not in Camp's non-secret allowlist", key)
		}
	}
	return nil
}

func (c *Client) verifyDefaultProvider(ctx context.Context, devpodContext, provider string) error {
	providers, err := c.listProviders(ctx, devpodContext)
	if err != nil {
		return err
	}
	if configured, ok := providers[provider]; !ok || !configured.Default {
		return fmt.Errorf("configured DevPod provider identity %q was not verified", provider)
	}
	return nil
}

// ProbeProvider verifies existing provider identity without adding, selecting,
// or reconfiguring provider state.
func (c *Client) ProbeProvider(ctx context.Context, devpodContext, provider string) error {
	if c == nil || c.runner == nil || strings.TrimSpace(devpodContext) == "" || strings.TrimSpace(provider) == "" {
		return errors.New("unsupported or incomplete DevPod provider probe")
	}
	providers, err := c.listProviders(ctx, devpodContext)
	if err != nil {
		return err
	}
	configured, exists := providers[provider]
	if !exists {
		return fmt.Errorf("configured DevPod provider identity %q was not found", provider)
	}
	if provider == "docker" {
		if !configured.Default {
			return fmt.Errorf("configured DevPod provider identity %q is not selected", provider)
		}
		return nil
	}
	if !configured.State.Initialized {
		return fmt.Errorf("configured DevPod provider identity %q is not initialized", provider)
	}
	return nil
}

type providerState struct {
	Default bool `json:"default"`
	State   struct {
		Initialized bool `json:"initialized"`
	} `json:"state"`
}

func (c *Client) listProviders(ctx context.Context, devpodContext string) (map[string]providerState, error) {
	result, err := c.run(ctx, []string{"provider", "list", "--context", devpodContext, "--output", "json"})
	if err != nil {
		return nil, fmt.Errorf("list DevPod providers in context %q: %w", devpodContext, err)
	}
	providers := map[string]providerState{}
	if err := json.Unmarshal(result.Stdout, &providers); err != nil {
		return nil, fmt.Errorf("decode DevPod provider list: %w", err)
	}
	return providers, nil
}

func (c *Client) ListProviderNames(ctx context.Context, devpodContext string) ([]string, error) {
	providers, err := c.listProviders(ctx, devpodContext)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
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
	conflicts := []string{"--start-services"}
	conflicts = appendActiveFlag(conflicts, options.Context != "", "--context")
	conflicts = appendActiveFlag(conflicts, options.Workdir != "", "--workdir")
	conflicts = appendActiveFlag(conflicts, options.User != "", "--user")
	conflicts = appendActiveFlag(conflicts, len(options.ForwardPorts) > 0, "--forward-ports", "-L")
	conflicts = appendActiveFlag(conflicts, len(options.ReverseForwards) > 0, "--reverse-forward-ports", "-R")
	conflicts = appendActiveFlag(conflicts, len(options.SendEnv) > 0, "--send-env")
	conflicts = appendActiveFlag(conflicts, len(options.SetEnv) > 0, "--set-env")
	conflicts = appendActiveFlag(conflicts, options.ForwardPortsTimeout != "", "--forward-ports-timeout")
	conflicts = appendActiveFlag(conflicts, options.AgentForwarding != nil, "--agent-forwarding")
	conflicts = appendActiveFlag(conflicts, options.GPGAgentForwarding != nil, "--gpg-agent-forwarding")
	conflicts = appendActiveFlag(conflicts, options.Stdio != nil, "--stdio")
	conflicts = appendActiveFlag(conflicts, options.SSHKeepAliveInterval != "", "--ssh-keepalive-interval")
	conflicts = appendActiveFlag(conflicts, options.GitSSHSigningKey != "", "--git-ssh-signing-key")
	conflicts = appendActiveFlag(conflicts, options.TermMode != "", "--term-mode")
	conflicts = appendActiveFlag(conflicts, options.InstallTerminfo != nil, "--install-terminfo")
	if err := rejectRawConflicts(options.ForwardedArgv, conflicts...); err != nil {
		return ports.Command{}, err
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
	argv = appendRepeated(argv, "--forward-ports", options.ForwardPorts)
	argv = appendRepeated(argv, "--reverse-forward-ports", options.ReverseForwards)
	argv = appendRepeated(argv, "--send-env", options.SendEnv)
	argv = appendRepeated(argv, "--set-env", options.SetEnv)
	if options.ForwardPortsTimeout != "" {
		argv = append(argv, "--forward-ports-timeout", options.ForwardPortsTimeout)
	}
	argv = appendOptionalBool(argv, "--agent-forwarding", options.AgentForwarding)
	argv = appendOptionalBool(argv, "--gpg-agent-forwarding", options.GPGAgentForwarding)
	argv = appendOptionalBool(argv, "--stdio", options.Stdio)
	if options.SSHKeepAliveInterval != "" {
		argv = append(argv, "--ssh-keepalive-interval", options.SSHKeepAliveInterval)
	}
	if options.GitSSHSigningKey != "" {
		argv = append(argv, "--git-ssh-signing-key", options.GitSSHSigningKey)
	}
	if options.TermMode != "" {
		argv = append(argv, "--term-mode", options.TermMode)
	}
	argv = appendOptionalBool(argv, "--install-terminfo", options.InstallTerminfo)
	argv = append(argv, options.ForwardedArgv...)
	argv = append(argv, "--start-services="+strconv.FormatBool(options.StartServices))
	argv = append(argv, options.WorkspaceID)
	return ports.Command{Executable: c.executable, Argv: argv, Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr}, nil
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

func appendRepeated(argv []string, flag string, values []string) []string {
	for _, value := range values {
		argv = append(argv, flag, value)
	}
	return argv
}

func appendOptionalBool(argv []string, flag string, value *bool) []string {
	if value == nil {
		return argv
	}
	return append(argv, flag+"="+strconv.FormatBool(*value))
}

func appendActiveFlag(flags []string, active bool, names ...string) []string {
	if !active {
		return flags
	}
	return append(flags, names...)
}

func rejectRawConflicts(argv []string, typedFlags ...string) error {
	typed := make(map[string]struct{}, len(typedFlags))
	for _, flag := range typedFlags {
		typed[flag] = struct{}{}
	}
	for _, argument := range argv {
		flag := rawFlagName(argument)
		if _, conflict := typed[flag]; conflict {
			return fmt.Errorf("%w: %s", ErrDevPodArgumentConflict, flag)
		}
	}
	return nil
}

func rawFlagName(argument string) string {
	if strings.HasPrefix(argument, "--") {
		if index := strings.IndexByte(argument, '='); index >= 0 {
			return argument[:index]
		}
		return argument
	}
	if strings.HasPrefix(argument, "-L") {
		return "-L"
	}
	if strings.HasPrefix(argument, "-R") {
		return "-R"
	}
	return ""
}
