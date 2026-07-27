package remoteworker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type hydrationFixture struct {
	order     []string
	workspace string
	stage     string
}

func (fixture *hydrationFixture) Verify(context.Context, Request) (verifiedRuntimeKit, error) {
	fixture.order = append(fixture.order, "verify")
	return verifiedRuntimeKit{Store: "/runtime/store", RootSHA256: strings.Repeat("a", 64)}, nil
}

func (fixture *hydrationFixture) ExtractRoot(context.Context, Request, verifiedRuntimeKit) (string, error) {
	fixture.order = append(fixture.order, "extract")
	return fixture.stage, nil
}

func (fixture *hydrationFixture) InstallTools(Request, verifiedRuntimeKit) error {
	fixture.order = append(fixture.order, "tools")
	return nil
}

func (fixture *hydrationFixture) Promote(stage, workspace string) error {
	fixture.order = append(fixture.order, "promote")
	return promoteHydratedRoot(stage, workspace, nil)
}

func (fixture *hydrationFixture) Publish(_ Request, receipt HydrationReceipt) error {
	fixture.order = append(fixture.order, "receipt:"+receipt.Status)
	return nil
}

func TestHydrateVerifiesExtractsPromotesThenPublishesCompletion(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	stage := filepath.Join(parent, "stage")
	if err := os.MkdirAll(filepath.Join(workspace, ".camp-bootstrap"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "root.txt"), []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &hydrationFixture{workspace: workspace, stage: stage}
	request := validRequest()
	request.Operation = OperationHydrate
	request.WorkspaceRoot = workspace
	receipt, err := hydrateWorkspace(t.Context(), request, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "completed" ||
		strings.Join(fixture.order, ",") != "verify,extract,tools,promote,receipt:completed" {
		t.Fatalf("receipt=%#v order=%v", receipt, fixture.order)
	}
}

func TestPromoteHydratedRootPreservesOnlyBootstrapAndRuntime(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	stage := filepath.Join(parent, "stage")
	for _, path := range []string{
		filepath.Join(workspace, ".camp-bootstrap"),
		filepath.Join(workspace, ".camp", "runtime"),
		filepath.Join(stage, ".devcontainer"),
		filepath.Join(stage, ".camp", "build"),
		filepath.Join(stage, "src"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "README.md"), []byte("root"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := promoteHydratedRoot(stage, workspace, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(workspace, ".camp-bootstrap"),
		filepath.Join(workspace, ".camp", "runtime"),
		filepath.Join(workspace, ".camp", "build"),
		filepath.Join(workspace, ".devcontainer"),
		filepath.Join(workspace, "src"),
		filepath.Join(workspace, "README.md"),
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("promoted path %q: %v", path, err)
		}
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("stage entries remain: %v", entries)
	}
}

func TestPromoteHydratedRootRejectsUnexpectedWorkspaceEntryWithoutMutation(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	stage := filepath.Join(parent, "stage")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "user.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "root.txt"), []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := promoteHydratedRoot(stage, workspace, nil); err == nil {
		t.Fatal("promoteHydratedRoot() error = nil")
	}
	if body, err := os.ReadFile(filepath.Join(workspace, "user.txt")); err != nil || string(body) != "keep" {
		t.Fatalf("workspace entry changed: body=%q err=%v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(stage, "root.txt")); err != nil || string(body) != "root" {
		t.Fatalf("stage entry changed: body=%q err=%v", body, err)
	}
}
