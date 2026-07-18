package sshtransfer

import (
	"strings"
	"testing"
)

func TestCommandEnvironmentReplacesInheritedKeys(t *testing.T) {
	t.Setenv("CAMP_TRANSFER_ENV_TEST", "inherited")
	environment := commandEnvironment(map[string]string{"CAMP_TRANSFER_ENV_TEST": "override", "CAMP_TRANSFER_ENV_NEW": "new"})

	counts := map[string]int{}
	values := map[string]string{}
	for _, entry := range environment {
		for _, key := range []string{"CAMP_TRANSFER_ENV_TEST", "CAMP_TRANSFER_ENV_NEW"} {
			if strings.HasPrefix(entry, key+"=") {
				counts[key]++
				values[key] = strings.TrimPrefix(entry, key+"=")
			}
		}
	}
	if counts["CAMP_TRANSFER_ENV_TEST"] != 1 || values["CAMP_TRANSFER_ENV_TEST"] != "override" {
		t.Fatalf("override entries=%d value=%q", counts["CAMP_TRANSFER_ENV_TEST"], values["CAMP_TRANSFER_ENV_TEST"])
	}
	if counts["CAMP_TRANSFER_ENV_NEW"] != 1 || values["CAMP_TRANSFER_ENV_NEW"] != "new" {
		t.Fatalf("new entries=%d value=%q", counts["CAMP_TRANSFER_ENV_NEW"], values["CAMP_TRANSFER_ENV_NEW"])
	}
}
