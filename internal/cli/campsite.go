package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/domain"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/presentation"
)

func renderProductionSetupCampsite(ctx context.Context, out io.Writer, lockBytes []byte, experience presentation.TerminalExperience, width, height int) error {
	lock, err := tooladapter.ParseLock(bytes.NewReader(lockBytes))
	if err != nil {
		return err
	}
	settings, err := resolveProductionSettings()
	if err != nil {
		return err
	}
	if settings.runtime.Source == "" {
		return writeLifecycleEvents(out, experience, "setup",
			presentation.LifecycleEvent{Stage: presentation.StageToolReady, Message: "locked tools are ready"},
			presentation.LifecycleEvent{Stage: presentation.StageComplete, Message: "campsite configuration is not yet persisted; run camp init --help"},
		)
	}
	journal, err := journalstore.NewStore(settings.paths.DataRoot)
	if err != nil {
		return err
	}
	sessions, err := journal.List(ctx)
	if err != nil {
		return err
	}
	model, err := buildCampsiteModel(lock, settings.runtime, settings.backend, sessions)
	if err != nil {
		return err
	}
	animator, err := presentation.NewSetupAnimator(out, experience, model)
	if err != nil {
		return err
	}
	for _, waypoint := range []presentation.SetupWaypoint{presentation.SetupToolchain, presentation.SetupRuntime, presentation.SetupCapsule, presentation.SetupStorage} {
		if err := animator.Advance(ctx, waypoint); err != nil {
			return err
		}
	}
	return nil
}

func buildCampsiteModel(lock tooladapter.Lock, runtime config.Runtime, backend config.Backend, sessions []domain.JournalSnapshot) (presentation.CampsiteModel, error) {
	devpod, ok := lock.Tools["devpod"]
	if !ok {
		return presentation.CampsiteModel{}, fmt.Errorf("distribution lock has no devpod")
	}
	hauler, ok := lock.Tools["hauler"]
	if !ok {
		return presentation.CampsiteModel{}, fmt.Errorf("distribution lock has no hauler")
	}
	provider, contextName := runtime.DevPodProvider, runtime.DevPodContext
	runtimeKind := "remote DevPod"
	if provider == "" || provider == "docker" || provider == "podman" {
		runtimeKind = "local DevPod"
	}
	latest := latestCampsiteSession(sessions, runtime.Capsule)
	if latest != nil {
		if latest.Workspace.Provider != "" {
			provider = latest.Workspace.Provider
		}
		if latest.Workspace.Context != "" {
			contextName = latest.Workspace.Context
		}
		if latest.Workspace.LocalProvider {
			runtimeKind = "local DevPod"
		}
	}
	if provider == "" {
		provider = "default"
	}
	if contextName == "" {
		contextName = "default"
	}
	storage := backend.SanitizedURL + " · " + campsiteGeneration(latest)
	model := presentation.CampsiteModel{
		DevPod: presentation.ToolIdentity{Name: "DevPod", Version: devpod.Version}, Hauler: presentation.ToolIdentity{Name: "Hauler", Version: hauler.Version},
		Provider: provider, RuntimeKind: runtimeKind, Context: contextName, Capsule: runtime.Capsule, Source: runtime.Source,
		BackendKind: string(backend.Kind), Storage: storage, NextCommand: "camp open " + runtime.Source,
	}
	var sink strings.Builder
	if err := presentation.RenderCampsite(&sink, model, presentation.CampsiteOptions{}); err != nil {
		return presentation.CampsiteModel{}, err
	}
	return model, nil
}

func latestCampsiteSession(sessions []domain.JournalSnapshot, capsule string) *domain.JournalSnapshot {
	var latest *domain.JournalSnapshot
	for index := range sessions {
		candidate := &sessions[index]
		if candidate.Capsule != capsule {
			continue
		}
		if latest == nil || candidate.UpdatedAt.After(latest.UpdatedAt) {
			latest = candidate
		}
	}
	return latest
}

func campsiteGeneration(snapshot *domain.JournalSnapshot) string {
	if snapshot == nil {
		return "no committed generation"
	}
	for _, generation := range []*domain.GenerationRef{snapshot.Checkpoint.Generation, snapshot.CurrentBase, snapshot.OpenedGeneration} {
		if generation != nil {
			return fmt.Sprintf("generation %d", generation.Generation)
		}
	}
	return "no committed generation"
}
