package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/joshyorko/camp/internal/remoteworker"
	"github.com/spf13/cobra"
)

var ErrImagesCaptureRequiresCheckpoint = errors.New("camp images capture does not inspect workspace engines; push images through CAMP_REGISTRY, then run camp sync or camp close")

// NewRoot constructs Camp's production command root. Lifecycle commands are
// registered only when their production dependencies are available.
func NewRoot() *cobra.Command {
	return NewRootWithLifecycle(NewProductionLifecycle())
}

type Lifecycle interface {
	Init(context.Context, InitRequest, OutputMode, io.Writer) error
	Open(context.Context, string, OutputMode, io.Writer) error
	Attach(context.Context, AttachRequest, OutputMode, io.Writer) error
	Sync(context.Context, OutputMode, io.Writer) error
	Close(context.Context, CloseRequest, OutputMode, io.Writer) error
	Reopen(context.Context, string, OutputMode, io.Writer) error
	Recover(context.Context, string, OutputMode, io.Writer) error
	Supervise(context.Context, string, OutputMode, io.Writer) error
}

type AttachRequest struct {
	Target               string
	IDE                  string
	User                 string
	ForwardPorts         []string
	ReverseForwardPorts  []string
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
	DevPodArgs           []string
}

type InitRequest struct {
	Root           string
	Source         string
	Backend        string
	Capsule        string
	DevPodProvider string
	DevPodContext  string
	Migrate        bool
}

type CloseRequest struct {
	Discard bool
}

type Selection struct {
	Camp    string
	Session string
}

type selectionContextKey struct{}

func SelectionFromContext(ctx context.Context) Selection {
	selection, _ := ctx.Value(selectionContextKey{}).(Selection)
	return selection
}

func withSelection(ctx context.Context, selection Selection) context.Context {
	return context.WithValue(ctx, selectionContextKey{}, selection)
}

type doctorLifecycle interface {
	Doctor(context.Context, OutputMode, io.Writer) error
}

type Setup interface {
	Setup(context.Context, OutputMode, io.Reader, io.Writer) error
}

type InteractiveInit interface {
	InitInteractive(context.Context, InitRequest, OutputMode, io.Reader, io.Writer) error
}

type CampLister interface {
	List(context.Context, OutputMode, io.Writer) error
}

type CampStatus interface {
	Status(context.Context, OutputMode, io.Writer) error
}

type ConfigOperations interface {
	ConfigShow(context.Context, bool, bool, OutputMode, io.Writer) error
	ConfigSet(context.Context, string, string, OutputMode, io.Writer) error
}

type StrikeRequest struct {
	Purge bool
	Yes   bool
}

type CampStriker interface {
	Strike(context.Context, StrikeRequest, OutputMode, io.Writer) error
}

type SessionRequest struct {
	SessionID string
	Capsule   string
	Branch    string
}

type ServeRequest struct {
	Session SessionRequest
	Service string
}

type ServeLogsRequest struct {
	Session   SessionRequest
	Service   string
	TailBytes int64
}

type ServeRestartRequest struct {
	Session     SessionRequest
	Service     string
	LaunchToken string
}

type ImageOperations interface {
	ImagesList(context.Context, SessionRequest, OutputMode, io.Writer) error
	ImagesRestore(context.Context, SessionRequest, OutputMode, io.Writer) error
}

type ServeOperations interface {
	ServeStatus(context.Context, ServeRequest, OutputMode, io.Writer) error
	ServeLogs(context.Context, ServeLogsRequest, OutputMode, io.Writer) error
	ServeRestart(context.Context, ServeRestartRequest, OutputMode, io.Writer) error
}

type ProviderLister interface {
	ProvidersList(context.Context, OutputMode, io.Writer) error
}

type ProviderMutationRequest struct {
	Name    string
	Context string
	Options []string
}

type ProviderConfigurer interface {
	ProviderAdd(context.Context, ProviderMutationRequest, OutputMode, io.Writer) error
	ProviderUse(context.Context, ProviderMutationRequest, OutputMode, io.Writer) error
}

type KitReader interface {
	KitInspect(context.Context, string, OutputMode, io.Writer) error
	KitVerify(context.Context, string, OutputMode, io.Writer) error
}

