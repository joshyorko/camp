package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"golang.org/x/sys/unix"
)

type hydrationFixture struct {
	order         []string
	workspace     string
	stage         string
	mutateRuntime bool
}

type receiptObservingHydrationFixture struct{ hydrationFixture }

func (fixture *receiptObservingHydrationFixture) ObserveCompleted(request Request) (HydrationReceipt, bool, error) {
	fixture.order = append(fixture.order, "observe")
	return newProductionHydrationRuntime().ObserveCompleted(request)
}

func (fixture *hydrationFixture) Verify(_ context.Context, request Request) (verifiedRuntimeKit, error) {
	fixture.order = append(fixture.order, "verify")
	if fixture.mutateRuntime {
		if err := os.MkdirAll(filepath.Join(request.RuntimeRoot, "kit"), 0o700); err != nil {
			return verifiedRuntimeKit{}, err
		}
	}
	return verifiedRuntimeKit{Store: "/runtime/store", RootSHA256: strings.Repeat("a", 64)}, nil
}

func (fixture *hydrationFixture) AdmitWorkspace(workspace string) error {
	fixture.order = append(fixture.order, "admit")
	workspaceFD, _, err := openOperationDirectory(workspace)
	if err != nil {
		return err
	}
	defer unix.Close(workspaceFD)
	return validateInitialWorkspace(workspaceFD)
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
		strings.Join(fixture.order, ",") != "admit,verify,extract,tools,promote,receipt:completed" {
		t.Fatalf("receipt=%#v order=%v", receipt, fixture.order)
	}
}

