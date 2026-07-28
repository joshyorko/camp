package domain

import "time"

type ProcessIdentity struct {
	PID        int    `json:"pid" yaml:"pid"`
	BootID     string `json:"bootId" yaml:"bootId"`
	StartTicks uint64 `json:"startTicks" yaml:"startTicks"`
}

type RuntimeState string

const (
	RuntimeDesiredRunning  RuntimeState = "running"
	RuntimeDesiredStopped  RuntimeState = "stopped"
	RuntimeObservedPending RuntimeState = "pending"
	RuntimeObservedReady   RuntimeState = "ready"
	RuntimeObservedAbsent  RuntimeState = "absent"
	RuntimeObservedFailed  RuntimeState = "failed"
)

type ProcessRecord struct {
	Identity           ProcessIdentity `json:"identity" yaml:"identity"`
	DesiredExecutable  string          `json:"desiredExecutable" yaml:"desiredExecutable"`
	ObservedExecutable string          `json:"observedExecutable" yaml:"observedExecutable"`
	Argv               []string        `json:"argv" yaml:"argv"`
	ArgvSHA256         string          `json:"argvSha256" yaml:"argvSha256"`
	ParentPID          int             `json:"parentPid" yaml:"parentPid"`
	PGID               int             `json:"pgid" yaml:"pgid"`
	SID                int             `json:"sid" yaml:"sid"`
	NetNS              string          `json:"netNs" yaml:"netNs"`
}

type EndpointMapping struct {
	HostAddress string `json:"hostAddress" yaml:"hostAddress"`
	HostPort    int    `json:"hostPort" yaml:"hostPort"`
	GuestPort   int    `json:"guestPort" yaml:"guestPort"`
}

type ConfinementRecord struct {
	Executable             string `json:"executable" yaml:"executable"`
	Version                string `json:"version" yaml:"version"`
	EnvironmentFingerprint string `json:"environmentFingerprint" yaml:"environmentFingerprint"`
	Boundary               string `json:"boundary" yaml:"boundary"`
}

type ServiceUnitRecord struct {
	Name          string            `json:"name" yaml:"name"`
	LaunchToken   string            `json:"launchToken" yaml:"launchToken"`
	Confinement   ConfinementRecord `json:"confinement" yaml:"confinement"`
	Mapping       EndpointMapping   `json:"mapping" yaml:"mapping"`
	PIDPath       string            `json:"pidPath" yaml:"pidPath"`
	LogPath       string            `json:"logPath" yaml:"logPath"`
	Helper        ProcessRecord     `json:"helper" yaml:"helper"`
	Child         ProcessRecord     `json:"child" yaml:"child"`
	DesiredState  RuntimeState      `json:"desiredState" yaml:"desiredState"`
	ObservedState RuntimeState      `json:"observedState" yaml:"observedState"`
}

type LeaseRecord struct {
	Lease    *WriterLease `json:"lease,omitempty" yaml:"lease,omitempty"`
	Revision string       `json:"revision,omitempty" yaml:"revision,omitempty"`
}

type WorkspaceRecord struct {
	ID                string              `json:"id,omitempty" yaml:"id,omitempty"`
	Context           string              `json:"context,omitempty" yaml:"context,omitempty"`
	Provider          string              `json:"provider,omitempty" yaml:"provider,omitempty"`
	LocalProvider     bool                `json:"localProvider,omitempty" yaml:"localProvider,omitempty"`
	LocalFolder       string              `json:"localFolder,omitempty" yaml:"localFolder,omitempty"`
	Target            string              `json:"target,omitempty" yaml:"target,omitempty"`
	StagingRoot       string              `json:"stagingRoot,omitempty" yaml:"stagingRoot,omitempty"`
	EffectiveRoot     string              `json:"effectiveRoot,omitempty" yaml:"effectiveRoot,omitempty"`
	HardlinksRestored bool                `json:"hardlinksRestored,omitempty" yaml:"hardlinksRestored,omitempty"`
	ImagesRestored    bool                `json:"imagesRestored,omitempty" yaml:"imagesRestored,omitempty"`
	Mirror            MirrorAttemptRecord `json:"mirror" yaml:"mirror"`
}

type MirrorState string

