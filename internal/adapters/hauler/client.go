package hauler

import (
	"context"

	"github.com/joshyorko/camp/internal/ports"
)

type AddImageOptions struct {
	Reference string
	Platform  string
	Rewrite   string
	Local     bool
}

type RegistryOptions struct {
	Directory string
	Port      int
	ReadOnly  bool
	LogPath   string
	PIDPath   string
}

type FileserverOptions struct {
	Directory      string
	Port           int
	TimeoutSeconds int
	LogPath        string
	PIDPath        string
}

type Client struct {
	executable string
	runner     ports.Runner
}

func NewClient(executable string, runner ports.Runner) *Client {
	return &Client{executable: executable, runner: runner}
}

func (c *Client) Load(ctx context.Context, store string, filenames []string) (ports.Result, error) {
	argv := storeArgv(store, "load")
	for _, filename := range filenames {
		argv = append(argv, "--filename", filename)
	}
	return c.run(ctx, argv)
}

func (c *Client) Extract(ctx context.Context, store, reference, output string) (ports.Result, error) {
	return c.run(ctx, append(storeArgv(store, "extract"), reference, "--output", output))
}

func (c *Client) Sync(ctx context.Context, store string, manifests []string) (ports.Result, error) {
	argv := storeArgv(store, "sync")
	for _, manifest := range manifests {
		argv = append(argv, "--filename", manifest)
	}
	return c.run(ctx, argv)
}

func (c *Client) SyncFrom(ctx context.Context, store string, manifests []string, directory string) (ports.Result, error) {
	argv := storeArgv(store, "sync")
	for _, manifest := range manifests {
		argv = append(argv, "--filename", manifest)
	}
	return c.runner.Run(ctx, ports.Command{Executable: c.executable, Argv: argv, Directory: directory})
}

func (c *Client) AddImage(ctx context.Context, store string, options AddImageOptions) (ports.Result, error) {
	argv := append(storeArgv(store, "add", "image"), options.Reference)
	if options.Platform != "" {
		argv = append(argv, "--platform", options.Platform)
	}
	if options.Rewrite != "" {
		argv = append(argv, "--rewrite", options.Rewrite)
	}
	if options.Local {
		argv = append(argv, "--local")
	}
	return c.run(ctx, argv)
}

func (c *Client) AddFile(ctx context.Context, store, path, name string) (ports.Result, error) {
	return c.run(ctx, append(storeArgv(store, "add", "file"), path, "--name", name))
}

func (c *Client) Save(ctx context.Context, store, filename string) (ports.Result, error) {
	return c.run(ctx, append(storeArgv(store, "save"), "--filename", filename))
}

func (c *Client) Info(ctx context.Context, store string) (ports.Result, error) {
	return c.run(ctx, append(storeArgv(store, "info"), "--output", "json", "--digests"))
}

func (c *Client) Registry(store string, options RegistryOptions) ports.Command {
	definition := serviceDefinitionForClient(c.executable, store, options.Directory, options.Port, options.LogPath, options.PIDPath, RegistryServiceName, options.ReadOnly, 0)
	return definition.command()
}

func (c *Client) Fileserver(store string, options FileserverOptions) ports.Command {
	definition := serviceDefinitionForClient(c.executable, store, options.Directory, options.Port, options.LogPath, options.PIDPath, FileserverServiceName, false, options.TimeoutSeconds)
	return definition.command()
}

func storeArgv(store string, command ...string) []string {
	argv := []string{"store", "--store", store}
	return append(argv, command...)
}

func (c *Client) run(ctx context.Context, argv []string) (ports.Result, error) {
	return c.runner.Run(ctx, ports.Command{Executable: c.executable, Argv: argv})
}
