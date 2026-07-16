package domain

type SessionPaths struct {
	DataRoot    string `json:"dataRoot" yaml:"dataRoot"`
	WorkRoot    string `json:"workRoot" yaml:"workRoot"`
	StoreRoot   string `json:"storeRoot" yaml:"storeRoot"`
	SessionRoot string `json:"sessionRoot" yaml:"sessionRoot"`
	CacheRoot   string `json:"cacheRoot" yaml:"cacheRoot"`
}

type ConfigurationRecord struct {
	Capsule            string       `json:"capsule" yaml:"capsule"`
	BackendKind        string       `json:"backendKind" yaml:"backendKind"`
	BackendURL         string       `json:"backendUrl" yaml:"backendUrl"`
	BackendFingerprint string       `json:"backendFingerprint" yaml:"backendFingerprint"`
	Source             string       `json:"source,omitempty" yaml:"source,omitempty"`
	RegistryPort       int          `json:"registryPort" yaml:"registryPort"`
	FileserverPort     int          `json:"fileserverPort" yaml:"fileserverPort"`
	DevcontainerPath   string       `json:"devcontainerPath,omitempty" yaml:"devcontainerPath,omitempty"`
	Paths              SessionPaths `json:"paths" yaml:"paths"`
}

type SessionArtifactPaths struct {
	Root            string `json:"root" yaml:"root"`
	RuntimeRoot     string `json:"runtimeRoot" yaml:"runtimeRoot"`
	HaulPath        string `json:"haulPath" yaml:"haulPath"`
	RegistryOverlay string `json:"registryOverlay" yaml:"registryOverlay"`
}

type SourceDecisionKind string

const (
	SourceDecisionAdopted SourceDecisionKind = "adopted"
	SourceDecisionRemote  SourceDecisionKind = "remote"
)

type SourceDecision struct {
	Kind        SourceDecisionKind `json:"kind" yaml:"kind"`
	Root        string             `json:"root,omitempty" yaml:"root,omitempty"`
	Initialized bool               `json:"initialized" yaml:"initialized"`
	Lineage     *Lineage           `json:"lineage,omitempty" yaml:"lineage,omitempty"`
	Generation  *GenerationRef     `json:"generation,omitempty" yaml:"generation,omitempty"`
}

type CommandRecord struct {
	Executable string   `json:"executable" yaml:"executable"`
	Argv       []string `json:"argv" yaml:"argv"`
	Directory  string   `json:"directory,omitempty" yaml:"directory,omitempty"`
}

type DesiredServiceRecord struct {
	Name        string          `json:"name" yaml:"name"`
	LaunchToken string          `json:"launchToken" yaml:"launchToken"`
	Mapping     EndpointMapping `json:"mapping" yaml:"mapping"`
	PIDPath     string          `json:"pidPath" yaml:"pidPath"`
	LogPath     string          `json:"logPath" yaml:"logPath"`
	Child       CommandRecord   `json:"child" yaml:"child"`
}

type EntryMode string

const (
	EntryTerminal EntryMode = "terminal"
	EntryIDE      EntryMode = "ide"
)

type EntryRequestRecord struct {
	Mode   EntryMode `json:"mode" yaml:"mode"`
	Target string    `json:"target,omitempty" yaml:"target,omitempty"`
	IDE    string    `json:"ide,omitempty" yaml:"ide,omitempty"`
}

type ForwardingRecord struct {
	Name              string        `json:"name" yaml:"name"`
	LocalEndpoint     string        `json:"localEndpoint" yaml:"localEndpoint"`
	WorkspaceEndpoint string        `json:"workspaceEndpoint" yaml:"workspaceEndpoint"`
	Process           ProcessRecord `json:"process" yaml:"process"`
	DesiredState      RuntimeState  `json:"desiredState" yaml:"desiredState"`
	ObservedState     RuntimeState  `json:"observedState" yaml:"observedState"`
}

type WorkspaceCleanupAction string

const (
	WorkspaceCleanupDelete WorkspaceCleanupAction = "delete"
	WorkspaceCleanupStop   WorkspaceCleanupAction = "stop"
)

type CleanupPolicy struct {
	WorkspaceAction            WorkspaceCleanupAction `json:"workspaceAction" yaml:"workspaceAction"`
	RemoveOwnedMaterialization bool                   `json:"removeOwnedMaterialization" yaml:"removeOwnedMaterialization"`
	RemoveSessionArtifacts     bool                   `json:"removeSessionArtifacts" yaml:"removeSessionArtifacts"`
}

type RecoveryObjective string

const RecoveryObjectiveOpen RecoveryObjective = "open"

type HydrationPlan struct {
	Token     string `json:"token" yaml:"token"`
	StageRoot string `json:"stageRoot" yaml:"stageRoot"`
	FinalRoot string `json:"finalRoot" yaml:"finalRoot"`
}

type RecoveryRecord struct {
	Objective       RecoveryObjective      `json:"objective" yaml:"objective"`
	Configuration   ConfigurationRecord    `json:"configuration" yaml:"configuration"`
	Session         SessionArtifactPaths   `json:"session" yaml:"session"`
	Source          SourceDecision         `json:"source" yaml:"source"`
	Hydration       *HydrationPlan         `json:"hydration,omitempty" yaml:"hydration,omitempty"`
	DesiredServices []DesiredServiceRecord `json:"desiredServices,omitempty" yaml:"desiredServices,omitempty"`
	Entry           EntryRequestRecord     `json:"entry" yaml:"entry"`
	Forwarding      []ForwardingRecord     `json:"forwarding,omitempty" yaml:"forwarding,omitempty"`
	Cleanup         CleanupPolicy          `json:"cleanup" yaml:"cleanup"`
}
