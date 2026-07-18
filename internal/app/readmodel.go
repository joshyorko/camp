package app

import (
	"sort"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

type ProcessIdentityEvidence string

const (
	ProcessIdentityUnknown ProcessIdentityEvidence = "unknown"
	ProcessIdentityMatch   ProcessIdentityEvidence = "match"
	ProcessIdentityAbsent  ProcessIdentityEvidence = "absent"
	ProcessIdentityReused  ProcessIdentityEvidence = "pid-reused"
)

type ServiceEvidence struct {
	Helper ProcessIdentityEvidence
	Child  ProcessIdentityEvidence
}

type SessionEvidence struct {
	Services        map[string]ServiceEvidence
	PointerConflict bool
}

type ServiceLiveness string

const (
	ServiceLivenessUnknown   ServiceLiveness = "unknown"
	ServiceLivenessLive      ServiceLiveness = "live"
	ServiceLivenessStopped   ServiceLiveness = "stopped"
	ServiceLivenessDead      ServiceLiveness = "dead"
	ServiceLivenessPIDReused ServiceLiveness = "pid-reused"
)

type PublicationCondition string

const (
	PublicationNone            PublicationCondition = "none"
	PublicationPublished       PublicationCondition = "published"
	PublicationOrphaned        PublicationCondition = "orphaned"
	PublicationPointerConflict PublicationCondition = "pointer-conflict"
)

type CleanupCondition string

const (
	CleanupPending   CleanupCondition = "pending"
	CleanupRunning   CleanupCondition = "running"
	CleanupSucceeded CleanupCondition = "succeeded"
	CleanupFailed    CleanupCondition = "failed"
)

type RecoveryCondition string

const (
	RecoveryNone        RecoveryCondition = "none"
	RecoveryRequired    RecoveryCondition = "required"
	RecoveryCleanupOnly RecoveryCondition = "cleanup-only"
)

type ServiceReadModel struct {
	Name     string          `json:"name"`
	Liveness ServiceLiveness `json:"liveness"`
}

type PublicationReadModel struct {
	Condition  PublicationCondition `json:"condition"`
	Generation uint64               `json:"generation,omitempty"`
}

type CleanupReadModel struct {
	Condition CleanupCondition `json:"condition"`
	Message   string           `json:"message,omitempty"`
}

type RecoveryReadModel struct {
	Condition RecoveryCondition `json:"condition"`
	Command   string            `json:"command,omitempty"`
}

type SessionReadModel struct {
	ID          string               `json:"id"`
	Capsule     string               `json:"capsule"`
	Branch      string               `json:"branch"`
	State       string               `json:"state"`
	Mode        string               `json:"mode"`
	Root        string               `json:"root,omitempty"`
	WorkspaceID string               `json:"workspaceId,omitempty"`
	Provider    string               `json:"provider,omitempty"`
	Services    []ServiceReadModel   `json:"services"`
	Publication PublicationReadModel `json:"publication"`
	Cleanup     CleanupReadModel     `json:"cleanup"`
	Recovery    RecoveryReadModel    `json:"recovery"`
	CreatedAt   time.Time            `json:"createdAt"`
	UpdatedAt   time.Time            `json:"updatedAt"`
}

func BuildSessionReadModels(snapshots []domain.JournalSnapshot, evidence map[string]SessionEvidence) []SessionReadModel {
	models := make([]SessionReadModel, 0, len(snapshots))
	for _, snapshot := range snapshots {
		observation, observed := evidence[snapshot.SessionID]
		services := make([]ServiceReadModel, 0, len(snapshot.Services))
		for _, service := range snapshot.Services {
			serviceEvidence, ok := observation.Services[service.Name]
			liveness := ServiceLivenessUnknown
			if observed && ok {
				liveness = reconcileServiceLiveness(service.DesiredState, serviceEvidence)
			}
			services = append(services, ServiceReadModel{Name: service.Name, Liveness: liveness})
		}
		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
		publication := publicationReadModel(snapshot, observation.PointerConflict)
		cleanup := CleanupReadModel{Condition: CleanupCondition(snapshot.Cleanup.State), Message: snapshot.Cleanup.LastErr}
		recovery := RecoveryReadModel{Condition: RecoveryNone}
		if snapshot.Cleanup.State == domain.CleanupFailed {
			recovery.Condition = RecoveryRequired
			if snapshot.Checkpoint.PublicationSucceeded {
				recovery.Condition = RecoveryCleanupOnly
			}
			recovery.Command = "camp recover " + snapshot.SessionID
		} else if snapshot.State == domain.SessionRecovering {
			recovery = RecoveryReadModel{Condition: RecoveryRequired, Command: "camp recover " + snapshot.SessionID}
		}
		models = append(models, SessionReadModel{
			ID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch,
			State: string(snapshot.State), Mode: string(snapshot.Mode), Root: snapshot.Materialization.CanonicalPath,
			WorkspaceID: snapshot.Workspace.ID, Provider: snapshot.Workspace.Provider, Services: services,
			Publication: publication, Cleanup: cleanup, Recovery: recovery, CreatedAt: snapshot.CreatedAt, UpdatedAt: snapshot.UpdatedAt,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func reconcileServiceLiveness(desired domain.RuntimeState, evidence ServiceEvidence) ServiceLiveness {
	if evidence.Helper == ProcessIdentityReused || evidence.Child == ProcessIdentityReused {
		return ServiceLivenessPIDReused
	}
	if evidence.Helper == ProcessIdentityUnknown || evidence.Child == ProcessIdentityUnknown || evidence.Helper == "" || evidence.Child == "" {
		return ServiceLivenessUnknown
	}
	if evidence.Helper == ProcessIdentityMatch && evidence.Child == ProcessIdentityMatch {
		return ServiceLivenessLive
	}
	if evidence.Helper == ProcessIdentityAbsent && evidence.Child == ProcessIdentityAbsent && desired == domain.RuntimeDesiredStopped {
		return ServiceLivenessStopped
	}
	return ServiceLivenessDead
}

func publicationReadModel(snapshot domain.JournalSnapshot, conflict bool) PublicationReadModel {
	model := PublicationReadModel{Condition: PublicationNone}
	if snapshot.Checkpoint.Generation != nil {
		model.Generation = snapshot.Checkpoint.Generation.Generation
	}
	if conflict {
		model.Condition = PublicationPointerConflict
	} else if snapshot.Checkpoint.PublicationSucceeded {
		model.Condition = PublicationPublished
	} else if snapshot.Checkpoint.State == domain.CheckpointUploaded || snapshot.Checkpoint.State == domain.CheckpointVerified {
		model.Condition = PublicationOrphaned
	}
	return model
}
