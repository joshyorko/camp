package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCrashOpenCommandRunsFromCampSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "camp source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(root, "print-working-directory")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\npwd\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer

	command := newCrashOpenCommand(context.Background(), probe, source, nil, &output)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(output.String()); got != source {
		t.Fatalf("crash open working directory = %q, want %q", got, source)
	}
}

func TestCleanupCrashSessionRunsFromCampSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "camp source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(root, "print-working-directory")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\npwd\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := cleanupCrashSession(context.Background(), nil, source, probe)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output)); got != source {
		t.Fatalf("crash cleanup working directory = %q, want %q", got, source)
	}
}

func TestCrashSessionNeedsCleanupClose(t *testing.T) {
	tests := []struct {
		name        string
		initialized bool
		closed      bool
		want        bool
	}{
		{name: "not initialized", initialized: false, closed: false, want: false},
		{name: "active", initialized: true, closed: false, want: true},
		{name: "already closed", initialized: true, closed: true, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := crashSessionNeedsCleanupClose(test.initialized, test.closed); got != test.want {
				t.Fatalf("crashSessionNeedsCleanupClose(%t, %t) = %t, want %t", test.initialized, test.closed, got, test.want)
			}
		})
	}
}

func TestWaitForForwarderEvidenceReportsOpeningExit(t *testing.T) {
	openingDone := make(chan error, 1)
	openingDone <- errors.New("open failed")
	output := bytes.NewBufferString("exact camp open failure")

	_, err := waitForForwarderEvidenceOrExit(
		context.Background(),
		t.TempDir(),
		"registry",
		openingDone,
		output,
	)
	if err == nil {
		t.Fatal("waitForForwarderEvidenceOrExit() error = nil")
	}
	for _, want := range []string{"camp open exited before registry forwarder evidence", "open failed", "exact camp open failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("waitForForwarderEvidenceOrExit() error = %q, want %q", err, want)
		}
	}
}

func TestWaitForForwarderEvidenceUsesControllerRuntimeRoot(t *testing.T) {
	controller := t.TempDir()
	want := filepath.Join(scenarioRuntimeDirectory(controller), "camp", "session-1", "registry-forward.json")
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got, err := waitForForwarderEvidenceOrExit(ctx, controller, "registry", make(chan error), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("forwarder evidence path = %q, want %q", got, want)
	}
	if sessionID := forwarderEvidenceSessionID(got); sessionID != "session-1" {
		t.Fatalf("forwarder evidence session ID = %q, want session-1", sessionID)
	}
}
