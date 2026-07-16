package ports

import (
	"reflect"
	"testing"
)

func TestCommandRedactedViewPreservesArgvBoundariesAndSecrets(t *testing.T) {
	command := Command{
		Executable: "/usr/bin/tool",
		Argv:       []string{"login", "--token", "secret; rm -rf /", "literal value"},
		Directory:  "/tmp/work",
		Environment: map[string]string{
			"AWS_REGION":            "us-east-1",
			"AWS_SECRET_ACCESS_KEY": "secret-value",
		},
		Redaction: Redaction{
			ArgvIndices:     []int{2},
			EnvironmentKeys: []string{"AWS_SECRET_ACCESS_KEY"},
		},
	}

	got := command.RedactedView()
	want := CommandView{
		Executable: "/usr/bin/tool",
		Argv:       []string{"login", "--token", "[REDACTED]", "literal value"},
		Directory:  "/tmp/work",
		Environment: map[string]string{
			"AWS_REGION":            "us-east-1",
			"AWS_SECRET_ACCESS_KEY": "[REDACTED]",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redacted view mismatch\n got: %#v\nwant: %#v", got, want)
	}
	if len(command.Argv) != 4 || command.Argv[2] != "secret; rm -rf /" {
		t.Fatalf("command argv was mutated: %#v", command.Argv)
	}
}
