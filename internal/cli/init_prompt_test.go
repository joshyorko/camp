package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptInitNameUsesDirectoryDefaultAndRejectsEOF(t *testing.T) {
	var output bytes.Buffer
	name, err := promptInitName(strings.NewReader("\n"), &output, "/work/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if name != "alpha" {
		t.Fatalf("name = %q, want alpha", name)
	}
	if !strings.Contains(output.String(), "Camp name [alpha]:") {
		t.Fatalf("prompt = %q", output.String())
	}
	if _, err := promptInitName(strings.NewReader(""), &bytes.Buffer{}, "/work/alpha"); err == nil || !strings.Contains(err.Error(), "camp name") {
		t.Fatalf("EOF error = %v", err)
	}
}
