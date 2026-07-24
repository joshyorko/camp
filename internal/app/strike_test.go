package app

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

type strikeJournal struct{ sessions []domain.JournalSnapshot }

func (j strikeJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	return j.sessions, nil
}

type strikeController struct{ called bool }

func (c *strikeController) Archive(context.Context, StrikePlan) (string, error) {
	c.called = true
	return "/tmp/archive", nil
}
func (c *strikeController) Purge(context.Context, StrikePlan) error { c.called = true; return nil }

func TestStrikeRefusesActiveSession(t *testing.T) {
	controller := &strikeController{}
	_, err := (Strike{Sessions: strikeJournal{sessions: []domain.JournalSnapshot{{State: domain.SessionOpen}}}, Controller: controller}).Run(context.Background(), StrikeRequest{}, StrikePlan{})
	if err == nil || controller.called {
		t.Fatal("active session was not rejected before mutation")
	}
}

func TestStrikeRequiresPurgeConfirmation(t *testing.T) {
	controller := &strikeController{}
	_, err := (Strike{Sessions: strikeJournal{}, Controller: controller}).Run(context.Background(), StrikeRequest{Purge: true}, StrikePlan{})
	if err == nil || controller.called {
		t.Fatal("unconfirmed purge was not rejected")
	}
}

func TestStrikeArchivesClosedState(t *testing.T) {
	controller := &strikeController{}
	result, err := (Strike{Sessions: strikeJournal{}, Controller: controller}).Run(context.Background(), StrikeRequest{}, StrikePlan{BackendSafe: true})
	if err != nil || !controller.called || result.ArchivedPath == "" {
		t.Fatalf("archive: result=%+v err=%v", result, err)
	}
}

func TestStrikeRejectsUnsupportedBackend(t *testing.T) {
	controller := &strikeController{}
	_, err := (Strike{Sessions: strikeJournal{}, Controller: controller}).Run(context.Background(), StrikeRequest{}, StrikePlan{BackendSafe: false})
	if !errors.Is(err, ErrStrikeBackend) || controller.called {
		t.Fatalf("backend rejection: %v", err)
	}
}
