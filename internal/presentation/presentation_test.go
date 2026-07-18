package presentation

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/app"
)

func TestJSONSessionsEnvelopeIsVersionedDeterministicAndRedacted(t *testing.T) {
	t.Parallel()
	models := lifecycleModels()
	models[0].Cleanup.Message = "token=super-secret"
	body, err := MarshalSessionsJSON(models)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "super-secret") || strings.Contains(string(body), "\x1b[") {
		t.Fatalf("unsafe JSON: %s", body)
	}
	var envelope struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Kind          string                 `json:"kind"`
		Sessions      []app.SessionReadModel `json:"sessions"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.Kind != "sessions" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if got := []string{envelope.Sessions[0].ID, envelope.Sessions[1].ID, envelope.Sessions[2].ID, envelope.Sessions[3].ID}; !reflect.DeepEqual(got, []string{"a-opening", "b-open", "c-recovering", "d-closed"}) {
		t.Fatalf("session order = %v", got)
	}
}

func TestFailureContractsHaveStableCodesCommandsAndStreams(t *testing.T) {
	t.Parallel()
	failure := Failure{Code: "session_ambiguous", Message: "credential=secret ambiguous", NextCommands: []string{"camp status --session zeta", "camp status --session alpha"}}
	body, err := MarshalFailureJSON(failure)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"schemaVersion\": 1,\n  \"kind\": \"error\",\n  \"error\": {\n    \"code\": \"session_ambiguous\",\n    \"message\": \"credential=[REDACTED] ambiguous\",\n    \"nextCommands\": [\n      \"camp status --session alpha\",\n      \"camp status --session zeta\"\n    ]\n  }\n}\n"
	if string(body) != want {
		t.Fatalf("JSON failure:\n%s\nwant:\n%s", body, want)
	}
	if HumanStream(false) != StreamStdout || HumanStream(true) != StreamStderr || JSONStream() != StreamStdout {
		t.Fatalf("stream contract changed")
	}
}

func TestHumanGoldensCoverLifecycleAndFailureClasses(t *testing.T) {
	t.Parallel()
	assertGolden(t, "testdata/sessions.golden", RenderSessionsHuman(lifecycleModels()))
	failures := []Failure{
		{Code: "session_not_found", Message: "no matching Camp session", NextCommands: []string{"camp list"}},
		{Code: "session_ambiguous", Message: "multiple active Camp sessions", NextCommands: []string{"camp status --session a", "camp status --session b"}},
		{Code: "publication_orphaned", Message: "generation uploaded but pointer not committed", NextCommands: []string{"camp recover orphan"}},
		{Code: "cleanup_failed", Message: "checkpoint published; cleanup failed", NextCommands: []string{"camp recover closed"}},
	}
	var rendered strings.Builder
	for _, failure := range failures {
		rendered.WriteString(RenderFailureHuman(failure))
	}
	assertGolden(t, "testdata/failures.golden", rendered.String())
}

func assertGolden(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch for %s\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func lifecycleModels() []app.SessionReadModel {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	return []app.SessionReadModel{
		{ID: "d-closed", Capsule: "brain", Branch: "main", State: "closed", Services: []app.ServiceReadModel{}, Publication: app.PublicationReadModel{Condition: app.PublicationPublished, Generation: 44}, Cleanup: app.CleanupReadModel{Condition: app.CleanupSucceeded}, Recovery: app.RecoveryReadModel{Condition: app.RecoveryNone}, CreatedAt: now, UpdatedAt: now},
		{ID: "b-open", Capsule: "brain", Branch: "main", State: "open", Services: []app.ServiceReadModel{{Name: "registry", Liveness: app.ServiceLivenessLive}}, Publication: app.PublicationReadModel{Condition: app.PublicationNone}, Cleanup: app.CleanupReadModel{Condition: app.CleanupPending}, Recovery: app.RecoveryReadModel{Condition: app.RecoveryNone}, CreatedAt: now, UpdatedAt: now},
		{ID: "a-opening", Capsule: "brain", Branch: "feature", State: "opening", Services: []app.ServiceReadModel{{Name: "registry", Liveness: app.ServiceLivenessUnknown}}, Publication: app.PublicationReadModel{Condition: app.PublicationNone}, Cleanup: app.CleanupReadModel{Condition: app.CleanupPending}, Recovery: app.RecoveryReadModel{Condition: app.RecoveryNone}, CreatedAt: now, UpdatedAt: now},
		{ID: "c-recovering", Capsule: "brain", Branch: "main", State: "recovering", Services: []app.ServiceReadModel{{Name: "files", Liveness: app.ServiceLivenessPIDReused}}, Publication: app.PublicationReadModel{Condition: app.PublicationOrphaned, Generation: 45}, Cleanup: app.CleanupReadModel{Condition: app.CleanupFailed}, Recovery: app.RecoveryReadModel{Condition: app.RecoveryCleanupOnly, Command: "camp recover c-recovering"}, CreatedAt: now, UpdatedAt: now},
	}
}
