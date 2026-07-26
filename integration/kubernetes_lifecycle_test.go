//go:build linux

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

const kubernetesScenarioLabel = "evidence.camp.dev/scenario"

type kubernetesEvidenceContract struct {
	CandidateBinary   string
	CandidateCommit   string
	CandidateSHA256   string
	Provider          string
	DevPodContext     string
	DevPodHome        string
	DevPodConfig      string
	DevPodSSHConfig   string
	Kubeconfig        string
	KubernetesContext string
	NamespacePrefix   string
	EvidenceOutputDir string
	OCICapability     string
}

type kubernetesEvidenceGateError struct {
	Code   string   `json:"code"`
	Fields []string `json:"fields,omitempty"`
	Detail string   `json:"detail"`
}

func (e *kubernetesEvidenceGateError) Error() string {
	if len(e.Fields) == 0 {
		return e.Code + ": " + e.Detail
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Detail, strings.Join(e.Fields, ", "))
}

func loadKubernetesEvidenceContract(lookup func(string) string) (kubernetesEvidenceContract, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	required := []string{
		"CAMP_TEST_BINARY",
		"CAMP_KUBERNETES_EVIDENCE",
		"CAMP_KUBERNETES_PROVIDER",
		"CAMP_DEVPOD_CONTEXT",
		"CAMP_DEVPOD_HOME",
		"CAMP_DEVPOD_CONFIG",
		"CAMP_DEVPOD_SSH_CONFIG",
		"CAMP_KUBECONFIG",
		"CAMP_KUBERNETES_CONTEXT",
		"CAMP_KUBERNETES_CANDIDATE_COMMIT",
		"CAMP_KUBERNETES_CANDIDATE_SHA256",
		"CAMP_KUBERNETES_OCI_CAPABILITY",
		"CAMP_KUBERNETES_EVIDENCE_OUTPUT_DIR",
	}
	var missing []string
	for _, name := range required {
		if strings.TrimSpace(lookup(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{
			Code: "missing-protected-contract", Fields: missing,
			Detail: "protected Kubernetes evidence is gated, not skipped or passed",
		}
	}
	contract := kubernetesEvidenceContract{
		CandidateBinary:   strings.TrimSpace(lookup("CAMP_TEST_BINARY")),
		CandidateCommit:   strings.TrimSpace(lookup("CAMP_KUBERNETES_CANDIDATE_COMMIT")),
		CandidateSHA256:   strings.TrimSpace(lookup("CAMP_KUBERNETES_CANDIDATE_SHA256")),
		Provider:          strings.TrimSpace(lookup("CAMP_KUBERNETES_PROVIDER")),
		DevPodContext:     strings.TrimSpace(lookup("CAMP_DEVPOD_CONTEXT")),
		DevPodHome:        strings.TrimSpace(lookup("CAMP_DEVPOD_HOME")),
		DevPodConfig:      strings.TrimSpace(lookup("CAMP_DEVPOD_CONFIG")),
		DevPodSSHConfig:   strings.TrimSpace(lookup("CAMP_DEVPOD_SSH_CONFIG")),
		Kubeconfig:        strings.TrimSpace(lookup("CAMP_KUBECONFIG")),
		KubernetesContext: strings.TrimSpace(lookup("CAMP_KUBERNETES_CONTEXT")),
		NamespacePrefix:   strings.TrimSpace(lookup("CAMP_KUBERNETES_NAMESPACE_PREFIX")),
		EvidenceOutputDir: strings.TrimSpace(lookup("CAMP_KUBERNETES_EVIDENCE_OUTPUT_DIR")),
		OCICapability:     strings.TrimSpace(lookup("CAMP_KUBERNETES_OCI_CAPABILITY")),
	}
	if lookup("CAMP_KUBERNETES_EVIDENCE") != "1" {
		return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{Code: "unauthorized-protected-contract", Detail: "CAMP_KUBERNETES_EVIDENCE must equal 1"}
	}
	if contract.DevPodContext == "default" || contract.KubernetesContext == "default" {
		return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{Code: "unauthorized-protected-contract", Detail: "ambient/default contexts are forbidden"}
	}
	if contract.NamespacePrefix == "" {
		contract.NamespacePrefix = "camp-evidence"
	}
	if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`).MatchString(contract.NamespacePrefix) {
		return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{Code: "invalid-protected-contract", Fields: []string{"CAMP_KUBERNETES_NAMESPACE_PREFIX"}, Detail: "namespace prefix must be a bounded DNS label"}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(contract.CandidateCommit) ||
		!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(contract.CandidateSHA256) {
		return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{Code: "invalid-protected-contract", Detail: "candidate commit and SHA-256 must be exact lowercase hex"}
	}
	if contract.OCICapability != "required" && contract.OCICapability != "unsupported" {
		return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{Code: "invalid-protected-contract", Fields: []string{"CAMP_KUBERNETES_OCI_CAPABILITY"}, Detail: "must be required or unsupported"}
	}
	for name, path := range map[string]string{"CAMP_TEST_BINARY": contract.CandidateBinary, "CAMP_KUBECONFIG": contract.Kubeconfig} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{Code: "invalid-protected-contract", Fields: []string{name}, Detail: "must name a regular file"}
		}
	}
	digest, err := fileSHA256(contract.CandidateBinary)
	if err != nil {
		return kubernetesEvidenceContract{}, err
	}
	if digest != contract.CandidateSHA256 {
		return kubernetesEvidenceContract{}, &kubernetesEvidenceGateError{Code: "candidate-identity-mismatch", Detail: fmt.Sprintf("candidate SHA-256 %s does not match declared %s", digest, contract.CandidateSHA256)}
	}
	return contract, nil
}

func TestKubernetesLifecycleVertical(t *testing.T) {
	contract, err := loadKubernetesEvidenceContract(os.Getenv)
	if err != nil {
		writeKubernetesGateStatus(t, os.Getenv("CAMP_KUBERNETES_EVIDENCE_OUTPUT_DIR"), "gated", nil, err)
		t.Fatalf("protected Kubernetes evidence unavailable: %v", err)
	}
	for _, name := range []string{"devpod", "kubectl", "hauler"} {
		if _, err := exec.LookPath(name); err != nil {
			gated := &kubernetesEvidenceGateError{Code: "missing-cluster-capability", Fields: []string{name}, Detail: "required executable unavailable"}
			writeKubernetesGateStatus(t, contract.EvidenceOutputDir, "gated", &contract, gated)
			t.Fatalf("%v", gated)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	t.Cleanup(cancel)
	scenarioID := fmt.Sprintf("%s-%d", contract.CandidateCommit[:12], time.Now().UTC().UnixNano())
	namespace := fmt.Sprintf("%s-%s", contract.NamespacePrefix, contract.CandidateCommit[:8])
	if len(namespace) > 52 {
		namespace = namespace[:52]
	}
	namespace += fmt.Sprintf("-%x", sha256.Sum256([]byte(scenarioID)))[:9]
	ledger := newKubernetesResourceLedger(contract, scenarioID, namespace)
	t.Cleanup(func() {
		cleanupErr := ledger.Cleanup(context.WithoutCancel(ctx))
		ledger.WriteCleanupReceipt(t, cleanupErr)
		if cleanupErr != nil {
			t.Errorf("exact Kubernetes cleanup: %v", cleanupErr)
		}
	})
	if err := ledger.CreateNamespace(ctx); err != nil {
		writeKubernetesGateStatus(t, contract.EvidenceOutputDir, "gated", &contract, err)
		t.Fatalf("create authorized evidence namespace: %v", err)
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	backend := filepath.Join(root, "backend")
	controllerA := filepath.Join(root, "controller-a")
	controllerB := filepath.Join(root, "controller-b")
	writeKubernetesFixture(t, source)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	envA := kubernetesLifecycleEnvironment(contract, controllerA, backend, namespace)
	envB := kubernetesLifecycleEnvironment(contract, controllerB, backend, namespace)
	bin := contract.CandidateBinary
	campName := "kubernetes-" + contract.CandidateCommit[:12]

	mustRunLifecycle(t, ctx, envA, bin, "--json", "init", source, "--name", campName, "--devpod-provider", contract.Provider, "--devpod-context", contract.DevPodContext)
	opened := decodeOpenResult(t, mustRunLifecycleAt(t, ctx, envA, source, bin, "--json", "open", "--camp", campName))
	if opened.WorkspaceID == "" || opened.SessionID == "" {
		t.Fatalf("Kubernetes open result = %#v", opened)
	}
	ledger.TrackWorkspace(opened.WorkspaceID)
	workspaceRoot := "/workspaces/" + opened.WorkspaceID
	mustRunProtectedDevPod(t, ctx, contract, "ssh", opened.WorkspaceID, "--command",
		fmt.Sprintf("set -eu; printf 'kubernetes-sync\\n' >> %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "proof.txt")))))
	syncReceipt := decodeCheckpointReceipt(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "sync", "--camp", campName))
	if !syncReceipt.Published || syncReceipt.Generation.Generation != 1 {
		t.Fatalf("Kubernetes sync receipt = %#v", syncReceipt)
	}
	closeReceipt := decodeCloseReceipt(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "close", "--camp", campName))
	if !closeReceipt.PublicationSucceeded || !closeReceipt.CleanupSucceeded || closeReceipt.Generation.Generation != 2 {
		t.Fatalf("Kubernetes close receipt = %#v", closeReceipt)
	}

	reopened := decodeOpenResult(t, mustRunLifecycleAt(t, ctx, envB, source, bin, "--json", "reopen", "--camp", campName))
	ledger.TrackWorkspace(reopened.WorkspaceID)
	if reopened.Generation != 2 || reopened.WorkspaceID == "" || reopened.SessionID == opened.SessionID {
		t.Fatalf("fresh-controller Kubernetes reopen = %#v; original session %q", reopened, opened.SessionID)
	}
	reopenedRoot := "/workspaces/" + reopened.WorkspaceID
	mustRunProtectedDevPod(t, ctx, contract, "ssh", reopened.WorkspaceID, "--command",
		fmt.Sprintf("set -eu; grep -qx kubernetes-before %s; grep -qx kubernetes-sync %s",
			shellQuote(filepath.ToSlash(filepath.Join(reopenedRoot, "README.md"))),
			shellQuote(filepath.ToSlash(filepath.Join(reopenedRoot, "proof.txt")))))
	if contract.OCICapability == "required" {
		proveKubernetesDigestPinnedFixture(t, ctx, contract, reopened.WorkspaceID, reopenedRoot)
	}
	finalClose := decodeCloseReceipt(t, mustRunLifecycle(t, ctx, envB, bin, "--json", "close", "--camp", campName))
	if !finalClose.PublicationSucceeded || !finalClose.CleanupSucceeded || finalClose.Generation.Generation != 3 {
		t.Fatalf("fresh-controller Kubernetes close receipt = %#v", finalClose)
	}
	if err := ledger.CaptureOwnedResources(ctx); err != nil {
		t.Fatal(err)
	}
	writeKubernetesGateStatus(t, contract.EvidenceOutputDir, "passed", &contract, nil)
}

func writeKubernetesFixture(t *testing.T, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(source, "image-fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]struct {
		body string
		mode os.FileMode
	}{
		"README.md":                {"kubernetes-before\n", 0o644},
		"proof.txt":                {"before-sync\n", 0o600},
		"image-fixture/Dockerfile": {"FROM alpine:3.20\nCOPY run.sh /run.sh\nENTRYPOINT [\"/run.sh\"]\n", 0o644},
		"image-fixture/run.sh":     {"#!/bin/sh\nprintf 'camp-kubernetes-oci-ok\\n'\n", 0o755},
	} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(value.body), value.mode); err != nil {
			t.Fatal(err)
		}
	}
}

func kubernetesLifecycleEnvironment(contract kubernetesEvidenceContract, controller, backend, namespace string) []string {
	return []string{
		"XDG_CONFIG_HOME=" + filepath.Join(controller, "config"),
		"XDG_DATA_HOME=" + filepath.Join(controller, "data"),
		"XDG_STATE_HOME=" + filepath.Join(controller, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(controller, "cache"),
		"XDG_RUNTIME_DIR=" + filepath.Join(controller, "runtime"),
		"CAMP_BACKEND=file://" + backend,
		"CAMP_DEVPOD_PROVIDER=" + contract.Provider,
		"CAMP_DEVPOD_CONTEXT=" + contract.DevPodContext,
		"DEVPOD_HOME=" + contract.DevPodHome,
		"DEVPOD_CONFIG=" + contract.DevPodConfig,
		"SSH_CONFIG_PATH=" + contract.DevPodSSHConfig,
		"KUBECONFIG=" + contract.Kubeconfig,
		"CAMP_KUBERNETES_NAMESPACE=" + namespace,
	}
}

func mustRunProtectedDevPod(t *testing.T, ctx context.Context, contract kubernetesEvidenceContract, argv ...string) []byte {
	t.Helper()
	args := append([]string{argv[0], "--context", contract.DevPodContext}, argv[1:]...)
	output, err := runLifecycleCommand(ctx, []string{
		"DEVPOD_HOME=" + contract.DevPodHome,
		"DEVPOD_CONFIG=" + contract.DevPodConfig,
		"SSH_CONFIG_PATH=" + contract.DevPodSSHConfig,
		"KUBECONFIG=" + contract.Kubeconfig,
	}, "devpod", args...)
	if err != nil {
		t.Fatalf("protected devpod %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func proveKubernetesDigestPinnedFixture(t *testing.T, ctx context.Context, contract kubernetesEvidenceContract, workspaceID, workspaceRoot string) {
	t.Helper()
	command := fmt.Sprintf("set -eu; engine=; for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; test -n \"$engine\"; ref=camp-kubernetes-fixture:local; \"$engine\" build -t \"$ref\" %s; digest=$(\"$engine\" image inspect --format '{{index .RepoDigests 0}}' \"$ref\" 2>/dev/null || true); test -n \"$digest\"; case \"$digest\" in *@sha256:????????????????????????????????????????????????????????????????) ;; *) exit 31;; esac; \"$engine\" run --rm \"$digest\" | grep -qx camp-kubernetes-oci-ok",
		shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "image-fixture"))))
	mustRunProtectedDevPod(t, ctx, contract, "ssh", workspaceID, "--command", command)
}

type kubernetesResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type kubernetesResourceLedger struct {
	contract   kubernetesEvidenceContract
	scenarioID string
	namespace  string
	resources  []kubernetesResourceRef
	workspaces []string
}

func newKubernetesResourceLedger(contract kubernetesEvidenceContract, scenarioID, namespace string) *kubernetesResourceLedger {
	return &kubernetesResourceLedger{contract: contract, scenarioID: scenarioID, namespace: namespace}
}

func (l *kubernetesResourceLedger) kubectl(ctx context.Context, argv ...string) ([]byte, error) {
	args := append([]string{"--kubeconfig", l.contract.Kubeconfig, "--context", l.contract.KubernetesContext}, argv...)
	command := exec.CommandContext(ctx, "kubectl", args...)
	command.Env = mergeCommandEnvironment(os.Environ(), []string{"KUBECONFIG=" + l.contract.Kubeconfig})
	return command.CombinedOutput()
}

func (l *kubernetesResourceLedger) CreateNamespace(ctx context.Context) error {
	output, err := l.kubectl(ctx, "create", "namespace", l.namespace)
	if err != nil {
		return fmt.Errorf("kubectl create namespace: %w: %s", err, output)
	}
	output, err = l.kubectl(ctx, "label", "namespace", l.namespace,
		kubernetesScenarioLabel+"="+l.scenarioID,
		"evidence.camp.dev/candidate-commit="+l.contract.CandidateCommit,
		"--overwrite")
	if err != nil {
		return fmt.Errorf("label exact namespace: %w: %s", err, output)
	}
	return l.captureNamespace(ctx)
}

func (l *kubernetesResourceLedger) captureNamespace(ctx context.Context) error {
	output, err := l.kubectl(ctx, "get", "namespace", l.namespace, "-o", "json")
	if err != nil {
		return fmt.Errorf("get exact namespace: %w: %s", err, output)
	}
	var item struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(output, &item); err != nil {
		return fmt.Errorf("decode namespace identity: %w", err)
	}
	l.resources = append(l.resources, kubernetesResourceRef{Kind: item.Kind, Name: item.Metadata.Name, UID: item.Metadata.UID})
	return nil
}

func (l *kubernetesResourceLedger) TrackWorkspace(id string) {
	if id != "" {
		l.workspaces = append(l.workspaces, id)
	}
}

func (l *kubernetesResourceLedger) CaptureOwnedResources(ctx context.Context) error {
	output, err := l.kubectl(ctx, "get", "all,configmaps,secrets,persistentvolumeclaims", "-n", l.namespace, "-o", "json")
	if err != nil {
		return fmt.Errorf("list resources only inside exact owned namespace: %w: %s", err, output)
	}
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				UID       string `json:"uid"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(output, &list); err != nil {
		return fmt.Errorf("decode exact resource ledger: %w", err)
	}
	for _, item := range list.Items {
		ref := kubernetesResourceRef{Kind: item.Kind, Namespace: item.Metadata.Namespace, Name: item.Metadata.Name, UID: item.Metadata.UID}
		if ref.Namespace != l.namespace || ref.Name == "" || ref.UID == "" {
			return fmt.Errorf("resource outside exact ledger boundary: %#v", ref)
		}
		l.resources = append(l.resources, ref)
	}
	sort.Slice(l.resources, func(i, j int) bool {
		return l.resources[i].Kind+"/"+l.resources[i].Name < l.resources[j].Kind+"/"+l.resources[j].Name
	})
	return nil
}

func (l *kubernetesResourceLedger) Cleanup(ctx context.Context) error {
	var result error
	for _, workspaceID := range l.workspaces {
		output, err := runLifecycleCommand(ctx, []string{
			"DEVPOD_HOME=" + l.contract.DevPodHome,
			"DEVPOD_CONFIG=" + l.contract.DevPodConfig,
			"SSH_CONFIG_PATH=" + l.contract.DevPodSSHConfig,
			"KUBECONFIG=" + l.contract.Kubeconfig,
		}, "devpod", "delete", "--context", l.contract.DevPodContext, workspaceID, "--force")
		if err != nil && !strings.Contains(string(output), "not found") {
			result = errors.Join(result, fmt.Errorf("delete exact workspace %s: %w: %s", workspaceID, err, output))
		}
	}
	if l.namespace == "" {
		return errors.Join(result, errors.New("cleanup refused empty namespace"))
	}
	output, err := l.kubectl(ctx, "delete", "namespace", l.namespace, "--wait=true", "--timeout=10m")
	if err != nil && !strings.Contains(string(output), "NotFound") && !strings.Contains(string(output), "not found") {
		result = errors.Join(result, fmt.Errorf("delete exact namespace %s: %w: %s", l.namespace, err, output))
	}
	output, err = l.kubectl(ctx, "get", "namespace", l.namespace, "-o", "name")
	if err == nil {
		result = errors.Join(result, fmt.Errorf("owned namespace still exists after cleanup: %s", strings.TrimSpace(string(output))))
	} else if !strings.Contains(string(output), "NotFound") && !strings.Contains(string(output), "not found") {
		result = errors.Join(result, fmt.Errorf("cannot prove namespace absence: %w: %s", err, output))
	}
	return result
}

func (l *kubernetesResourceLedger) WriteCleanupReceipt(t *testing.T, cleanupErr error) {
	t.Helper()
	status := "passed"
	detail := ""
	if cleanupErr != nil {
		status, detail = "failed", cleanupErr.Error()
	}
	writeAllowlistedJSON(t, l.contract.EvidenceOutputDir, "cleanup-receipt.json", map[string]any{
		"schemaVersion":   1,
		"result":          status,
		"detail":          detail,
		"candidateCommit": l.contract.CandidateCommit,
		"candidateSha256": l.contract.CandidateSHA256,
		"scenarioId":      l.scenarioID,
		"namespace":       l.namespace,
		"resources":       l.resources,
		"workspaceIds":    l.workspaces,
	})
}

func writeKubernetesGateStatus(t *testing.T, outputDir, result string, contract *kubernetesEvidenceContract, evidenceErr error) {
	t.Helper()
	if outputDir == "" {
		return
	}
	record := map[string]any{"schemaVersion": 1, "result": result}
	if contract != nil {
		record["candidateCommit"] = contract.CandidateCommit
		record["candidateSha256"] = contract.CandidateSHA256
		record["provider"] = contract.Provider
		record["devpodContext"] = contract.DevPodContext
		record["kubernetesContext"] = contract.KubernetesContext
		record["ociCapability"] = contract.OCICapability
	}
	if evidenceErr != nil {
		record["detail"] = evidenceErr.Error()
		var gated *kubernetesEvidenceGateError
		if errors.As(evidenceErr, &gated) {
			record["gate"] = gated
		}
	}
	writeAllowlistedJSON(t, outputDir, "gate-status.json", record)
}

func writeAllowlistedJSON(t *testing.T, outputDir, name string, value any) {
	t.Helper()
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Errorf("create Kubernetes evidence directory: %v", err)
		return
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Errorf("encode %s: %v", name, err)
		return
	}
	body = append(body, '\n')
	if err := os.WriteFile(filepath.Join(outputDir, name), body, 0o600); err != nil {
		t.Errorf("write %s: %v", name, err)
	}
}

func fileSHA256(path string) (string, error) {
	stream, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, stream); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
