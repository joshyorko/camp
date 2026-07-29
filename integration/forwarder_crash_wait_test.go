package integration

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

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
