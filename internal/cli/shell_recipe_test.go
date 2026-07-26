//go:build linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	campcontract "github.com/joshyorko/camp"
	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/setupui"
)

func TestGeneratedShellRecipesPreserveLiteralArguments(t *testing.T) {
	workingDirectory := t.TempDir()
	rootMarker := filepath.Join(workingDirectory, "root-substitution-ran")
	root := "root $(touch${IFS}root-substitution-ran)"
	if err := os.Mkdir(filepath.Join(workingDirectory, root), 0o700); err != nil {
		t.Fatal(err)
	}
	capsuleMarker := filepath.Join(t.TempDir(), "capsule-separator-ran")
	capsule := "alpha's camp;touch$IFS" + capsuleMarker

	pipeline := newRichInitPipeline(context.Background(), InitRequest{Root: root}, func(context.Context, InitRequest, OutputMode, io.Writer) error {
		return nil
	})
	var next string
	for message := range pipeline.Start(map[string]string{"name": capsule}) {
		if accepted, ok := message.(setupui.ConfigAcceptedMsg); ok {
			next = accepted.NextCmd
		}
	}
	if got := runCampRecipe(t, workingDirectory, next); !reflect.DeepEqual(got, []string{filepath.Join(workingDirectory, root), "open"}) {
		t.Fatalf("rich next command arguments = %#v", got)
	}
	assertPathAbsent(t, rootMarker)

	failedPipeline := newRichInitPipeline(context.Background(), InitRequest{Root: root}, func(context.Context, InitRequest, OutputMode, io.Writer) error {
		return errors.New("initialization failed")
	})
	var recovery string
	for message := range failedPipeline.Start(map[string]string{"name": capsule}) {
		if failed, ok := message.(setupui.WaypointFailedMsg); ok {
			recovery = failed.Recovery
		}
	}
	if got := runCampRecipe(t, workingDirectory, recovery)[1:]; !reflect.DeepEqual(got, []string{"init", root, "--name", capsule}) {
		t.Fatalf("rich recovery command arguments = %#v", got)
	}
	assertPathAbsent(t, rootMarker)
	assertPathAbsent(t, capsuleMarker)

	providerMarker := filepath.Join(workingDirectory, "provider-separator-ran")
	contextMarker := filepath.Join(workingDirectory, "context-substitution-ran")
	provider := "remote;touch${IFS}provider-separator-ran"
	contextName := "work$(touch${IFS}context-substitution-ran)"
	if err := config.ValidateDevPodProvider(provider); err != nil {
		t.Fatalf("hostile provider should reach the recipe renderer: %v", err)
	}
	if err := config.ValidateDevPodContext(contextName); err != nil {
		t.Fatalf("hostile context should reach the recipe renderer: %v", err)
	}
	probes := productionReachabilityProbes(config.Bootstrap{DevPodProvider: provider, DevPodContext: contextName}, nil, doctorReachabilityToolResolver{})
	var remediation string
	for _, probe := range probes {
		if probe.Capability() == "provider" {
			remediation = probe.Probe(context.Background()).Remediation
			break
		}
	}
	if got := runCampRecipe(t, workingDirectory, remediation)[1:]; !reflect.DeepEqual(got, []string{"provider", "use", provider, "--context", contextName}) {
		t.Fatalf("doctor remediation arguments = %#v", got)
	}
	assertPathAbsent(t, providerMarker)
	assertPathAbsent(t, contextMarker)

	lock, err := tooladapter.ParseLock(strings.NewReader(string(campcontract.DistributionToolLock())))
	if err != nil {
		t.Fatal(err)
	}
	sourceMarker := filepath.Join(t.TempDir(), "source-substitution-ran")
	source := "/work/source$(touch$IFS" + sourceMarker + ")"
	model, err := buildCampsiteModel(
		lock,
		config.Runtime{Bootstrap: config.Bootstrap{Capsule: "alpha", Source: source}},
		config.Backend{Kind: config.BackendFile, SanitizedURL: "file:///store"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCampRecipe(t, workingDirectory, model.NextCommand)[1:]; !reflect.DeepEqual(got, []string{"open", source}) {
		t.Fatalf("campsite next command arguments = %#v", got)
	}
	assertPathAbsent(t, sourceMarker)
}

func TestSetupPATHExportPreservesLiteralDirectories(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "path-substitution-ran")
	managedDir := "/managed/$(touch$IFS" + marker + ")"
	ensurer := &recordingToolEnsurer{resolutions: map[string]tooladapter.Resolution{
		"devpod": {Path: filepath.Join(managedDir, "devpod"), Managed: true},
		"hauler": {Path: "/other managed/hauler", Managed: true},
	}}
	var output bytes.Buffer
	if err := runManagedToolSetup(context.Background(), ModeJSON, &output, ensurer, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result setupResult `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", envelope.Result.PATHExport+`; printf '%s' "$PATH"`)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	body, err := command.Output()
	if err != nil {
		t.Fatalf("execute PATH export: %v", err)
	}
	want := strings.Join(envelope.Result.PATH, ":") + ":/usr/bin:/bin"
	if string(body) != want {
		t.Fatalf("PATH = %q, want %q", body, want)
	}
	assertPathAbsent(t, marker)
}

func runCampRecipe(t *testing.T, workingDirectory, recipe string) []string {
	t.Helper()
	script := `camp() { printf '%s\n' "$PWD" "$@"; }; ` + recipe
	command := exec.Command("sh", "-c", script)
	command.Dir = workingDirectory
	body, err := command.Output()
	if err != nil {
		t.Fatalf("execute recipe %q: %v", recipe, err)
	}
	return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell recipe executed unintended side effect %q: %v", path, err)
	}
}
