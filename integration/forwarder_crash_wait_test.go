package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
