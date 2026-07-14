package config

import (
	"strings"
	"testing"
)

func TestRedactRemovesSchemaSecretsAndURLCredentials(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"accessToken": "alpha",
		"nested":      map[string]any{"password": "bravo", "safe": "visible"},
		"backend":     "https://user:pass@example.test/bucket?token=charlie&region=us-east-1",
	}
	body, err := MarshalRedacted(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, secret := range []string{"alpha", "bravo", "user:pass", "charlie", "user:"} {
		if strings.Contains(text, secret) {
			t.Fatalf("redacted output leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "visible") || !strings.Contains(text, "region=us-east-1") || !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("redacted output lost safe fields: %s", text)
	}
}
