package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	request := registry.SnapshotRequest{SessionID: "session-a", RegistryLaunchToken: "old"}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	journal := barrierJournal{
		snapshot: domain.JournalSnapshot{SessionID: "session-a", Services: []domain.ServiceUnitRecord{record}},
		pending:  []ports.PendingIntent{{Intent: ports.IntentRecord{ID: "checkpoint-1-3", SessionID: "session-a", Transition: "RegistrySnapshotSealed", Input: input}}},
	}
	controller := &recordingBarrierController{}
	barrier := NewRegistryBarrier(journal, controller)
	err = barrier.WithCut(context.Background(), request, func() error {
		controller.events = append(controller.events, "cut")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(controller.events, []string{"observe", "stop", "cut", "restart"}) {
		t.Fatalf("events=%#v", controller.events)
	}
	if controller.parentIntentID != "checkpoint-1-3" {
		t.Fatalf("restart parent intent = %q", controller.parentIntentID)
	}
}

func TestRegistryBarrierRestartsSameLaunchAfterPreCutStopFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	record := domain.ServiceUnitRecord{Name: "registry", LaunchToken: "old"}
	request := registry.SnapshotRequest{SessionID: "session-a", SnapshotRoot: filepath.Join(root, "sealed"), RegistryLaunchToken: "old"}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	j := barrierJournal{
		snapshot: domain.JournalSnapshot{SessionID: "session-a", Services: []domain.ServiceUnitRecord{record}},
		pending:  []ports.PendingIntent{{Intent: ports.IntentRecord{ID: "checkpoint-1-3", SessionID: "session-a", Transition: "RegistrySnapshotSealed", Input: input}}},
	}
	stopErr := errors.New("post-stop inspection raced with exit")
	controller := &recordingBarrierController{observations: []supervisor.UnitState{supervisor.UnitLive, supervisor.UnitStopped}, stopErr: stopErr}
	err = NewRegistryBarrier(j, controller).WithCut(context.Background(), request, func() error {
		t.Fatal("cut ran after an unconfirmed stop")
		return nil
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("WithCut() error = %v, want stop error", err)
	}
	if !reflect.DeepEqual(controller.events, []string{"observe", "stop", "observe", "restart"}) || controller.restartToken != "old" {
		t.Fatalf("events=%#v restartToken=%q", controller.events, controller.restartToken)
	}
	if _, err := os.Stat(request.SnapshotRoot); !os.IsNotExist(err) {
		t.Fatalf("failed pre-cut stop published a snapshot: %v", err)
	}
}

func TestRegistryBarrierRepairsDurablyStoppedPreCutRetryBeforeSealing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	record := domain.ServiceUnitRecord{Name: "registry", LaunchToken: "old"}
	request := registry.SnapshotRequest{SessionID: "session-a", SnapshotRoot: filepath.Join(root, "sealed"), RegistryLaunchToken: "old"}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	j := barrierJournal{
		snapshot: domain.JournalSnapshot{SessionID: "session-a", Services: []domain.ServiceUnitRecord{record}},
		pending:  []ports.PendingIntent{{Intent: ports.IntentRecord{ID: "checkpoint-1-3", SessionID: "session-a", Transition: "RegistrySnapshotSealed", Input: input}}},
	}
	controller := &recordingBarrierController{observations: []supervisor.UnitState{supervisor.UnitStopped}}
	err = NewRegistryBarrier(j, controller).WithCut(context.Background(), request, func() error {
		t.Fatal("cut ran before the stopped registry was repaired")
		return nil
	})
	if err == nil || !reflect.DeepEqual(controller.events, []string{"observe", "restart"}) || controller.restartToken != "old" {
		t.Fatalf("error=%v events=%#v restartToken=%q", err, controller.events, controller.restartToken)
	}
}

func TestRegistryBarrierReconcilesExactNestedRestartBeforeRetryingSeal(t *testing.T) {
	t.Parallel()
	record := domain.ServiceUnitRecord{Name: "registry", LaunchToken: "old"}
	request := registry.SnapshotRequest{SessionID: "session-a", SnapshotRoot: filepath.Join(t.TempDir(), "sealed"), RegistryLaunchToken: "old"}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	seal := ports.IntentRecord{ID: "checkpoint-1-3", SessionID: "session-a", Transition: "RegistrySnapshotSealed", Input: input}
	restart := ports.IntentRecord{ID: "registry-old-restart", SessionID: "session-a", Transition: "ServiceRestart"}
	start := ports.IntentRecord{ID: "registry-old", SessionID: "session-a", Transition: "ServiceStart"}
	j := barrierJournal{
		snapshot: domain.JournalSnapshot{SessionID: "session-a", Services: []domain.ServiceUnitRecord{record}},
		pending:  []ports.PendingIntent{{Intent: seal}, {Intent: restart}, {Intent: start}},
	}
	controller := &recordingBarrierController{}
	err = NewRegistryBarrier(j, controller).WithCut(context.Background(), request, func() error {
		t.Fatal("cut ran while a nested restart was pending")
		return nil
	})
	if err == nil || !reflect.DeepEqual(controller.events, []string{"restart"}) || controller.parentIntentID != seal.ID || controller.restartToken != "old" {
		t.Fatalf("error=%v events=%#v parent=%q token=%q", err, controller.events, controller.parentIntentID, controller.restartToken)
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

type recordingBarrierController struct {
	events         []string
	parentIntentID string
	observations   []supervisor.UnitState
	stopErr        error
	restartToken   string
}

func (c *recordingBarrierController) Observe(context.Context, domain.ServiceUnitRecord) (supervisor.UnitObservation, error) {
	c.events = append(c.events, "observe")
	state := supervisor.UnitLive
	if len(c.observations) != 0 {
		state = c.observations[0]
		c.observations = c.observations[1:]
	}
	return supervisor.UnitObservation{State: state}, nil
}
func (c *recordingBarrierController) Stop(context.Context, domain.ServiceUnitRecord) error {
	c.events = append(c.events, "stop")
	return c.stopErr
}
func (c *recordingBarrierController) RestartWithin(_ context.Context, _ string, _ string, launchToken, parentIntentID string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	c.events = append(c.events, "restart")
	c.parentIntentID = parentIntentID
	c.restartToken = launchToken
	return domain.ServiceUnitRecord{}, domain.JournalSnapshot{}, nil
}
