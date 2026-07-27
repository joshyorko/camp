package remoteworker

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	ProtocolSchemaVersion uint32 = 1
	DiagnosticLimit              = 64 << 10
	maxRequestBytes              = 1 << 20
	maxDiagnosticBytes           = 60 << 10
)

var (
	ErrInvalidRequest        = errors.New("invalid remote-worker request")
	ErrUnsupportedOperation  = errors.New("remote-worker operation is not implemented")
	ErrUnsupportedCapability = errors.New("remote-worker capability is unsupported")
	ErrIdentityMismatch      = errors.New("remote-worker identity mismatch")
)

type Operation string

const (
	OperationProbe         Operation = "probe"
	OperationActivateImage Operation = "activateImage"
	OperationHydrate       Operation = "hydrate"
	OperationStartServices Operation = "startServices"
	OperationObserve       Operation = "observe"
	OperationCheckpoint    Operation = "checkpoint"
	OperationStopServices  Operation = "stopServices"
	OperationCleanup       Operation = "cleanup"
)

type FileIdentity struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type ExpectedIdentity struct {
	Architecture string       `json:"architecture"`
	Helper       FileIdentity `json:"helper"`
	Kit          FileIdentity `json:"kit"`
	Manifest     FileIdentity `json:"manifest"`
	Image        string       `json:"image"`
}

type Request struct {
	SchemaVersion uint32           `json:"schemaVersion"`
	Operation     Operation        `json:"operation"`
	SessionID     string           `json:"sessionId"`
	WorkspaceRoot string           `json:"workspaceRoot"`
	RuntimeRoot   string           `json:"runtimeRoot"`
	ManifestPath  string           `json:"manifestPath"`
	Expected      ExpectedIdentity `json:"expected"`
}

type Result struct {
	SchemaVersion uint32          `json:"schemaVersion"`
	Operation     Operation       `json:"operation"`
	Receipt       json.RawMessage `json:"receipt"`
}

type UnsupportedReceipt struct {
	Status     string `json:"status"`
	Diagnostic string `json:"diagnostic"`
}

func DecodeRequest(reader io.Reader) (Request, error) {
	var request Request
	decoder := json.NewDecoder(io.LimitReader(reader, maxRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, invalidRequest("decode: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request, invalidRequest("trailing JSON")
	}
	if err := validateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func validateRequest(request Request) error {
	if request.SchemaVersion != ProtocolSchemaVersion {
		return invalidRequest("unsupported schema")
	}
	switch request.Operation {
	case OperationProbe, OperationActivateImage, OperationHydrate, OperationStartServices,
		OperationObserve, OperationCheckpoint, OperationStopServices, OperationCleanup:
	default:
		return invalidRequest("unsupported operation")
	}
	if !safeSegment(request.SessionID) {
		return invalidRequest("unsafe session ID")
	}
	for name, path := range map[string]string{
		"workspace root": request.WorkspaceRoot,
		"runtime root":   request.RuntimeRoot,
		"manifest path":  request.ManifestPath,
	} {
		if !safeAbsolutePath(path) {
			return invalidRequest("unsafe %s", name)
		}
	}
	if request.Expected.Architecture != "linux/amd64" && request.Expected.Architecture != "linux/arm64" {
		return invalidRequest("unsupported architecture")
	}
	if !validIdentity(request.Expected.Helper) || request.Expected.Helper.Name != "camp" ||
		!validIdentity(request.Expected.Kit) || request.Expected.Kit.Name != "camp-hauler-kit.tar.zst" ||
		!validIdentity(request.Expected.Manifest) {
		return invalidRequest("invalid helper or kit identity")
	}
	const digestMarker = "@sha256:"
	index := strings.LastIndex(request.Expected.Image, digestMarker)
	if index <= 0 || !validDigest(request.Expected.Image[index+len(digestMarker):]) {
		return invalidRequest("image is not immutable")
	}
	return nil
}

func validIdentity(identity FileIdentity) bool {
	return safeSegment(identity.Name) && identity.Size > 0 && validDigest(identity.SHA256)
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safeSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, `/\`+"\x00") &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func safeAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		strings.IndexFunc(path, unicode.IsControl) < 0
}

func invalidRequest(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, arguments...))
}

func boundedDiagnostic(err error) string {
	value := err.Error()
	if len(value) > maxDiagnosticBytes {
		value = value[:maxDiagnosticBytes]
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

func encodeResult(writer io.Writer, operation Operation, receipt any) error {
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	result, err := json.Marshal(Result{
		SchemaVersion: ProtocolSchemaVersion,
		Operation:     operation,
		Receipt:       body,
	})
	if err != nil {
		return err
	}
	result = append(result, '\n')
	if len(result) > DiagnosticLimit {
		return errors.New("remote-worker result exceeds diagnostic limit")
	}
	_, err = io.Copy(writer, bytes.NewReader(result))
	return err
}
