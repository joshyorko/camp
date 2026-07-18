package lifecycle

import (
	"context"
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
	err := barrier.WithCut(context.Background(), registry.SnapshotRequest{SessionID: "session-a"}, func() error {
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

type barrierJournal struct{ snapshot domain.JournalSnapshot }

func (j barrierJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	return j.snapshot, nil, nil
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