func TestHydrateRejectsIneligibleWorkspaceBeforeVerifierRuntimeMutation(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	stage := filepath.Join(parent, "stage")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "user.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &hydrationFixture{workspace: workspace, stage: stage, mutateRuntime: true}
	request := validRequest()
	request.Operation = OperationHydrate
	request.WorkspaceRoot = workspace
	request.RuntimeRoot = filepath.Join(workspace, ".camp", "runtime")
	if _, err := hydrateWorkspace(t.Context(), request, fixture); !errors.Is(err, ErrUnsafeHydration) {
		t.Fatalf("hydrateWorkspace() error = %v", err)
	}
	if got := strings.Join(fixture.order, ","); got != "admit" {
		t.Fatalf("hydration order = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(workspace, ".camp", "runtime")); !os.IsNotExist(err) {
		t.Fatalf("runtime was mutated before admission failed: %v", err)
	}
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatalf("root was extracted before admission failed: %v", err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 1 || entries[0].Name() != "user.txt" {
		t.Fatalf("workspace changed before admission failed: entries=%v err=%v", entries, err)
	}
	if body, err := os.ReadFile(filepath.Join(workspace, "user.txt")); err != nil || string(body) != "keep" {
		t.Fatalf("workspace bytes changed before admission failed: body=%q err=%v", body, err)
	}
}

func TestHydrateCompletedReceiptBypassesAdmissionAndVerification(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	request := hydrationRequestWithTrustedManifest(t, workspace, strings.Repeat("a", 64))
	if err := os.MkdirAll(request.RuntimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hydrated"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := newHydrationReceipt(request, strings.Repeat("a", 64))
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.RuntimeRoot, "hydrate.receipt.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &receiptObservingHydrationFixture{hydrationFixture: hydrationFixture{mutateRuntime: true}}
	receipt, err := hydrateWorkspace(t.Context(), request, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if receipt != want || strings.Join(fixture.order, ",") != "observe" {
		t.Fatalf("receipt=%#v order=%v", receipt, fixture.order)
	}
}

func TestInvalidCompletedReceiptDoesNotBypassAdmission(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*HydrationReceipt)
		symlink bool
	}{
		{name: "forged", mutate: func(receipt *HydrationReceipt) { receipt.Status = "forged" }},
		{name: "mismatched", mutate: func(receipt *HydrationReceipt) { receipt.Expected.Kit.SHA256 = strings.Repeat("c", 64) }},
		{name: "valid but wrong root digest", mutate: func(receipt *HydrationReceipt) { receipt.RootSHA256 = strings.Repeat("b", 64) }},
		{name: "stale", mutate: func(receipt *HydrationReceipt) { receipt.SessionID = "old-session" }},
		{name: "symlinked", symlink: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "workspace")
			request := hydrationRequestWithTrustedManifest(t, workspace, strings.Repeat("a", 64))
			if err := os.MkdirAll(filepath.Join(workspace, ".camp", "runtime"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "user.txt"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			receipt := newHydrationReceipt(request, strings.Repeat("a", 64))
			if test.mutate != nil {
				test.mutate(&receipt)
			}
			path := filepath.Join(workspace, ".camp", "runtime", "hydrate.receipt.json")
			if test.symlink {
				if err := os.Symlink("../../user.txt", path); err != nil {
					t.Fatal(err)
				}
			} else {
				body, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fixture := &receiptObservingHydrationFixture{hydrationFixture: hydrationFixture{mutateRuntime: true}}
			if _, err := hydrateWorkspace(t.Context(), request, fixture); !errors.Is(err, ErrUnsafeHydration) {
				t.Fatalf("hydrateWorkspace() error = %v", err)
			}
			if got := strings.Join(fixture.order, ","); got != "observe,admit" {
				t.Fatalf("hydration order = %q", got)
			}
			if _, err := os.Lstat(filepath.Join(request.RuntimeRoot, "kit")); !os.IsNotExist(err) {
				t.Fatalf("verifier mutated runtime after invalid receipt: %v", err)
			}
		})
	}
}

func TestReplacedManifestCannotAuthorizeCompletedReceipt(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	request := hydrationRequestWithTrustedManifest(t, workspace, strings.Repeat("a", 64))
	if err := os.MkdirAll(request.RuntimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "user.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := canonicalHydrationManifest(t, strings.Repeat("b", 64))
	if err := os.WriteFile(request.ManifestPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := newHydrationReceipt(request, strings.Repeat("b", 64))
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.RuntimeRoot, "hydrate.receipt.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	fixture := &receiptObservingHydrationFixture{hydrationFixture: hydrationFixture{mutateRuntime: true}}
	if _, err := hydrateWorkspace(t.Context(), request, fixture); !errors.Is(err, ErrUnsafeHydration) {
		t.Fatalf("hydrateWorkspace() error = %v", err)
	}
	if got := strings.Join(fixture.order, ","); got != "observe,admit" {
		t.Fatalf("hydration order = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(request.RuntimeRoot, "kit")); !os.IsNotExist(err) {
		t.Fatalf("verifier mutated runtime after replaced manifest: %v", err)
	}
}

func TestSymlinkedManifestCannotAuthorizeCompletedReceipt(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	request := hydrationRequestWithTrustedManifest(t, workspace, strings.Repeat("a", 64))
	trustedCopy := filepath.Join(filepath.Dir(request.ManifestPath), "trusted-copy.json")
	if err := os.Rename(request.ManifestPath, trustedCopy); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(trustedCopy), request.ManifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(request.RuntimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "user.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := newHydrationReceipt(request, strings.Repeat("a", 64))
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.RuntimeRoot, "hydrate.receipt.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	fixture := &receiptObservingHydrationFixture{hydrationFixture: hydrationFixture{mutateRuntime: true}}
	if _, err := hydrateWorkspace(t.Context(), request, fixture); !errors.Is(err, ErrUnsafeHydration) {
		t.Fatalf("hydrateWorkspace() error = %v", err)
	}
	if got := strings.Join(fixture.order, ","); got != "observe,admit" {
		t.Fatalf("hydration order = %q", got)
	}
}

func hydrationRequestWithTrustedManifest(t *testing.T, workspace, rootSHA256 string) Request {
	t.Helper()
	request := validRequest()
	request.Operation = OperationHydrate
	request.WorkspaceRoot = workspace
	request.RuntimeRoot = filepath.Join(workspace, ".camp", "runtime")
	request.ManifestPath = filepath.Join(filepath.Dir(workspace), ".camp-bootstrap", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(request.ManifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := canonicalHydrationManifest(t, rootSHA256)
	if err := os.WriteFile(request.ManifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	request.Expected.Manifest = FileIdentity{
		Name:   "manifest.json",
		SHA256: fmt.Sprintf("%x", digest),
		Size:   int64(len(body)),
	}
	return request
}

func canonicalHydrationManifest(t *testing.T, rootSHA256 string) []byte {
	t.Helper()
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	body, err := haulkit.MarshalCanonical(haulkit.Manifest{
		SchemaVersion: haulkit.ManifestSchemaVersion,
		Kind:          "camp-hauler-kit",
		SessionID:     "session-1",
		Capsule:       "capsule",
		Lineage:       domain.Lineage{Branch: "main"},
		Architecture:  "linux/amd64",
		Store: haulkit.StoreIdentity{
			HaulerVersion: "v2.0.2",
			IndexSHA256:   digest,
			Entries:       []haulkit.StoreEntry{{Reference: "root", Type: "file", Digest: digest, Size: 4}},
		},
		Root: haulkit.RootIdentity{Reference: "hauler/root.tar.zst:latest", SHA256: rootSHA256, Size: 4},
		Tools: haulkit.ToolIdentities{
			Camp:   haulkit.FileIdentity{Name: "camp", Version: "dev", SHA256: digest, Size: 4},
			Hauler: haulkit.FileIdentity{Name: "hauler", Version: "v2.0.2", SHA256: digest, Size: 4},
			Pasta:  haulkit.FileIdentity{Name: "pasta", Version: "pasta 1", SHA256: digest, Size: 4},
		},
		Archive: haulkit.ArchiveIdentity{SHA256: digest, Size: 8},
		Chunks:  []haulkit.ChunkIdentity{{Index: 0, Name: "kit.tar.zst.part-000000", SHA256: digest, Size: 8}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
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
