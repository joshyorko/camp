package domain

import (
	"strings"
	"testing"
)

func TestExecutionProvenanceDecoderValidatesEveryNestedVersionAndDigest(t *testing.T) {
	t.Parallel()
	valid := `{"schemaVersion":1,"binding":{"schemaVersion":1,"blueprint":{"schemaVersion":1,"digest":"` + strings.Repeat("a", 64) + `"},"profileDigest":"` + strings.Repeat("b", 64) + `"},"candidateSha256":"` + strings.Repeat("c", 64) + `"}`
	if _, err := DecodeExecutionProvenance([]byte(valid)); err != nil {
		t.Fatalf("valid provenance: %v", err)
	}
	for name, body := range map[string]string{
		"unsupported version":  strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		"bad candidate digest": strings.Replace(valid, strings.Repeat("c", 64), "sha256:"+strings.Repeat("c", 64), 1),
		"unknown field":        strings.TrimSuffix(valid, "}") + `,"hostPath":"/home/josh"}`,
	} {
		name, body := name, body
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeExecutionProvenance([]byte(body)); err == nil {
				t.Fatal("decode succeeded, want error")
			}
		})
	}
}
