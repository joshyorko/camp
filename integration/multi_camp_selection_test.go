package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/campconfig"
	"github.com/joshyorko/camp/internal/cli"
	"github.com/joshyorko/camp/internal/domain"
)

type smokeSessionStore struct {
	sessions []domain.JournalSnapshot
}

func (store *smokeSessionStore) List(context.Context) ([]domain.JournalSnapshot, error) {
	return append([]domain.JournalSnapshot(nil), store.sessions...), nil
}

type multiCampCommandHarness struct {
	store  *smokeSessionStore
	routes []string
	next   int
}

func (h *multiCampCommandHarness) Init(context.Context, cli.InitRequest, cli.OutputMode, io.Writer) error {
	return nil
}

func (h *multiCampCommandHarness) Open(ctx context.Context, path string, _ cli.OutputMode, out io.Writer) error {
	selection := cli.SelectionFromContext(ctx)
	camp := selection.Camp
	source := ""
	backend := ""
	provider := "docker"
	contextName := "default"
	if path != "" {
		resolved, err := campconfig.Discover(path)
		if err != nil {
			return err
		}
		camp = resolved.Manifest.ID
		source = resolved.Root
		backend = resolved.Manifest.Backend
		provider = resolved.Manifest.Workspace.Provider
		contextName = resolved.Manifest.Workspace.Context
	}
	if camp == "" {
		return fmt.Errorf("smoke open lacks camp")
	}
	h.next++
	now := time.Unix(int64(h.next), 0)
	session := domain.JournalSnapshot{
		SessionID: fmt.Sprintf("%s-session-%d", camp, h.next),
		Capsule:   camp, Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen,
		CreatedAt: now, UpdatedAt: now,
		Recovery: domain.RecoveryRecord{Configuration: domain.ConfigurationRecord{
			Capsule: camp, Source: source, BackendURL: backend,
		}},
		Workspace: domain.WorkspaceRecord{Provider: provider, Context: contextName},
	}
	h.store.sessions = append(h.store.sessions, session)
	h.routes = append(h.routes, "open:"+session.SessionID)
	_, err := fmt.Fprintln(out, session.SessionID)
	return err
}

func (h *multiCampCommandHarness) selected(ctx context.Context, purpose app.SelectionPurpose) (domain.JournalSnapshot, error) {
	selection := cli.SelectionFromContext(ctx)
	return app.SelectSession(ctx, h.store, app.SessionSelector{SessionID: selection.Session, Capsule: selection.Camp, Branch: "main"}, purpose)
}

func (h *multiCampCommandHarness) Attach(ctx context.Context, request cli.AttachRequest, _ cli.OutputMode, _ io.Writer) error {
	session, err := h.selected(ctx, app.SelectionActiveMutation)
	if err == nil {
		h.routes = append(h.routes, "attach:"+session.SessionID+":"+request.Target)
	}
	return err
}

func (h *multiCampCommandHarness) Sync(ctx context.Context, _ cli.OutputMode, _ io.Writer) error {
	session, err := h.selected(ctx, app.SelectionActiveMutation)
	if err == nil {
		h.routes = append(h.routes, "sync:"+session.SessionID)
	}
	return err
}

func (h *multiCampCommandHarness) Close(ctx context.Context, _ cli.CloseRequest, _ cli.OutputMode, _ io.Writer) error {
	session, err := h.selected(ctx, app.SelectionActiveMutation)
	if err != nil {
		return err
	}
	for index := range h.store.sessions {
		if h.store.sessions[index].SessionID == session.SessionID {
			h.store.sessions[index].State = domain.SessionClosed
			h.store.sessions[index].UpdatedAt = h.store.sessions[index].UpdatedAt.Add(time.Second)
		}
	}
	h.routes = append(h.routes, "close:"+session.SessionID)
	return nil
}

func (h *multiCampCommandHarness) Reopen(ctx context.Context, _ string, mode cli.OutputMode, out io.Writer) error {
	closed, err := h.selected(ctx, app.SelectionHistory)
	if err != nil {
		return err
	}
	h.routes = append(h.routes, "history:"+closed.SessionID)
	return h.Open(context.Background(), closed.Recovery.Configuration.Source, mode, out)
}

