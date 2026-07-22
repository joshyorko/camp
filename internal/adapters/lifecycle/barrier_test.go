package lifecycle

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/registry"
)

func TestRegistryBarrierStopsForCutAndRestartsBeforeReturn(t *testing.T) {
	t.Parallel()
	record := domain.ServiceUnitRecord{Name: "registry", LaunchToken: "old"}
	journal := barrierJournal{snapshot: domain.JournalSnapshot{SessionID: "session-a", Services: []domain.ServiceUnitRecord{record}}}
	controller := &recordingBarrierController{}
	barrier := NewRegistryBarrier(journal, controller)
	err := barrier.WithCut(context.Background(), registry.SnapshotRequest{SessionID: "session-a", RegistryLaunchToken: "old"}, func() error {
		controller.events = append(controller.events, "cut")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(controller.events, []string{"observe", "stop", "cut", "restart"}) {
		t.Fatalf("events=%#v", controller.events)
	}
}

func TestRegistryBarrierAllowsOnlyItsExactPendingSealIntent(t *testing.T) {
	t.Parallel()
	record := domain.ServiceUnitRecord{Name: "registry", LaunchToken: "old"}
	request := registry.SnapshotRequest{SessionID: "session-a", OverlayRoot: "/overlay", SnapshotRoot: "/snapshot", CatalogEndpoint: "http://127.0.0.1:5000", RegistryLaunchToken: "old"}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	journal := barrierJournal{
		snapshot: domain.JournalSnapshot{SessionID: "session-a", Services: []domain.ServiceUnitRecord{record}},
		pending:  []ports.PendingIntent{{Intent: ports.IntentRecord{ID: "checkpoint-1-3", SessionID: "session-a", Transition: "RegistrySnapshotSealed", Input: input}}},
	}
	controller := &recordingBarrierController{}
	if err := NewRegistryBarrier(journal, controller).WithCut(context.Background(), request, func() error { return nil }); err != nil {
		t.Fatalf("WithCut() rejected its exact pending seal intent: %v", err)
	}

	journal.pending[0].Intent.Transition = "WorkspaceImagesInventoried"
	if err := NewRegistryBarrier(journal, &recordingBarrierController{}).WithCut(context.Background(), request, func() error { return nil }); err == nil {
		t.Fatal("WithCut() accepted an unrelated pending intent")
	}
	journal.pending[0].Intent.Transition = "RegistrySnapshotSealed"
	journal.snapshot.Services[0].LaunchToken = "restarted-after-unknown-cut"
	if err := NewRegistryBarrier(journal, &recordingBarrierController{}).WithCut(context.Background(), request, func() error { return nil }); err == nil {
		t.Fatal("WithCut() repeated a seal after the registry launch identity changed")
	}
}

type barrierJournal struct {
	snapshot domain.JournalSnapshot
	pending  []ports.PendingIntent
}

func (j barrierJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	return j.snapshot, j.pending, nil
}

type recordingBarrierController struct{ events []string }

func (c *recordingBarrierController) Observe(context.Context, domain.ServiceUnitRecord) (supervisor.UnitObservation, error) {
	c.events = append(c.events, "observe")
	return supervisor.UnitObservation{State: supervisor.UnitLive}, nil
}
func (c *recordingBarrierController) Stop(context.Context, domain.ServiceUnitRecord) error {
	c.events = append(c.events, "stop")
	return nil
}
func (c *recordingBarrierController) Restart(_ context.Context, _ string, _ string, _ string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	c.events = append(c.events, "restart")
	return domain.ServiceUnitRecord{}, domain.JournalSnapshot{}, nil
}
