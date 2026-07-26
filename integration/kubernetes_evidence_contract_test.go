package integration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKubernetesEvidenceContractFailsClosedWithTypedMissingFields(t *testing.T) {
	_, err := loadKubernetesEvidenceContract(nil)
	var gated *kubernetesEvidenceGateError
	if !errors.As(err, &gated) {
		t.Fatalf("error = %T %v, want typed Kubernetes evidence gate error", err, err)
	}
	if gated.Code != "missing-protected-contract" {
		t.Fatalf("code = %q", gated.Code)
	}
	for _, field := range []string{
		"CAMP_TEST_BINARY",
		"CAMP_KUBERNETES_PROVIDER",
		"CAMP_DEVPOD_CONTEXT",
		"CAMP_KUBECONFIG",
		"CAMP_KUBERNETES_CONTEXT",
		"CAMP_KUBERNETES_CANDIDATE_COMMIT",
		"CAMP_KUBERNETES_CANDIDATE_SHA256",
		"CAMP_KUBERNETES_OCI_CAPABILITY",
	} {
		if !strings.Contains(gated.Error(), field) {
			t.Errorf("error %q does not name %s", gated.Error(), field)
		}
	}
}

func TestLoadKubernetesEvidenceContractRejectsAmbientDefaultContext(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "camp")
	kubeconfig := filepath.Join(root, "kubeconfig")
	for path, body := range map[string]string{candidate: "candidate", kubeconfig: "kubeconfig"} {
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	values := validKubernetesContractValues(candidate, kubeconfig)
	values["CAMP_DEVPOD_CONTEXT"] = "default"

	_, err := loadKubernetesEvidenceContract(mapLookup(values))
	var gated *kubernetesEvidenceGateError
	if !errors.As(err, &gated) || gated.Code != "unauthorized-protected-contract" {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestLoadKubernetesEvidenceContractBindsExactCandidate(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "camp")
	kubeconfig := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfig, []byte("kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := validKubernetesContractValues(candidate, kubeconfig)

	contract, err := loadKubernetesEvidenceContract(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if contract.CandidateSHA256 != "dda18a0e21ae47c53b4309434cbc02ae8bf764fa83a6defbb719431242722aa7" {
		t.Fatalf("candidate SHA-256 = %q", contract.CandidateSHA256)
	}
	if contract.CandidateCommit != strings.Repeat("a", 40) || contract.OCICapability != "unsupported" {
		t.Fatalf("contract = %#v", contract)
	}
}

func validKubernetesContractValues(candidate, kubeconfig string) map[string]string {
	return map[string]string{
		"CAMP_TEST_BINARY":                    candidate,
		"CAMP_KUBERNETES_EVIDENCE":            "1",
		"CAMP_KUBERNETES_PROVIDER":            "kubernetes-protected",
		"CAMP_DEVPOD_CONTEXT":                 "camp-protected",
		"CAMP_DEVPOD_HOME":                    filepath.Join(filepath.Dir(candidate), "devpod-home"),
		"CAMP_DEVPOD_CONFIG":                  filepath.Join(filepath.Dir(candidate), "devpod-config.yaml"),
		"CAMP_DEVPOD_SSH_CONFIG":              filepath.Join(filepath.Dir(candidate), "devpod-ssh"),
		"CAMP_KUBECONFIG":                     kubeconfig,
		"CAMP_KUBERNETES_CONTEXT":             "authorized-context",
		"CAMP_KUBERNETES_CANDIDATE_COMMIT":    strings.Repeat("a", 40),
		"CAMP_KUBERNETES_CANDIDATE_SHA256":    "dda18a0e21ae47c53b4309434cbc02ae8bf764fa83a6defbb719431242722aa7",
		"CAMP_KUBERNETES_OCI_CAPABILITY":      "unsupported",
		"CAMP_KUBERNETES_EVIDENCE_OUTPUT_DIR": filepath.Join(filepath.Dir(candidate), "evidence"),
		"CAMP_KUBERNETES_NAMESPACE_PREFIX":    "camp-evidence",
	}
}

func mapLookup(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}