const (
	MirrorCompleted MirrorState = "completed"
	MirrorAmbiguous MirrorState = "ambiguous"
)

type MirrorAttemptRecord struct {
	LogicalAttempt uint64      `json:"logicalAttempt,omitempty" yaml:"logicalAttempt,omitempty"`
	AttemptID      string      `json:"attemptId,omitempty" yaml:"attemptId,omitempty"`
	State          MirrorState `json:"state,omitempty" yaml:"state,omitempty"`
	Root           string      `json:"root,omitempty" yaml:"root,omitempty"`
	RemoteRoot     string      `json:"remoteRoot,omitempty" yaml:"remoteRoot,omitempty"`
	Method         string      `json:"method,omitempty" yaml:"method,omitempty"`
	Exclusions     []string    `json:"exclusions,omitempty" yaml:"exclusions,omitempty"`
}

type EndpointRecord struct {
	Candidates []EndpointMapping `json:"candidates,omitempty" yaml:"candidates,omitempty"`
	Committed  []EndpointMapping `json:"committed,omitempty" yaml:"committed,omitempty"`
}

type SupervisorRecord struct {
	Identity ProcessIdentity `json:"identity" yaml:"identity"`
	Desired  RuntimeState    `json:"desiredState" yaml:"desiredState"`
	Observed RuntimeState    `json:"observedState" yaml:"observedState"`
}

type SessionMode string

const (
	SessionReadWrite SessionMode = "readWrite"
	SessionReadOnly  SessionMode = "readOnly"
)

type SessionState string

const (
	SessionOpening    SessionState = "opening"
	SessionOpen       SessionState = "open"
	SessionRecovering SessionState = "recovering"
	SessionClosed     SessionState = "closed"
)

type JournalSnapshot struct {
	SchemaVersion           int                 `json:"schemaVersion" yaml:"schemaVersion"`
	SessionID               string              `json:"sessionId" yaml:"sessionId"`
	Capsule                 string              `json:"capsule" yaml:"capsule"`
	Lineage                 Lineage             `json:"lineage" yaml:"lineage"`
	Mode                    SessionMode         `json:"mode" yaml:"mode"`
	Tools                   ToolVersions        `json:"tools" yaml:"tools"`
	ExecutionBinding        *ExecutionBinding   `json:"executionBinding,omitempty" yaml:"executionBinding,omitempty"`
	OpenedGeneration        *GenerationRef      `json:"openedGeneration,omitempty" yaml:"openedGeneration,omitempty"`
	CurrentBase             *GenerationRef      `json:"currentBase,omitempty" yaml:"currentBase,omitempty"`
	CurrentPointer          *LatestPointer      `json:"currentPointer,omitempty" yaml:"currentPointer,omitempty"`
	ExpectedPointerRevision string              `json:"expectedPointerRevision,omitempty" yaml:"expectedPointerRevision,omitempty"`
	Confinement             ConfinementRecord   `json:"confinement" yaml:"confinement"`
	Lease                   LeaseRecord         `json:"lease" yaml:"lease"`
	Workspace               WorkspaceRecord     `json:"workspace" yaml:"workspace"`
	Endpoints               EndpointRecord      `json:"endpoints" yaml:"endpoints"`
	Supervisor              SupervisorRecord    `json:"supervisor" yaml:"supervisor"`
	Services                []ServiceUnitRecord `json:"services,omitempty" yaml:"services,omitempty"`
	Images                  ImageInventory      `json:"images,omitempty" yaml:"images,omitempty"`
	RegistryCutRoot         string              `json:"registryCutRoot,omitempty" yaml:"registryCutRoot,omitempty"`
	Recovery                RecoveryRecord      `json:"recovery" yaml:"recovery"`
	State                   SessionState        `json:"state" yaml:"state"`
	Materialization         Materialization     `json:"materialization" yaml:"materialization"`
	Checkpoint              Checkpoint          `json:"checkpoint" yaml:"checkpoint"`
	Cleanup                 Cleanup             `json:"cleanup" yaml:"cleanup"`
	CreatedAt               time.Time           `json:"createdAt" yaml:"createdAt"`
	UpdatedAt               time.Time           `json:"updatedAt" yaml:"updatedAt"`
}