type KitExportRequest struct {
	Generation string
	Output     string
}

type KitExporter interface {
	KitExport(context.Context, KitExportRequest, OutputMode, io.Writer) error
}

type KitImportRequest struct {
	File string
	Camp string
}

type KitImporter interface {
	KitImport(context.Context, KitImportRequest, OutputMode, io.Writer) error
}

func NewRootWithLifecycle(lifecycle Lifecycle) *cobra.Command {
	root := &cobra.Command{
		Use:           "camp",
		Short:         "Recoverable capsule workspaces",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			return UsageError(fmt.Errorf("unknown command %q for %q", args[0], "camp"))
		},
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	root.PersistentFlags().Bool("json", false, "emit stable JSON output")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return UsageError(err)
	})
	root.DisableAutoGenTag = true
	root.AddCommand(newCompletionCommand(root))
	if diagnostics, ok := lifecycle.(doctorLifecycle); ok {
		root.AddCommand(noArgumentCommand("doctor", "Diagnose required host capabilities", diagnostics.Doctor))
	}
	if setup, ok := lifecycle.(Setup); ok {
		root.AddCommand(setupCommand(setup.Setup))
	}
	if camps, ok := lifecycle.(CampLister); ok {
		root.AddCommand(noArgumentCommand("list", "List stored camps", camps.List))
	}
	if status, ok := lifecycle.(CampStatus); ok {
		root.AddCommand(selectionCommand("status", "Show the selected camp session", status.Status))
	}
	if configuration, ok := lifecycle.(ConfigOperations); ok {
		root.AddCommand(newConfigCommand(configuration))
	}
	if striker, ok := lifecycle.(CampStriker); ok {
		root.AddCommand(newStrikeCommand(striker.Strike))
	}
	var interactiveInit func(context.Context, InitRequest, OutputMode, io.Reader, io.Writer) error
	if initLifecycle, ok := lifecycle.(InteractiveInit); ok {
		interactiveInit = initLifecycle.InitInteractive
	}
	if imageOperations, ok := lifecycle.(ImageOperations); ok {
		root.AddCommand(newImagesCommand(imageOperations))
	}
	if serveOperations, ok := lifecycle.(ServeOperations); ok {
		root.AddCommand(newServeCommand(serveOperations))
	}
	if providers, ok := lifecycle.(ProviderLister); ok {
		configurer, _ := lifecycle.(ProviderConfigurer)
		root.AddCommand(newProviderCommand(providers.ProvidersList, configurer))
	}
	if kitReader, ok := lifecycle.(KitReader); ok {
		var exporter func(context.Context, KitExportRequest, OutputMode, io.Writer) error
		if kitExporter, ok := lifecycle.(KitExporter); ok {
			exporter = kitExporter.KitExport
		}
		var importer func(context.Context, KitImportRequest, OutputMode, io.Writer) error
		if kitImporter, ok := lifecycle.(KitImporter); ok {
			importer = kitImporter.KitImport
		}
		root.AddCommand(newKitCommand(kitReader.KitInspect, kitReader.KitVerify, exporter, importer))
	}
	root.AddCommand(
		newInitCommand(lifecycle.Init, interactiveInit),
		optionalArgumentCommand("open", "Open a capsule workspace", lifecycle.Open),
		newAttachCommand(lifecycle.Attach),
		noArgumentCommand("sync", "Publish a checkpoint and remain open", lifecycle.Sync),
		newCloseCommand(lifecycle.Close),
		optionalArgumentCommand("reopen", "Reopen a closed capsule workspace", lifecycle.Reopen),
		optionalArgumentCommand("recover", "Recover an interrupted lifecycle", lifecycle.Recover),
		hiddenRequiredArgumentCommand("supervise", lifecycle.Supervise),
	)
	return root
}

func RunRemoteWorker(ctx context.Context, streams Streams) error {
	return remoteworker.Run(ctx, streams.In, streams.Out, streams.ErrOut)
}

