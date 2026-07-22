//go:build linux

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	campcontract "github.com/joshyorko/camp"
	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/config"
)

type toolEnsurer interface {
	Ensure(context.Context, string, string, string) (tooladapter.Resolution, error)
}

type setupResult struct {
	Tools      []tooladapter.Resolution `json:"tools"`
	PATH       []string                 `json:"path"`
	PATHExport string                   `json:"pathExport,omitempty"`
}

func (p *ProductionLifecycle) Setup(ctx context.Context, mode OutputMode, out io.Writer) error {
	lockBytes := campcontract.DistributionToolLock()
	if err := runProductionToolSetup(ctx, mode, out, lockBytes, "", environmentMap(os.Environ()), runtime.GOOS, runtime.GOARCH); err != nil || mode == ModeJSON {
		return err
	}
	return renderProductionSetupCampsite(ctx, out, lockBytes)
}

func runProductionToolSetup(ctx context.Context, mode OutputMode, out io.Writer, lockBytes []byte, home string, environment map[string]string, goos, arch string, options ...tooladapter.InstallerOption) error {
	lock, err := tooladapter.ParseLock(bytes.NewReader(lockBytes))
	if err != nil {
		return err
	}
	paths, err := config.ResolveXDGPaths(config.XDGInput{Home: home, Environment: environment})
	if err != nil {
		return err
	}
	installer, err := tooladapter.NewInstaller(lock, paths.DataRoot, options...)
	if err != nil {
		return err
	}
	return runManagedToolSetup(ctx, mode, out, installer, goos, arch)
}

func runManagedToolSetup(ctx context.Context, mode OutputMode, out io.Writer, ensurer toolEnsurer, goos, arch string) error {
	result := setupResult{Tools: make([]tooladapter.Resolution, 0, 2)}
	for _, name := range []string{"devpod", "hauler"} {
		resolution, err := ensurer.Ensure(ctx, name, goos, arch)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", name, err)
		}
		result.Tools = append(result.Tools, resolution)
		if resolution.Managed {
			result.PATH = append(result.PATH, filepath.Dir(resolution.Path))
		}
	}
	if len(result.PATH) > 0 {
		result.PATHExport = `export PATH="` + strings.Join(result.PATH, ":") + `:$PATH"`
	}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "setup", result, "")
	}
	for index, name := range []string{"devpod", "hauler"} {
		resolution := result.Tools[index]
		if _, err := fmt.Fprintf(out, "%s %s ready at %s (asset sha256 %s; binary sha256 %s)\n", name, resolution.Version, resolution.Path, resolution.AssetSHA256, resolution.BinarySHA256); err != nil {
			return err
		}
	}
	if result.PATHExport != "" {
		_, err := fmt.Fprintf(out, "Add managed tools to PATH for this shell:\n%s\n", result.PATHExport)
		return err
	}
	return nil
}
