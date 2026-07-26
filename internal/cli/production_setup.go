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
	"github.com/joshyorko/camp/internal/doctor"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/presentation"
	"github.com/joshyorko/camp/internal/setupui"
)

type toolEnsurer interface {
	Ensure(context.Context, string, string, string) (tooladapter.Resolution, error)
}

type toolInspector interface {
	Inspect(context.Context, string, string, string) (tooladapter.Resolution, error)
}

type doctorManagedToolResolver struct {
	inspector toolInspector
	goos      string
	arch      string
}

func (r doctorManagedToolResolver) Inspect(ctx context.Context, name string) (doctor.ManagedToolIdentity, error) {
	resolution, err := r.inspector.Inspect(ctx, name, r.goos, r.arch)
	if err != nil {
		return doctor.ManagedToolIdentity{}, err
	}
	return doctor.ManagedToolIdentity{
		Path: resolution.Path, Repository: resolution.Repository, Version: resolution.Version,
		BinarySHA256: resolution.BinarySHA256, Managed: resolution.Managed,
	}, nil
}

func newDoctorManagedToolResolver(lockBytes []byte, dataRoot string) (doctorManagedToolResolver, error) {
	lock, err := tooladapter.ParseLock(bytes.NewReader(lockBytes))
	if err != nil {
		return doctorManagedToolResolver{}, err
	}
	installer, err := tooladapter.NewInstaller(lock, dataRoot)
	if err != nil {
		return doctorManagedToolResolver{}, err
	}
	return doctorManagedToolResolver{inspector: installer, goos: runtime.GOOS, arch: runtime.GOARCH}, nil
}

type setupResult struct {
	Tools      []tooladapter.Resolution `json:"tools"`
	PATH       []string                 `json:"path"`
	PATHExport string                   `json:"pathExport,omitempty"`
}

type managedToolPaths struct {
	devpod string
	hauler string
}

func resolveManagedToolPaths(ctx context.Context, ensurer toolEnsurer, goos, arch string) (managedToolPaths, error) {
	devpodResolution, err := ensurer.Ensure(ctx, "devpod", goos, arch)
	if err != nil {
		return managedToolPaths{}, fmt.Errorf("prepare devpod: %w", err)
	}
	haulerResolution, err := ensurer.Ensure(ctx, "hauler", goos, arch)
	if err != nil {
		return managedToolPaths{}, fmt.Errorf("prepare hauler: %w", err)
	}
	return managedToolPaths{devpod: devpodResolution.Path, hauler: haulerResolution.Path}, nil
}

func (p *ProductionLifecycle) Setup(ctx context.Context, mode OutputMode, in io.Reader, out io.Writer) error {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return err
	}
	experience, width, height := resolveTerminalExperience(mode, out, environment, probeTerminal)
	if mode == ModeHuman {
		if _, statErr := os.Stat(paths.ConfigPath); statErr != nil {
			if !os.IsNotExist(statErr) {
				return statErr
			}
			// Rich interactive path: a truecolor TTY with a real keyboard and
			// minimum dimensions runs the full-screen Trailhead scene from the
			// first prompt through CAMP IS READY. Everything else (plain, JSON,
			// non-TTY, piped input, undersized terminals) keeps the
			// deterministic line-based flow below.
			if canUseRichSetup(experience, width, height) && inputIsTTY(in) {
				handled, err := p.runRichSetup(ctx, in, out, setupPromptDefaults{
					Backend: "file://" + filepath.Join(paths.DataRoot, "backend"),
				})
				if handled {
					return err
				}
			}
			request, err := promptSetupRequest(in, out, setupPromptDefaults{
				Backend: "file://" + filepath.Join(paths.DataRoot, "backend"),
			}, experience, presentation.ScreenSize{Width: width, Height: height})
			if err != nil {
				return err
			}
			if _, err := persistSetupDefaults(paths.ConfigPath, request); err != nil {
				return err
			}
		}
	}
	lockBytes := campcontract.DistributionToolLock()
	completed := func(name string, resolution tooladapter.Resolution) error {
		return writeLifecycleEvents(out, experience, "setup", presentation.LifecycleEvent{Stage: presentation.StageToolReady, Message: fmt.Sprintf("%s %s is ready", name, resolution.Version)})
	}
	if mode == ModeJSON {
		completed = nil
	}
	if err := runProductionToolSetupWithEvents(ctx, mode, out, lockBytes, "", environment, runtime.GOOS, runtime.GOARCH, completed); err != nil || mode == ModeJSON {
		if err != nil && mode == ModeHuman {
			_ = renderProductionSetupFailure(ctx, out, lockBytes, experience, width, height, err)
		}
		return err
	}
	return renderProductionSetupCampsite(ctx, out, lockBytes, experience, width, height)
}

