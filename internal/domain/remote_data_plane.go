package domain

type DataPlaneMode string

const (
	DataPlaneLegacyMirror DataPlaneMode = "legacyMirror"
	DataPlaneHaulerKitV1  DataPlaneMode = "haulerKitV1"
)

type RemoteDataPlaneRecord struct {
	Mode           DataPlaneMode `json:"mode" yaml:"mode"`
	AttemptID      string        `json:"attemptId" yaml:"attemptId"`
	BootstrapRoot  string        `json:"bootstrapRoot,omitempty" yaml:"bootstrapRoot,omitempty"`
	KitSHA256      string        `json:"kitSHA256,omitempty" yaml:"kitSHA256,omitempty"`
	KitSize        int64         `json:"kitSize,omitempty" yaml:"kitSize,omitempty"`
	ManifestSHA256 string        `json:"manifestSHA256,omitempty" yaml:"manifestSHA256,omitempty"`
	ManifestSize   int64         `json:"manifestSize,omitempty" yaml:"manifestSize,omitempty"`
	OuterImage     string        `json:"outerImage,omitempty" yaml:"outerImage,omitempty"`
}
