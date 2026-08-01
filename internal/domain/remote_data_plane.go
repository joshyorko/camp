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
	HelperSHA256   string        `json:"helperSHA256,omitempty" yaml:"helperSHA256,omitempty"`
	HelperSize     int64         `json:"helperSize,omitempty" yaml:"helperSize,omitempty"`
	ManifestSHA256 string        `json:"manifestSHA256,omitempty" yaml:"manifestSHA256,omitempty"`
	ManifestSize   int64         `json:"manifestSize,omitempty" yaml:"manifestSize,omitempty"`
	SourceImage    string        `json:"sourceImage,omitempty" yaml:"sourceImage,omitempty"`
	OuterImage     string        `json:"outerImage,omitempty" yaml:"outerImage,omitempty"`
	LifecycleUser  string        `json:"lifecycleUser,omitempty" yaml:"lifecycleUser,omitempty"`
	RequestSchema  uint32        `json:"requestSchema,omitempty" yaml:"requestSchema,omitempty"`
	RequestSession string        `json:"requestSession,omitempty" yaml:"requestSession,omitempty"`
	WorkspaceRoot  string        `json:"workspaceRoot,omitempty" yaml:"workspaceRoot,omitempty"`
	RuntimeRoot    string        `json:"runtimeRoot,omitempty" yaml:"runtimeRoot,omitempty"`
	ManifestPath   string        `json:"manifestPath,omitempty" yaml:"manifestPath,omitempty"`
	Architecture   string        `json:"architecture,omitempty" yaml:"architecture,omitempty"`
	ConfigSHA256   string        `json:"configSHA256,omitempty" yaml:"configSHA256,omitempty"`
	ConfigSize     int64         `json:"configSize,omitempty" yaml:"configSize,omitempty"`
}