func persistSetupDefaults(path string, request InitRequest) (config.Persistent, error) {
	return config.NewStore(path).Modify(func(value *config.Persistent) error {
		value.DefaultCapsule = ""
		value.Source = ""
		value.Backend = request.Backend
		value.DevPodProvider = request.DevPodProvider
		value.DevPodContext = request.DevPodContext
		return nil
	})
}

func canUseRichSetup(experience presentation.TerminalExperience, width, height int) bool {
	return experience == presentation.TerminalColor && width >= setupui.MinWidth && height >= setupui.MinHeight
}

func renderProductionSetupFailure(ctx context.Context, out io.Writer, lockBytes []byte, experience presentation.TerminalExperience, width, height int, cause error) error {
	lock, err := tooladapter.ParseLock(bytes.NewReader(lockBytes))
	if err != nil {
		return err
	}
	settings, err := resolveProductionSettings()
	if err != nil {
		return err
	}
	if settings.runtime.Source == "" {
		return nil
	}
	j, err := journalstore.NewStore(settings.paths.DataRoot)
	if err != nil {
		return err
	}
	sessions, err := j.List(ctx)
	if err != nil {
		return err
	}
	model, err := buildCampsiteModel(lock, settings.runtime, settings.backend, sessions)
	if err != nil {
		return err
	}
	animator, err := presentation.NewSetupAnimator(out, experience, model, presentation.ScreenSize{Width: width, Height: height})
	if err != nil {
		return err
	}
	return animator.Fail(ctx, presentation.SetupToolchain, cause, "camp setup")
}

func runProductionToolSetup(ctx context.Context, mode OutputMode, out io.Writer, lockBytes []byte, home string, environment map[string]string, goos, arch string, options ...tooladapter.InstallerOption) error {
	return runProductionToolSetupWithEvents(ctx, mode, out, lockBytes, home, environment, goos, arch, nil, options...)
}

func runProductionToolSetupWithEvents(ctx context.Context, mode OutputMode, out io.Writer, lockBytes []byte, home string, environment map[string]string, goos, arch string, completed func(string, tooladapter.Resolution) error, options ...tooladapter.InstallerOption) error {
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
	return runManagedToolSetupWithEvents(ctx, mode, out, installer, goos, arch, completed)
}

func runManagedToolSetup(ctx context.Context, mode OutputMode, out io.Writer, ensurer toolEnsurer, goos, arch string) error {
	return runManagedToolSetupWithEvents(ctx, mode, out, ensurer, goos, arch, nil)
}

func runManagedToolSetupWithEvents(ctx context.Context, mode OutputMode, out io.Writer, ensurer toolEnsurer, goos, arch string, completed func(string, tooladapter.Resolution) error) error {
	result := setupResult{Tools: make([]tooladapter.Resolution, 0, 2)}
	for _, name := range []string{"devpod", "hauler"} {
		resolution, err := ensurer.Ensure(ctx, name, goos, arch)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", name, err)
		}
		result.Tools = append(result.Tools, resolution)
		if completed != nil {
			if err := completed(name, resolution); err != nil {
				return err
			}
		}
		if resolution.Managed {
			result.PATH = append(result.PATH, filepath.Dir(resolution.Path))
		}
	}
	if len(result.PATH) > 0 {
		result.PATHExport = "export PATH=" + shellQuoteArgument(strings.Join(result.PATH, ":")+":") + `"$PATH"`
	}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "setup", result, "")
	}
	return nil
}
