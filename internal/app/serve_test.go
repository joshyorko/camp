package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestServeStatusAndLogsUseSelectedRecordedService(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	record := domain.ServiceUnitRecord{Name: "registry", LogPath: "/owned/session/registry.log", DesiredState: domain.RuntimeDesiredRunning}
	snapshot.Services = []domain.ServiceUnitRecord{record}
	journal := &fakeRecoveryJournal{snapshot: snapshot}
	controller := &fakeServeController{observation: supervisor.UnitObservation{State: supervisor.UnitLive, Record: record}}
	logs := &fakeServeLogs{chunk: supervisor.LogChunk{Bytes: []byte("tail"), Truncated: true}}
	guard := &serveGuard{}
	usecase := NewServe(journal, &serveLocker{}, guard, controller, logs)

	status, err := usecase.Status(context.Background(), SessionSelector{SessionID: snapshot.SessionID}, "registry")
	if err != nil || status.Name != "registry" || status.Liveness != ServiceLivenessLive {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
	chunk, err := usecase.Logs(context.Background(), SessionSelector{SessionID: snapshot.SessionID}, "registry", 64)
	if err != nil || string(chunk.Bytes) != "tail" || !chunk.Truncated {
		t.Fatalf("Logs() = %#v, %v", chunk, err)
	}
	if !reflect.DeepEqual(logs.record, record) || logs.limit != 64 || guard.calls != 2 {
		t.Fatalf("serve guards/log input calls=%d record=%#v limit=%d", guard.calls, logs.record, logs.limit)
	}
}

func TestServeRestartLocksAndRevalidatesBeforeSupervisorEffect(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	record := domain.ServiceUnitRecord{Name: "registry", DesiredState: domain.RuntimeDesiredRunning}
	snapshot.Services = []domain.ServiceUnitRecord{record}
	order := []string{}
	journal := &fakeRecoveryJournal{snapshot: snapshot}
	locks := &serveLocker{}
	guard := &serveGuard{order: &order}
	controller := &fakeServeController{order: &order, restarted: record, next: snapshot, observation: supervisor.UnitObservation{State: supervisor.UnitLive, Record: record}}
	usecase := NewServe(journal, locks, guard, controller, &fakeServeLogs{})

	result, err := usecase.Restart(context.Background(), ServeRestartRequest{Selector: SessionSelector{SessionID: snapshot.SessionID}, Service: "registry", LaunchToken: "new-token"})
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if result.Name != "registry" || controller.sessionID != snapshot.SessionID || controller.launchToken != "new-token" {
		t.Fatalf("Restart() = %#v controller=%#v", result, controller)
	}
	if !reflect.DeepEqual(order, []string{"guard", "restart"}) || locks.owner.Operation != "serve-restart:registry" || locks.released != 1 {
		t.Fatalf("restart order=%#v owner=%#v releases=%d", order, locks.owner, locks.released)
	}
}

type serveGuard struct {
	calls int
	order *[]string
}

type serveLocker struct {
	owner    ports.OperationOwner
	released int
}

func (l *serveLocker) Acquire(_ context.Context, owner ports.OperationOwner) (ports.OperationToken, error) {
	l.owner = owner
	return ports.OperationToken{Owner: owner}, nil
}
func (l *serveLocker) Release(context.Context, ports.OperationToken) error {
	l.released++
	return nil
}

func (g *serveGuard) Revalidate(context.Context, domain.JournalSnapshot, []ports.PendingIntent) error {
	g.calls++
	if g.order != nil {
		*g.order = append(*g.order, "guard")
	}
	return nil
}

type fakeServeController struct {
	observation supervisor.UnitObservation
	restarted   domain.ServiceUnitRecord
	next        domain.JournalSnapshot
	order       *[]string
	sessionID   string
	launchToken string
}

func (f *fakeServeController) Observe(context.Context, domain.ServiceUnitRecord) (supervisor.UnitObservation, error) {
	return f.observation, nil
}
func (f *fakeServeController) Restart(_ context.Context, sessionID, _ string, launchToken string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	if f.order != nil {
		*f.order = append(*f.order, "restart")
	}
	f.sessionID = sessionID
	f.launchToken = launchToken
	return f.restarted, f.next, nil
}

type fakeServeLogs struct {
	chunk  supervisor.LogChunk
	record domain.ServiceUnitRecord
	limit  int64
}

func (f *fakeServeLogs) ReadTail(_ context.Context, record domain.ServiceUnitRecord, limit int64) (supervisor.LogChunk, error) {
	f.record = record
	f.limit = limit
	return f.chunk, nil
}
