package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/setupui"
)

func TestRichSetupPipelineContinuesThroughCampInitialization(t *testing.T) {
	stateRoot := t.TempDir()
	campRoot := filepath.Join(t.TempDir(), "test-camp-robot")
	if err := os.MkdirAll(campRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf '[{\"Descriptor\":{\"digest\":\"sha256:"+digest+"\"}}]'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateRoot, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(stateRoot, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(stateRoot, "cache"))

	lifecycle := &ProductionLifecycle{
		setupToolRunner: func(context.Context, OutputMode, io.Writer, func(string, tooladapter.Resolution) error) error {
			return nil
		},
	}
	pipeline := newRichSetupPipeline(lifecycle, context.Background())
	var accepted setupui.ConfigAcceptedMsg
	for message := range pipeline.Start(map[string]string{
		"root":     campRoot,
		"name":     "test_camp",
		"backend":  "file://" + filepath.Join(stateRoot, "backend"),
		"provider": "room-of-requirement",
		"context":  "default",
	}) {
		if value, ok := message.(setupui.ConfigAcceptedMsg); ok {
			accepted = value
		}
	}

	manifest, err := os.ReadFile(filepath.Join(campRoot, ".camp", "camp.yaml"))
	if err != nil {
		t.Fatalf("read camp manifest: %v", err)
	}
	if !strings.Contains(string(manifest), "id: test_camp") {
		t.Fatalf("manifest = %q", manifest)
	}
	if accepted.ReadyLine != "test_camp is initialized" || accepted.NextCmd != "cd "+campRoot+" && camp open" {
		t.Fatalf("accepted = %#v", accepted)
	}
}
