package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

func TestOperationalQueriesListReconcilesEverySession(t *testing.T) {
	t.Parallel()
	first := readModelSnapshot()
	first.SessionID = "session-z"
	first.Services = []domain.ServiceUnitRecord{{Name: "registry", DesiredState: domain.RuntimeDesiredRunning}}
	second := readModelSnapshot()
	second.SessionID = "session-a"
	second.Services = []domain.ServiceUnitRecord{{Name: "files", DesiredState: domain.RuntimeDesiredStopped}}
	observer := &fakeSessionObserver{evidence: map[string]SessionEvidence{
		"session-z": {Services: map[string]ServiceEvidence{"registry": {Helper: ProcessIdentityMatch, Child: ProcessIdentityMatch}}},
		"session-a": {Services: map[string]ServiceEvidence{"files": {Helper: ProcessIdentityAbsent, Child: ProcessIdentityAbsent}}},
	}}
	queries := OperationalQueries{Sessions: fakeSessionLister{sessions: []domain.JournalSnapshot{first, second}}, Observer: observer}

	models, err := queries.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := []string{models[0].ID, models[1].ID}; !reflect.DeepEqual(got, []string{"session-a", "session-z"}) {
		t.Fatalf("List() ids = %#v", got)
	}
	if models[0].Services[0].Liveness != ServiceLivenessStopped || models[1].Services[0].Liveness != ServiceLivenessLive {
		t.Fatalf("List() liveness = %#v", models)
	}
	if !reflect.DeepEqual(observer.observed, []string{"session-z", "session-a"}) {
		t.Fatalf("observed sessions = %#v", observer.observed)
	}
}

func TestOperationalQueriesStatusSelectsClosedSessionForHistoryPurpose(t *testing.T) {
	t.Parallel()
	closed := readModelSnapshot()
	closed.SessionID = "closed"
	closed.State = domain.SessionClosed
	queries := OperationalQueries{Sessions: fakeSessionLister{sessions: []domain.JournalSnapshot{closed}}, Observer: &fakeSessionObserver{}}

	model, err := queries.Status(context.Background(), SessionSelector{SessionID: "closed"})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if model.ID != "closed" || model.State != string(domain.SessionClosed) {
		t.Fatalf("Status() = %#v", model)
	}
}

func TestOperationalQueriesHistoryUsesSelectedSessionLineageAndReturnsReadModels(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	snapshot.SessionID = "closed"
	snapshot.State = domain.SessionClosed
	created := time.Date(2026, 7, 18, 14, 0, 0, 0, time.UTC)
	history := &fakeGenerationHistory{generations: []domain.GenerationMetadata{
		{Generation: domain.GenerationRef{Generation: 2, ArchiveSHA256: "bbb"}, ObjectKey: "objects/2", Size: 20, CreatedAt: created, SessionID: "new"},
		{Generation: domain.GenerationRef{Generation: 1, ArchiveSHA256: "aaa"}, ObjectKey: "objects/1", Size: 10, CreatedAt: created.Add(-time.Hour), SessionID: "old"},
	}}
	queries := OperationalQueries{Sessions: fakeSessionLister{sessions: []domain.JournalSnapshot{snapshot}}, History: history}

	models, err := queries.HistoryFor(context.Background(), SessionSelector{SessionID: "closed"})
	if err != nil {
		t.Fatalf("HistoryFor() error = %v", err)
	}
	if history.capsule != snapshot.Capsule || history.lineage != snapshot.Lineage {
		t.Fatalf("history selection = %q %#v", history.capsule, history.lineage)
	}
	want := []GenerationReadModel{
		{Generation: 2, ArchiveSHA256: "bbb", ObjectKey: "objects/2", Size: 20, CreatedAt: created, SessionID: "new"},
		{Generation: 1, ArchiveSHA256: "aaa", ObjectKey: "objects/1", Size: 10, CreatedAt: created.Add(-time.Hour), SessionID: "old"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("HistoryFor() = %#v, want %#v", models, want)
	}
}

func TestOperationalQueriesFailClosedWhenObservationFails(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	observeErr := errors.New("inspect process identity")
	queries := OperationalQueries{Sessions: fakeSessionLister{sessions: []domain.JournalSnapshot{snapshot}}, Observer: &fakeSessionObserver{err: observeErr}}

	if _, err := queries.List(context.Background()); !errors.Is(err, observeErr) {
		t.Fatalf("List() error = %v, want %v", err, observeErr)
	}
}

type fakeSessionObserver struct {
	evidence map[string]SessionEvidence
	err      error
	observed []string
}

func (f *fakeSessionObserver) Observe(_ context.Context, snapshot domain.JournalSnapshot) (SessionEvidence, error) {
	f.observed = append(f.observed, snapshot.SessionID)
	if f.err != nil {
		return SessionEvidence{}, f.err
	}
	return f.evidence[snapshot.SessionID], nil
}

type fakeGenerationHistory struct {
	generations []domain.GenerationMetadata
	capsule     string
	lineage     domain.Lineage
}

func (f *fakeGenerationHistory) List(_ context.Context, capsule string, lineage domain.Lineage) ([]domain.GenerationMetadata, error) {
	f.capsule = capsule
	f.lineage = lineage
	return f.generations, nil
}