func newConfigCommand(operations ConfigOperations) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Inspect and update Camp configuration", Args: usageArgs(cobra.NoArgs)}
	showEffective := false
	show := &cobra.Command{
		Use: "show", Short: "Show Camp configuration", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return operations.ConfigShow(command.Context(), showEffective, true, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	show.Flags().BoolVar(&showEffective, "effective", false, "resolve defaults, environment, and flags")
	set := &cobra.Command{
		Use: "set KEY VALUE", Short: "Persist one supported Camp configuration value", Args: usageArgs(cobra.ExactArgs(2)),
		RunE: func(command *cobra.Command, args []string) error {
			return operations.ConfigSet(command.Context(), args[0], args[1], OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.AddCommand(show, set)
	return command
}

func newImagesCommand(operations ImageOperations) *cobra.Command {
	command := &cobra.Command{Use: "images", Short: "Inspect and reconcile workspace images", Args: usageArgs(cobra.NoArgs)}

	listRequest := SessionRequest{}
	list := &cobra.Command{
		Use: "list", Short: "List recorded workspace images", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return operations.ImagesList(command.Context(), listRequest, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSessionFlags(list, &listRequest)

	captureRequest := SessionRequest{}
	capture := &cobra.Command{
		Use:   "capture",
		Short: "Explain registry-only image capture",
		Long:  "Camp captures only images explicitly pushed through CAMP_REGISTRY during camp sync or camp close.",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return ErrImagesCaptureRequiresCheckpoint
		},
	}
	addSessionFlags(capture, &captureRequest)

	restoreRequest := SessionRequest{}
	restore := &cobra.Command{
		Use: "restore", Short: "Restore recorded workspace images", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return operations.ImagesRestore(command.Context(), restoreRequest, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSessionFlags(restore, &restoreRequest)
	command.AddCommand(list, capture, restore)
	return command
}

func newKitCommand(inspect func(context.Context, string, OutputMode, io.Writer) error, verify func(context.Context, string, OutputMode, io.Writer) error, exporter func(context.Context, KitExportRequest, OutputMode, io.Writer) error, importer func(context.Context, KitImportRequest, OutputMode, io.Writer) error) *cobra.Command {
	command := &cobra.Command{
		Use: "kit", Short: "Inspect and verify CampKit archives", Args: usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	command.AddCommand(
		&cobra.Command{
			Use: "inspect [file]", Short: "Inspect a CampKit archive", Args: usageArgs(cobra.ExactArgs(1)),
			RunE: func(command *cobra.Command, args []string) error {
				if err := validateRegularFile(args[0]); err != nil {
					return UsageError(err)
				}
				return inspect(command.Context(), args[0], OutputModeFrom(command), command.OutOrStdout())
			},
		},
		&cobra.Command{
			Use: "verify [file]", Short: "Verify archive integrity and manifest consistency", Args: usageArgs(cobra.ExactArgs(1)),
			RunE: func(command *cobra.Command, args []string) error {
				if err := validateRegularFile(args[0]); err != nil {
					return UsageError(err)
				}
				return verify(command.Context(), args[0], OutputModeFrom(command), command.OutOrStdout())
			},
		},
	)
	if exporter != nil {
		request := KitExportRequest{}
		export := &cobra.Command{
			Use: "export", Short: "Export an exact CampKit generation", Args: usageArgs(cobra.NoArgs),
			RunE: func(command *cobra.Command, _ []string) error {
				if request.Generation == "" || request.Output == "" {
					return UsageError(fmt.Errorf("--generation and --output are required"))
				}
				return exporter(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
			},
		}
		export.Flags().StringVar(&request.Generation, "generation", "", "exact generation reference")
		export.Flags().StringVar(&request.Output, "output", "", "CampKit output file")
		command.AddCommand(export)
	}
	if importer != nil {
		request := KitImportRequest{}
		importCommand := &cobra.Command{
			Use: "import [file]", Short: "Import a verified CampKit into a new local camp", Args: usageArgs(cobra.ExactArgs(1)),
			RunE: func(command *cobra.Command, args []string) error {
				if err := validateRegularFile(args[0]); err != nil {
					return UsageError(err)
				}
				if request.Camp == "" {
					return UsageError(fmt.Errorf("--as is required"))
				}
				request.File = args[0]
				return importer(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
			},
		}
		importCommand.Flags().StringVar(&request.Camp, "as", "", "new local camp name")
		command.AddCommand(importCommand)
	}
	return command
}

func newServeCommand(operations ServeOperations) *cobra.Command {
	command := &cobra.Command{Use: "serve", Short: "Inspect and restart Camp-managed services", Args: usageArgs(cobra.NoArgs)}

	statusRequest := ServeRequest{}
	status := &cobra.Command{
		Use: "status [service]", Short: "Show observed service status", Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			statusRequest.Service = args[0]
			return operations.ServeStatus(command.Context(), statusRequest, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSessionFlags(status, &statusRequest.Session)

	logsRequest := ServeLogsRequest{TailBytes: 64 * 1024}
	logs := &cobra.Command{
		Use: "logs [service]", Short: "Read bounded service logs", Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			if logsRequest.TailBytes <= 0 {
				return UsageError(errors.New("--tail-bytes must be greater than zero"))
			}
			logsRequest.Service = args[0]
			return operations.ServeLogs(command.Context(), logsRequest, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSessionFlags(logs, &logsRequest.Session)
	logs.Flags().Int64Var(&logsRequest.TailBytes, "tail-bytes", logsRequest.TailBytes, "maximum log bytes to read")

	restartRequest := ServeRestartRequest{}
	restart := &cobra.Command{
		Use: "restart [service]", Short: "Restart a recorded service", Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			if restartRequest.LaunchToken == "" {
				return UsageError(errors.New("--launch-token is required"))
			}
			restartRequest.Service = args[0]
			return operations.ServeRestart(command.Context(), restartRequest, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSessionFlags(restart, &restartRequest.Session)
	restart.Flags().StringVar(&restartRequest.LaunchToken, "launch-token", "", "new unique service launch token")

	command.AddCommand(status, logs, restart)
	return command
}

func newProviderCommand(list func(context.Context, OutputMode, io.Writer) error, configurer ProviderConfigurer) *cobra.Command {
	command := &cobra.Command{
		Use: "provider", Short: "Inspect configured DevPod providers", Args: usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	command.AddCommand(noArgumentCommand("list", "List configured DevPod providers", list))
	if configurer != nil {
		command.AddCommand(
			newProviderMutationCommand("add", "Add or repair a built-in DevPod provider", configurer.ProviderAdd),
			newProviderMutationCommand("use", "Select an existing DevPod provider", configurer.ProviderUse),
		)
	}
	return command
}

func newProviderMutationCommand(use, short string, run func(context.Context, ProviderMutationRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := ProviderMutationRequest{}
	command := &cobra.Command{
		Use: use + " NAME", Short: short, Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			request.Name = args[0]
			return run(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&request.Context, "context", "", "DevPod context (defaults to Camp configuration)")
	command.Flags().StringArrayVarP(&request.Options, "option", "o", nil, "provider option in KEY=VALUE form")
	return command
}

func addSessionFlags(command *cobra.Command, request *SessionRequest) {
	command.Flags().StringVar(&request.SessionID, "session", "", "select an exact session ID")
	command.Flags().StringVar(&request.Capsule, "capsule", "", "select a capsule")
	command.Flags().StringVar(&request.Branch, "branch", "", "select a branch")
}

func newStrikeCommand(run func(context.Context, StrikeRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := StrikeRequest{}
	command := &cobra.Command{
		Use: "strike", Short: "Archive local Camp state and start fresh", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().BoolVar(&request.Purge, "purge", false, "permanently remove verified local Camp state")
	command.Flags().BoolVar(&request.Yes, "yes", false, "confirm permanent purge")
	return command
}

func newCloseCommand(run func(context.Context, CloseRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := CloseRequest{}
	selection := Selection{}
	command := &cobra.Command{
		Use: "close", Short: "Publish a checkpoint and close", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(withSelection(command.Context(), selection), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().BoolVar(&request.Discard, "discard", false, "close without publishing the open session")
	addSelectionFlags(command, &selection)
	return command
}

func setupCommand(run func(context.Context, OutputMode, io.Reader, io.Writer) error) *cobra.Command {
	return &cobra.Command{
		Use: "setup", Short: "Install or reuse pinned DevPod and Hauler tools", Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command.Context(), OutputModeFrom(command), command.InOrStdin(), command.OutOrStdout())
		},
	}
}

func newAttachCommand(run func(context.Context, AttachRequest, OutputMode, io.Writer) error) *cobra.Command {
	request := AttachRequest{IDE: "none"}
	selection := Selection{}
	var insiders, agentForwarding, gpgAgentForwarding, stdio, installTerminfo bool
	command := &cobra.Command{
		Use: "attach [target]", Short: "Attach to an open capsule workspace",
		Args: func(command *cobra.Command, args []string) error {
			positional := args
			if index := command.ArgsLenAtDash(); index >= 0 {
				positional = args[:index]
			}
			return usageArgs(cobra.MaximumNArgs(1))(command, positional)
		},
		RunE: func(command *cobra.Command, args []string) error {
			positional := args
			if index := command.ArgsLenAtDash(); index >= 0 {
				positional = args[:index]
				request.DevPodArgs = append(request.DevPodArgs, args[index:]...)
			}
			if len(positional) == 1 {
				request.Target = positional[0]
			}
			if insiders {
				if command.Flags().Changed("ide") && request.IDE != "vscode-insiders" {
					return UsageError(errors.New("--insiders conflicts with --ide unless --ide=vscode-insiders"))
				}
				request.IDE = "vscode-insiders"
			}
			if command.Flags().Changed("agent-forwarding") {
				request.AgentForwarding = &agentForwarding
			}
			if command.Flags().Changed("gpg-agent-forwarding") {
				request.GPGAgentForwarding = &gpgAgentForwarding
			}
			if command.Flags().Changed("stdio") {
				request.Stdio = &stdio
			}
			if command.Flags().Changed("install-terminfo") {
				request.InstallTerminfo = &installTerminfo
			}
			return run(withSelection(command.Context(), selection), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	flags := command.Flags()
	flags.StringVar(&request.IDE, "ide", "none", "entry mode: none, vscode, vscode-insiders, or t3-code")
	flags.BoolVar(&insiders, "insiders", false, "alias for --ide=vscode-insiders")
	flags.StringVar(&request.User, "user", "", "SSH user")
	flags.StringSliceVarP(&request.ForwardPorts, "forward-ports", "L", nil, "forward a local port through DevPod SSH")
	flags.StringSliceVarP(&request.ReverseForwardPorts, "reverse-forward-ports", "R", nil, "reverse-forward a port through DevPod SSH")
	flags.StringSliceVar(&request.SendEnv, "send-env", nil, "send an environment variable through DevPod SSH")
	flags.StringSliceVar(&request.SetEnv, "set-env", nil, "set an environment variable in DevPod SSH")
	flags.StringVar(&request.ForwardPortsTimeout, "forward-ports-timeout", "", "DevPod forward-port timeout")
	flags.BoolVar(&agentForwarding, "agent-forwarding", false, "forward the SSH agent")
	flags.BoolVar(&gpgAgentForwarding, "gpg-agent-forwarding", false, "forward the GPG agent")
	flags.BoolVar(&stdio, "stdio", false, "attach SSH to standard I/O")
	flags.StringVar(&request.SSHKeepAliveInterval, "ssh-keepalive-interval", "", "SSH keepalive interval")
	flags.StringVar(&request.GitSSHSigningKey, "git-ssh-signing-key", "", "Git SSH signing key path")
	flags.StringVar(&request.TermMode, "term-mode", "", "terminal mode")
	flags.BoolVar(&installTerminfo, "install-terminfo", false, "install local terminal information")
	flags.StringSliceVar(&request.DevPodArgs, "devpod-arg", nil, "append one raw DevPod SSH argument")
	addSelectionFlags(command, &selection)
	return command
}

func newInitCommand(run func(context.Context, InitRequest, OutputMode, io.Writer) error, interactive func(context.Context, InitRequest, OutputMode, io.Reader, io.Writer) error) *cobra.Command {
	request := InitRequest{}
	var legacyWorkspaceContext string
	command := &cobra.Command{
		Use: "init [root]", Short: "Initialize a capsule root", Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				request.Root = args[0]
			}
			if command.Flags().Changed("devpod-context") && command.Flags().Changed("workspace-context") {
				return UsageError(errors.New("--devpod-context cannot be combined with --workspace-context"))
			}
			if command.Flags().Changed("workspace-context") {
				request.DevPodContext = legacyWorkspaceContext
			}
			if request.Migrate {
				if request.Root != "" || request.Capsule != "" || command.Flags().Changed("backend") || command.Flags().Changed("workspace-provider") || command.Flags().Changed("devpod-context") || command.Flags().Changed("workspace-context") {
					return UsageError(errors.New("--migrate cannot be combined with a root or camp settings"))
				}
			} else if request.Capsule == "" {
				if OutputModeFrom(command) == ModeHuman && interactive != nil {
					return interactive(command.Context(), request, ModeHuman, command.InOrStdin(), command.OutOrStdout())
				}
				return UsageError(errors.New("init requires --name"))
			}
			return run(command.Context(), request, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&request.Capsule, "name", "", "stable camp ID")
	command.Flags().StringVar(&request.Backend, "backend", "", "camp backend URL (defaults to machine setup)")
	command.Flags().StringVar(&request.DevPodProvider, "workspace-provider", "", "workspace runtime provider (defaults to machine setup)")
	command.Flags().StringVar(&request.DevPodContext, "devpod-context", "", "named DevPod configuration context (defaults to machine setup)")
	command.Flags().StringVar(&legacyWorkspaceContext, "workspace-context", "", "deprecated alias for --devpod-context")
	_ = command.Flags().MarkHidden("workspace-context")
	command.Flags().BoolVar(&request.Migrate, "migrate", false, "migrate the legacy singleton configuration")
	return command
}

func optionalArgumentCommand(use, short string, run func(context.Context, string, OutputMode, io.Writer) error) *cobra.Command {
	selection := Selection{}
	command := &cobra.Command{
		Use: use + " [target]", Short: short, Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			value := ""
			if len(args) == 1 {
				value = args[0]
			}
			if (use == "reopen" || use == "recover") && value != "" {
				if selection.Session != "" && selection.Session != value {
					return UsageError(errors.New("positional session conflicts with --session"))
				}
				selection.Session = value
			}
			return run(withSelection(command.Context(), selection), value, OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSelectionFlags(command, &selection)
	return command
}

func noArgumentCommand(use, short string, run func(context.Context, OutputMode, io.Writer) error) *cobra.Command {
	selection := Selection{}
	command := &cobra.Command{Use: use, Short: short, Args: usageArgs(cobra.NoArgs), RunE: func(command *cobra.Command, _ []string) error {
		return run(withSelection(command.Context(), selection), OutputModeFrom(command), command.OutOrStdout())
	}}
	if use == "sync" {
		addSelectionFlags(command, &selection)
	}
	return command
}

func addSelectionFlags(command *cobra.Command, selection *Selection) {
	command.Flags().StringVar(&selection.Camp, "camp", "", "select a camp by stable ID")
	command.Flags().StringVar(&selection.Session, "session", "", "select a session by ID")
}

func selectionCommand(use, short string, run func(context.Context, OutputMode, io.Writer) error) *cobra.Command {
	selection := Selection{}
	command := &cobra.Command{
		Use: use, Short: short, Args: usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			return run(withSelection(command.Context(), selection), OutputModeFrom(command), command.OutOrStdout())
		},
	}
	addSelectionFlags(command, &selection)
	return command
}

func hiddenRequiredArgumentCommand(use string, run func(context.Context, string, OutputMode, io.Writer) error) *cobra.Command {
	return &cobra.Command{
		Use:    use + " [session]",
		Hidden: true,
		Args:   usageArgs(cobra.ExactArgs(1)),
		RunE: func(command *cobra.Command, args []string) error {
			return run(command.Context(), args[0], OutputModeFrom(command), command.OutOrStdout())
		},
	}
}

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if err := validate(command, args); err != nil {
			return UsageError(err)
		}
		return nil
	}
}

func validateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("expected %s to be a regular file", path)
	}
	return nil
}
