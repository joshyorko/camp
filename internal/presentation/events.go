package presentation

// The lifecycle scene has stable, fact-based waypoints. They identify a
// completed or active product fact; they never represent elapsed time or a
// percentage inferred from process output.
const (
	StageHydrate      LifecycleStage = "hydrate"
	StageServices     LifecycleStage = "services"
	StageDevPod       LifecycleStage = "devpod"
	StageAttach       LifecycleStage = "attach"
	StageMirror       LifecycleStage = "mirror"
	StageImageCapture LifecycleStage = "image-capture"
	StageArchive      LifecycleStage = "archive"
	StageUpload       LifecycleStage = "upload"
	StagePointer      LifecycleStage = "pointer"
	StageCleanup      LifecycleStage = "cleanup"
	StageRecovery     LifecycleStage = "recovery"
)

// RichLifecycleEventKind distinguishes activity from a proven lifecycle fact.
// It lets the TUI show the current real operation without treating its text as
// a protocol or advancing the scene before the application confirms a fact.
type RichLifecycleEventKind uint8

const (
	RichLifecycleActivity RichLifecycleEventKind = iota
	RichLifecycleCompleted
	RichLifecycleSucceeded
	RichLifecycleFailed
)

// RichLifecycleEvent is the typed presentation contract for the interactive
// lifecycle UI. CLI adapters construct it directly from application facts;
// subprocess text is never parsed to synthesize a stage.
type RichLifecycleEvent struct {
	Kind            RichLifecycleEventKind
	Stage           LifecycleStage
	Message         string
	RecoveryCommand string
}

// VisualLifecycleStages returns a new ordered slice so callers cannot mutate
// the canonical stage sequence shared by lifecycle scenes and adapters.
func VisualLifecycleStages() []LifecycleStage {
	return []LifecycleStage{
		StageHydrate,
		StageServices,
		StageDevPod,
		StageAttach,
		StageMirror,
		StageImageCapture,
		StageArchive,
		StageUpload,
		StagePointer,
		StageCleanup,
		StageRecovery,
	}
}

// LifecycleStageLabel is intentionally a closed mapping: unknown stage IDs
// are not rendered as authoritative lifecycle progress.
func LifecycleStageLabel(stage LifecycleStage) string {
	switch stage {
	case StageHydrate:
		return "HYDRATE"
	case StageServices:
		return "SERVICES"
	case StageDevPod:
		return "DEVPOD"
	case StageAttach:
		return "ATTACH"
	case StageMirror:
		return "MIRROR"
	case StageImageCapture:
		return "IMAGE CAPTURE"
	case StageArchive:
		return "ARCHIVE"
	case StageUpload:
		return "UPLOAD"
	case StagePointer:
		return "POINTER"
	case StageCleanup:
		return "CLEANUP"
	case StageRecovery:
		return "RECOVERY"
	default:
		return ""
	}
}
