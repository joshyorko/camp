package app

import "context"

type ProgressStage string

const (
	ProgressSnapshottingRoot         ProgressStage = "snapshotting-root"
	ProgressDownloadingRoomImage     ProgressStage = "downloading-room-image"
	ProgressBuildingHaulerKit        ProgressStage = "building-hauler-kit"
	ProgressVerifyingHaulerKit       ProgressStage = "verifying-hauler-kit"
	ProgressWorkspacePrepared        ProgressStage = "workspace-prepared"
	ProgressImagesCaptured           ProgressStage = "images-captured"
	ProgressRegistrySealed           ProgressStage = "registry-sealed"
	ProgressGenerationBuilt          ProgressStage = "generation-built"
	ProgressGenerationUploaded       ProgressStage = "generation-uploaded"
	ProgressGenerationPublished      ProgressStage = "generation-published"
	ProgressServingRefreshed         ProgressStage = "serving-refreshed"
	ProgressWorkspaceClosed          ProgressStage = "workspace-closed"
	ProgressForwardersStopped        ProgressStage = "forwarders-stopped"
	ProgressServicesStopped          ProgressStage = "services-stopped"
	ProgressSupervisorStopped        ProgressStage = "supervisor-stopped"
	ProgressSessionArtifactsRemoved  ProgressStage = "session-artifacts-removed"
	ProgressLeaseReleased            ProgressStage = "lease-released"
	ProgressMaterializationRemoved   ProgressStage = "materialization-removed"
	ProgressMaterializationPreserved ProgressStage = "materialization-preserved"
)

type ProgressEvent struct {
	Stage      ProgressStage `json:"stage"`
	Message    string        `json:"message,omitempty"`
	Complete   bool          `json:"complete,omitempty"`
	Generation uint64        `json:"generation,omitempty"`
	ImageCount int           `json:"imageCount,omitempty"`
	Bytes      int64         `json:"bytes,omitempty"`
}

type ProgressReporter interface {
	Report(context.Context, ProgressEvent) error
}

type ProgressFunc func(context.Context, ProgressEvent) error

func (f ProgressFunc) Report(ctx context.Context, event ProgressEvent) error {
	if f == nil {
		return nil
	}
	return f(ctx, event)
}

type progressContextKey struct{}

func WithProgressReporter(ctx context.Context, reporter ProgressReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressContextKey{}, reporter)
}

func reportProgress(ctx context.Context, event ProgressEvent) error {
	reporter, _ := ctx.Value(progressContextKey{}).(ProgressReporter)
	if reporter == nil {
		return nil
	}
	return reporter.Report(ctx, event)
}
