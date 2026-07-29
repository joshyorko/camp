package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	devPodEvidenceSectionLimit = 16 << 10
	devPodEvidenceTotalLimit   = 64 << 10
)

var (
	devPodEvidenceControlPattern = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]|\x1b\[[0-?]*[ -/]*[@-~]`)
	devPodEvidenceSecretPattern  = regexp.MustCompile(`(?i)(password|secret|token|credential|authorization|access[_-]?key|private[_-]?key)(["'=:\s]+)([^\s",}]+)`)
	dockerContainerIDPattern     = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

type lifecycleDiagnosticCommand func(context.Context, string, ...string) ([]byte, error)

type lifecycleDiagnosticSection struct {
	name   string
	output []byte
	err    error
}

func logDevPodFailureEvidence(t *testing.T, ctx context.Context, isolation devPodTestIsolation) {
	t.Helper()
	sections := collectDevPodFailureEvidence(
		ctx,
		func(ctx context.Context, command string, argv ...string) ([]byte, error) {
			return runDevPodCommand(ctx, isolation, command, argv...)
		},
		func(ctx context.Context, command string, argv ...string) ([]byte, error) {
			return runLifecycleCommand(ctx, nil, "docker", append([]string{command}, argv...)...)
		},
	)
	remaining := devPodEvidenceTotalLimit
	for _, section := range sections {
		if remaining <= 0 {
			t.Log("DevPod failure evidence truncated at 64 KiB")
			return
		}
		body := boundedRedactedDiagnostic(section.output, min(devPodEvidenceSectionLimit, remaining))
		remaining -= len(body)
		t.Logf("DevPod failure evidence %s (command error: %v):\n%s", section.name, section.err, body)
	}
}

func collectDevPodFailureEvidence(
	ctx context.Context,
	runDevPod lifecycleDiagnosticCommand,
	runDocker lifecycleDiagnosticCommand,
) []lifecycleDiagnosticSection {
	var sections []lifecycleDiagnosticSection
	listOutput, listErr := runBoundedDiagnosticCommand(ctx, runDevPod, "list", "--output", "json", "--skip-pro")
	sections = append(sections, lifecycleDiagnosticSection{name: "workspace list", output: listOutput, err: listErr})

	var workspaces []struct {
		ID string `json:"id"`
	}
	if listErr != nil || json.Unmarshal(listOutput, &workspaces) != nil {
		return sections
	}
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].ID < workspaces[j].ID })
	for _, workspace := range workspaces {
		if workspace.ID == "" {
			continue
		}
		for _, command := range []struct {
			name string
			argv []string
		}{
			{name: "status", argv: []string{"status", workspace.ID, "--output", "json", "--timeout", "10s"}},
			{name: "logs", argv: []string{"logs", workspace.ID}},
		} {
			output, err := runBoundedDiagnosticCommand(ctx, runDevPod, command.argv[0], command.argv[1:]...)
			sections = append(sections, lifecycleDiagnosticSection{
				name:   fmt.Sprintf("workspace %s DevPod %s", workspace.ID, command.name),
				output: output,
				err:    err,
			})
		}

		label := "dev.containers.id=" + workspace.ID
		containerOutput, containerErr := runBoundedDiagnosticCommand(
			ctx,
			runDocker,
			"ps",
			"--all",
			"--no-trunc",
			"--filter",
			"label="+label,
			"--format",
			"{{.ID}}",
		)
		sections = append(sections, lifecycleDiagnosticSection{
			name:   fmt.Sprintf("workspace %s exact Docker containers", workspace.ID),
			output: containerOutput,
			err:    containerErr,
		})
		for _, containerID := range strings.Fields(string(containerOutput)) {
			if !dockerContainerIDPattern.MatchString(containerID) {
				continue
			}
			for _, command := range []struct {
				name string
				argv []string
			}{
				{
					name: "inspect state",
					argv: []string{"inspect", "--format", "{{json .State}}", containerID},
				},
				{
					name: "inspect identity",
					argv: []string{"inspect", "--format", "{{json .Name}} {{json .Config.Image}} {{json .Mounts}}", containerID},
				},
				{
					name: "logs",
					argv: []string{"logs", "--tail", "200", containerID},
				},
			} {
				output, err := runBoundedDiagnosticCommand(ctx, runDocker, command.argv[0], command.argv[1:]...)
				sections = append(sections, lifecycleDiagnosticSection{
					name:   fmt.Sprintf("workspace %s container %s Docker %s", workspace.ID, containerID, command.name),
					output: output,
					err:    err,
				})
			}
		}
		eventsOutput, eventsErr := runBoundedDiagnosticCommand(
			ctx,
			runDocker,
			"events",
			"--since",
			"15m",
			"--until",
			"now",
			"--filter",
			"label="+label,
			"--format",
			"{{json .}}",
		)
		sections = append(sections, lifecycleDiagnosticSection{
			name:   fmt.Sprintf("workspace %s exact Docker events", workspace.ID),
			output: eventsOutput,
			err:    eventsErr,
		})
	}
	return sections
}

func runBoundedDiagnosticCommand(
	ctx context.Context,
	run lifecycleDiagnosticCommand,
	command string,
	argv ...string,
) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return run(commandCtx, command, argv...)
}

func boundedRedactedDiagnostic(output []byte, limit int) string {
	if limit <= 0 {
		return ""
	}
	text := devPodEvidenceControlPattern.ReplaceAllString(string(output), "")
	text = devPodEvidenceSecretPattern.ReplaceAllString(text, `${1}${2}[REDACTED]`)
	if len(text) <= limit {
		return text
	}
	marker := "[truncated to final diagnostic bytes]\n"
	if limit <= len(marker) {
		return marker[:limit]
	}
	return marker + text[len(text)-(limit-len(marker)):]
}

func TestCollectDevPodFailureEvidenceUsesExactWorkspaceLabels(t *testing.T) {
	var devPodCalls, dockerCalls []string
	devPodRunner := func(_ context.Context, command string, argv ...string) ([]byte, error) {
		devPodCalls = append(devPodCalls, strings.Join(append([]string{command}, argv...), " "))
		switch command {
		case "list":
			return []byte(`[{"id":"workspace-b"},{"id":"workspace-a"}]`), nil
		case "status":
			return []byte(`{"state":"Stopped"}`), nil
		case "logs":
			return []byte("agent log"), nil
		default:
			return nil, fmt.Errorf("unexpected DevPod command %q", command)
		}
	}
	dockerRunner := func(_ context.Context, command string, argv ...string) ([]byte, error) {
		call := strings.Join(append([]string{command}, argv...), " ")
		dockerCalls = append(dockerCalls, call)
		if command == "ps" && strings.Contains(call, "workspace-a") {
			return []byte("0123456789abcdef\n"), nil
		}
		return nil, nil
	}

	sections := collectDevPodFailureEvidence(context.Background(), devPodRunner, dockerRunner)
	if len(sections) != 12 {
		t.Fatalf("diagnostic sections = %d, want 12", len(sections))
	}
	if got := strings.Join(devPodCalls, "\n"); !strings.Contains(got, "status workspace-a --output json --timeout 10s") ||
		!strings.Contains(got, "logs workspace-b") {
		t.Fatalf("DevPod calls omitted workspace diagnostics:\n%s", got)
	}
	for _, workspace := range []string{"workspace-a", "workspace-b"} {
		label := "label=dev.containers.id=" + workspace
		if got := strings.Join(dockerCalls, "\n"); !strings.Contains(got, label) {
			t.Fatalf("Docker calls omitted exact label %q:\n%s", label, got)
		}
	}
	if got := strings.Join(dockerCalls, "\n"); strings.Contains(got, "docker ps") || strings.Contains(got, "--filter name=") {
		t.Fatalf("Docker discovery was not label-scoped:\n%s", got)
	}
}

func TestBoundedRedactedDiagnostic(t *testing.T) {
	input := []byte("\x1b[31mstart\x1b[0m token=do-not-log\n" + strings.Repeat("x", 128))
	got := boundedRedactedDiagnostic(input, 80)
	if len(got) != 80 {
		t.Fatalf("bounded diagnostic length = %d, want 80", len(got))
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "do-not-log") {
		t.Fatalf("diagnostic leaked controls or secret: %q", got)
	}
	if !strings.HasPrefix(got, "[truncated to final diagnostic bytes]\n") {
		t.Fatalf("diagnostic omitted truncation marker: %q", got)
	}
}

func TestDockerDiagnosticContainerIDValidation(t *testing.T) {
	if !dockerContainerIDPattern.MatchString(strings.Repeat("a", 64)) {
		t.Fatal("valid Docker container ID rejected")
	}
	for _, invalid := range []string{"short", strings.Repeat("g", 64), "abc;docker-rm"} {
		if dockerContainerIDPattern.MatchString(invalid) {
			t.Fatalf("unsafe Docker container ID %q accepted", invalid)
		}
	}
}
