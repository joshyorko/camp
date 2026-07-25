package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/campconfig"
	"github.com/joshyorko/camp/internal/domain"
)

type multiCampSessions []domain.JournalSnapshot

func (sessions multiCampSessions) List(context.Context) ([]domain.JournalSnapshot, error) {
	return sessions, nil
}

func TestTwoCampRootsAndActiveSessionsStayIsolated(t *testing.T) {
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	roots := []string{t.TempDir(), t.TempDir()}
	ids := []string{"alpha", "beta"}
	sessions := multiCampSessions{
		{SessionID: "alpha-session", Capsule: "alpha", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen},
		{SessionID: "beta-session", Capsule: "beta", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen},
	}
	for index, root := range roots {
		manifest := campconfig.Manifest{
			SchemaVersion: 1, ID: ids[index], Source: ".", Backend: backend,
			Workspace: campconfig.Workspace{Provider: "docker", Context: "default"},
		}
		if _, err := campconfig.Create(root, manifest); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(root, "nested")
		if err := os.Mkdir(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		resolved, err := campconfig.Discover(nested)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Manifest.ID != ids[index] || resolved.Root != root {
			t.Fatalf("root %d resolved %#v", index, resolved)
		}
		selected, err := app.SelectActiveSession(context.Background(), sessions, app.SessionSelector{Capsule: resolved.Manifest.ID, Branch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if selected.SessionID != ids[index]+"-session" {
			t.Fatalf("camp %s routed to %s", ids[index], selected.SessionID)
		}
	}
}
