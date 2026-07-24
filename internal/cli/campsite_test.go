package cli

import (
	"strings"
	"testing"

	campcontract "github.com/joshyorko/camp"
	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/domain"
)

func TestBuildCampsiteModelUsesLockedConfigAndDurableSessionValues(t *testing.T) {
	lock, err := tooladapter.ParseLock(strings.NewReader(`
schemaVersion: 1
tools:
  devpod:
    repository: owner/devpod
    version: v0.26.1
    commit: 1111111111111111111111111111111111111111
    assets: {linux: {amd64: {url: https://example.test/devpod, sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}}}
  hauler:
    repository: owner/hauler
    version: v2.0.2
    commit: 2222222222222222222222222222222222222222
    assets: {linux: {amd64: {url: https://example.test/hauler, sha256: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}}}
fixtures:
  room: {repository: owner/room, version: v1.0.0, commit: 3333333333333333333333333333333333333333}
`))
	if err != nil {
		t.Fatal(err)
	}
	generation := domain.GenerationRef{Generation: 42, ArchiveSHA256: strings.Repeat("c", 64)}
	snapshot := domain.JournalSnapshot{SessionID: "session-1", Capsule: "brain", Workspace: domain.WorkspaceRecord{Provider: "ror", Context: "work", LocalProvider: false}, CurrentBase: &generation, State: domain.SessionOpen}
	model, err := buildCampsiteModel(lock, config.Runtime{Bootstrap: config.Bootstrap{Capsule: "brain", Source: "/home/josh/brain", DevPodProvider: "ror", DevPodContext: "work"}}, config.Backend{Kind: config.BackendS3, SanitizedURL: "s3://camp/brain"}, []domain.JournalSnapshot{snapshot})
	if err != nil {
		t.Fatalf("buildCampsiteModel: %v", err)
	}
	if model.DevPod.Version != "v0.26.1" || model.Hauler.Version != "v2.0.2" {
		t.Fatalf("tool versions = %#v %#v", model.DevPod, model.Hauler)
	}
	if model.Provider != "ror" || model.Context != "work" || model.RuntimeKind != "remote DevPod" {
		t.Fatalf("runtime = provider %q context %q kind %q", model.Provider, model.Context, model.RuntimeKind)
	}
	if model.Capsule != "brain" || model.Source != "/home/josh/brain" || model.BackendKind != "s3" || model.Storage != "s3://camp/brain · generation 42" {
		t.Fatalf("model = %#v", model)
	}
}

func TestBuildCampsiteModelStatesAbsenceWithoutInventingGeneration(t *testing.T) {
	lock, err := tooladapter.ParseLock(strings.NewReader(string(campcontract.DistributionToolLock())))
	if err != nil {
		t.Fatal(err)
	}
	model, err := buildCampsiteModel(lock, config.Runtime{Bootstrap: config.Bootstrap{Capsule: "brain", Source: "/brain", DevPodProvider: "docker", DevPodContext: "default"}}, config.Backend{Kind: config.BackendFile, SanitizedURL: "file:///store"}, nil)
	if err != nil {
		t.Fatalf("buildCampsiteModel: %v", err)
	}
	if model.RuntimeKind != "local DevPod" || model.Storage != "file:///store · no committed generation" {
		t.Fatalf("model = %#v", model)
	}
}

func TestBuildCampsiteModelIgnoresSessionsForOtherCapsules(t *testing.T) {
	lock, err := tooladapter.ParseLock(strings.NewReader(string(campcontract.DistributionToolLock())))
	if err != nil {
		t.Fatal(err)
	}
	generation := domain.GenerationRef{Generation: 99, ArchiveSHA256: strings.Repeat("d", 64)}
	other := domain.JournalSnapshot{
		SessionID:   "other-session",
		Capsule:     "other-capsule",
		Workspace:   domain.WorkspaceRecord{Provider: "docker", Context: "other", LocalProvider: true},
		CurrentBase: &generation,
		State:       domain.SessionClosed,
	}
	model, err := buildCampsiteModel(
		lock,
		config.Runtime{Bootstrap: config.Bootstrap{
			Capsule: "brain", Source: "/brain",
			DevPodProvider: "room-of-requirement", DevPodContext: "default",
		}},
		config.Backend{Kind: config.BackendFile, SanitizedURL: "file:///store"},
		[]domain.JournalSnapshot{other},
	)
	if err != nil {
		t.Fatalf("buildCampsiteModel: %v", err)
	}
	if model.Provider != "room-of-requirement" || model.Context != "default" || model.RuntimeKind != "remote DevPod" {
		t.Fatalf("runtime = provider %q context %q kind %q", model.Provider, model.Context, model.RuntimeKind)
	}
	if model.Storage != "file:///store · no committed generation" {
		t.Fatalf("storage = %q, want no generation from another capsule", model.Storage)
	}
}