func (h *multiCampCommandHarness) Recover(context.Context, string, cli.OutputMode, io.Writer) error {
	return nil
}

func (h *multiCampCommandHarness) Supervise(context.Context, string, cli.OutputMode, io.Writer) error {
	return nil
}

func (h *multiCampCommandHarness) Status(ctx context.Context, _ cli.OutputMode, out io.Writer) error {
	session, err := h.selected(ctx, app.SelectionRecovery)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s %s %s\n", session.Capsule, session.SessionID, session.State)
	return err
}

func (h *multiCampCommandHarness) List(_ context.Context, _ cli.OutputMode, out io.Writer) error {
	camps := make([]string, 0, len(h.store.sessions))
	seen := map[string]bool{}
	for _, session := range h.store.sessions {
		if !seen[session.Capsule] {
			seen[session.Capsule] = true
			camps = append(camps, session.Capsule)
		}
	}
	sort.Strings(camps)
	_, err := fmt.Fprintln(out, strings.Join(camps, "\n"))
	return err
}

func runSmokeCommand(t *testing.T, lifecycle cli.Lifecycle, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), cli.NewRootWithLifecycle(lifecycle), args, cli.Streams{Out: &stdout, ErrOut: &stderr})
	if code != int(cli.ExitSuccess) {
		t.Fatalf("camp %v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestTwoCampCommandLifecycleDoesNotCrossSelect(t *testing.T) {
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	roots := []string{t.TempDir(), t.TempDir()}
	ids := []string{"alpha", "beta"}
	for index, root := range roots {
		if _, err := campconfig.Create(root, campconfig.Manifest{
			SchemaVersion: 1, ID: ids[index], Source: ".", Backend: backend,
			Workspace: campconfig.Workspace{Provider: "docker", Context: "default"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	harness := &multiCampCommandHarness{store: &smokeSessionStore{}}
	alphaOpen := strings.TrimSpace(runSmokeCommand(t, harness, "open", roots[0]))
	betaOpen := strings.TrimSpace(runSmokeCommand(t, harness, "open", roots[1]))
	listed := runSmokeCommand(t, harness, "list")
	if !strings.Contains(listed, "alpha") || !strings.Contains(listed, "beta") {
		t.Fatalf("list = %q", listed)
	}
	if status := runSmokeCommand(t, harness, "status", "--session", alphaOpen); !strings.Contains(status, "alpha "+alphaOpen+" open") {
		t.Fatalf("alpha status = %q", status)
	}
	runSmokeCommand(t, harness, "attach", "src", "--session", betaOpen)
	runSmokeCommand(t, harness, "sync", "--session", alphaOpen)
	runSmokeCommand(t, harness, "close", "--session", alphaOpen)
	runSmokeCommand(t, harness, "close", "--session", betaOpen)
	alphaReopened := strings.TrimSpace(runSmokeCommand(t, harness, "reopen", alphaOpen))
	if !strings.HasPrefix(alphaReopened, "alpha-session-") || alphaReopened == alphaOpen {
		t.Fatalf("reopened alpha session = %q", alphaReopened)
	}
	if status := runSmokeCommand(t, harness, "status", "--session", betaOpen); !strings.Contains(status, "beta "+betaOpen+" closed") {
		t.Fatalf("beta status after alpha reopen = %q", status)
	}
	wantRoutes := []string{
		"open:" + alphaOpen,
		"open:" + betaOpen,
		"attach:" + betaOpen + ":src",
		"sync:" + alphaOpen,
		"close:" + alphaOpen,
		"close:" + betaOpen,
		"history:" + alphaOpen,
		"open:" + alphaReopened,
	}
	if strings.Join(harness.routes, "\n") != strings.Join(wantRoutes, "\n") {
		t.Fatalf("routes:\n%s\nwant:\n%s", strings.Join(harness.routes, "\n"), strings.Join(wantRoutes, "\n"))
	}
}
