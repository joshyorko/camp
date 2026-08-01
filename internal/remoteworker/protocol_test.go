package remoteworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func validRequest() Request {
	return Request{
		SchemaVersion: ProtocolSchemaVersion,
		Operation:     OperationProbe,
		SessionID:     "session-1",
		WorkspaceRoot: "/workspaces/capsule",
		RuntimeRoot:   "/var/lib/camp/session-1",
		ManifestPath:  "/workspaces/.camp-bootstrap/manifest.json",
		Expected: ExpectedIdentity{
			Architecture: "linux/amd64",
			Helper:       FileIdentity{Name: "camp", SHA256: strings.Repeat("a", 64), Size: 123},
			Kit:          FileIdentity{Name: "camp-hauler-kit.tar.zst", SHA256: strings.Repeat("b", 64), Size: 456},
			Manifest:     FileIdentity{Name: "manifest.json", SHA256: strings.Repeat("d", 64), Size: 789},
			SourceImage:  "registry.example/camp@sha256:" + strings.Repeat("c", 64),
			Image:        "sha256:" + strings.Repeat("e", 64),
		},
	}
}

func TestDecodeRequestAcceptsOneStrictVersionedDocument(t *testing.T) {
	body, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequest(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != OperationProbe || got.Expected.Architecture != "linux/amd64" {
		t.Fatalf("DecodeRequest() = %#v", got)
	}

	for name, mutate := range map[string]func([]byte) []byte{
		"unknown field": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"operation"`), []byte(`"extra":true,"operation"`), 1)
		},
		"trailing document": func(body []byte) []byte { return append(body, body...) },
		"wrong schema": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":2`), 1)
		},
		"relative path": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"/workspaces/capsule"`), []byte(`"../capsule"`), 1)
		},
		"unknown operation": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"probe"`), []byte(`"invented"`), 1)
		},
		"recursive duplicate": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"architecture"`), []byte(`"architecture":"linux/amd64","architecture"`), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRequest(bytes.NewReader(mutate(append([]byte(nil), body...)))); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("DecodeRequest() error = %v", err)
			}
		})
	}
}

func TestRunEmitsBoundedTypedResultForMalformedRequest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), strings.NewReader("{"), &stdout, &stderr)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 || stdout.Len() == 0 || stdout.Len() > DiagnosticLimit {
		t.Fatalf("stdout bytes=%d stderr=%q", stdout.Len(), stderr.String())
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var receipt ErrorReceipt
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if result.Operation != OperationRejected || receipt.Status != "error" ||
		receipt.Code != "invalid_request" || receipt.Diagnostic == "" {
		t.Fatalf("result=%#v receipt=%#v", result, receipt)
	}
}

func TestBoundedDiagnosticNormalizesBeforeApplyingByteCap(t *testing.T) {
	diagnostic := boundedDiagnostic(errors.New(string(bytes.Repeat([]byte{0xff}, DiagnosticLimit))))
	if !utf8.ValidString(diagnostic) || len(diagnostic) > maxDiagnosticBytes {
		t.Fatalf("diagnostic bytes=%d valid=%v", len(diagnostic), utf8.ValidString(diagnostic))
	}
}

func TestBoundedStderrDiagnosticRedactsControlsSecretsAndExcess(t *testing.T) {
	secret := "provider-token-should-not-escape"
	diagnostic := boundedStderrDiagnostic([]byte(strings.Repeat("x", maxDiagnosticBytes+1) + "\nTOKEN=" + secret + "\n\x1b[31mfailed\x1b[0m"))
	if len(diagnostic) > maxDiagnosticBytes || strings.Contains(diagnostic, secret) || strings.Contains(diagnostic, "\x1b") || !strings.Contains(diagnostic, "[redacted]") || !strings.Contains(diagnostic, `\u001b`) {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
}

func TestRunDispatchesProductionCheckpointAndEmitsOneBoundedError(t *testing.T) {
	request := validRequest()
	request.Operation = OperationCheckpoint
	request.Checkpoint = checkpointRequest(false).Checkpoint
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = Run(t.Context(), bytes.NewReader(body), &stdout, &stderr)
	if err == nil || errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if stdout.Len() > DiagnosticLimit {
		t.Fatalf("stdout bytes = %d", stdout.Len())
	}
	var result Result
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	var receipt ErrorReceipt
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != ProtocolSchemaVersion || result.Operation != OperationCheckpoint ||
		receipt.Status != "error" || receipt.Diagnostic == "" {
		t.Fatalf("result = %#v, receipt = %#v", result, receipt)
	}
}
