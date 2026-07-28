package devpod

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestIDEEntryValidateAcceptsSupportedModes(t *testing.T) {
	t.Parallel()
	for _, entry := range []IDEEntry{
		{IDE: IDETerminal},
		{IDE: IDEVSCode},
		{IDE: IDEVSCodeInsiders},
		{IDE: IDET3Code},
		{IDE: IDET3Code, Sites: true},
	} {
		entry := entry
		t.Run(string(entry.IDE), func(t *testing.T) {
			t.Parallel()
			if err := entry.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestIDEEntryValidateRejectsUnknownIDEAndSitesWithoutT3(t *testing.T) {
	t.Parallel()
	for _, entry := range []IDEEntry{
		{IDE: IDE("cursor")},
		{IDE: IDETerminal, Sites: true},
		{IDE: IDEVSCode, Sites: true},
		{IDE: IDEVSCodeInsiders, Sites: true},
	} {
		if err := entry.Validate(); !errors.Is(err, ErrInvalidIDEEntry) {
			t.Fatalf("IDEEntry%#v.Validate() error = %v, want ErrInvalidIDEEntry", entry, err)
		}
	}
}

func TestIDEEntryDevPodSetupDoesNotStartT3OrSites(t *testing.T) {
	t.Parallel()
	gotIDE, gotOpen, err := (IDEEntry{IDE: IDET3Code, Sites: true}).DevPodSetup()
	if err != nil {
		t.Fatalf("DevPodSetup() error = %v", err)
	}
	if gotIDE != IDETerminal || gotOpen {
		t.Fatalf("DevPodSetup() = (%q, %t), want (%q, false)", gotIDE, gotOpen, IDETerminal)
	}
}

func TestNestedVSCodeCommandUsesExactEncodedDevPodHostURI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ide  IDE
		want ports.Command
	}{
		{
			name: "stable",
			ide:  IDEVSCode,
			want: ports.Command{Executable: "code", Argv: []string{
				"--reuse-window", "--folder-uri", "vscode-remote://ssh-remote+camp-second-brain.devpod/workspaces/Second%20Brain/Memory%20D%23notes%3F",
			}},
		},
		{
			name: "insiders",
			ide:  IDEVSCodeInsiders,
			want: ports.Command{Executable: "code-insiders", Argv: []string{
				"--reuse-window", "--folder-uri", "vscode-remote://ssh-remote+camp-second-brain.devpod/workspaces/Second%20Brain/Memory%20D%23notes%3F",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := NestedIDECommand(IDEOpenOptions{
				IDE: IDEEntry{IDE: test.ide}, WorkspaceID: "camp-second-brain",
				ContainerTarget: "/workspaces/Second Brain/Memory D#notes?",
			})
			if err != nil {
				t.Fatalf("NestedIDECommand() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("NestedIDECommand() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNestedIDECommandRejectsUnsafeOrNonVSCodeInputs(t *testing.T) {
	t.Parallel()
	tests := []IDEOpenOptions{
		{IDE: IDEEntry{IDE: IDETerminal}, WorkspaceID: "camp", ContainerTarget: "/workspaces/root"},
		{IDE: IDEEntry{IDE: IDET3Code}, WorkspaceID: "camp", ContainerTarget: "/workspaces/root"},
		{IDE: IDEEntry{IDE: IDEVSCode}, WorkspaceID: "bad\nworkspace", ContainerTarget: "/workspaces/root"},
		{IDE: IDEEntry{IDE: IDEVSCode}, WorkspaceID: "camp", ContainerTarget: "relative/path"},
	}
	for _, options := range tests {
		if _, err := NestedIDECommand(options); !errors.Is(err, ErrInvalidIDEEntry) {
			t.Fatalf("NestedIDECommand(%#v) error = %v, want ErrInvalidIDEEntry", options, err)
		}
	}
}

func TestWorkspaceSSHHostRejectsOverlongDNSLabel(t *testing.T) {
	t.Parallel()
	workspaceID := strings.Repeat("a", 64)
	if _, err := WorkspaceSSHHost(workspaceID); !errors.Is(err, ErrInvalidIDEEntry) {
		t.Fatalf("WorkspaceSSHHost(%q) error = %v, want ErrInvalidIDEEntry", workspaceID, err)
	}
}

func TestWorkspaceSSHHostAcceptsMaximumDNSLabel(t *testing.T) {
	t.Parallel()
	workspaceID := strings.Repeat("a", 63)
	host, err := WorkspaceSSHHost(workspaceID)
	if err != nil {
		t.Fatalf("WorkspaceSSHHost(%q) error = %v", workspaceID, err)
	}
	if want := workspaceID + ".devpod"; host != want {
		t.Fatalf("WorkspaceSSHHost(%q) = %q, want %q", workspaceID, host, want)
	}
}

func TestOpenNestedIDERunsOneDirectLauncherWithoutDevPodSSH(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{result: ports.Result{ExitCode: 17}}
	client := NewClient("/opt/devpod", runner)
	got, err := client.OpenNestedIDE(context.Background(), IDEOpenOptions{
		IDE: IDEEntry{IDE: IDEVSCodeInsiders}, WorkspaceID: "camp-second-brain", ContainerTarget: "/workspaces/Second Brain/Memory D",
	})
	if err != nil {
		t.Fatalf("OpenNestedIDE() error = %v", err)
	}
	if !reflect.DeepEqual(got, runner.result) {
		t.Fatalf("result = %#v, want %#v", got, runner.result)
	}
	want := ports.Command{Executable: "code-insiders", Argv: []string{
		"--reuse-window", "--folder-uri", "vscode-remote://ssh-remote+camp-second-brain.devpod/workspaces/Second%20Brain/Memory%20D",
	}}
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0], want) {
		t.Fatalf("commands = %#v, want [%#v]", runner.commands, want)
	}
}
