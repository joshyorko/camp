package integration

import (
	"fmt"
	"os"
	"strings"
)

func resolveCandidateBinary(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("CAMP_TEST_BINARY must name the orchestrator-provided Camp candidate")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat CAMP_TEST_BINARY %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("CAMP_TEST_BINARY %q is not executable", path)
	}
	return path, nil
}

func candidateBinary(t interface {
	Helper()
	Fatal(...any)
}) string {
	t.Helper()
	path, err := resolveCandidateBinary(os.Getenv("CAMP_TEST_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func mergeCommandEnvironment(base, overrides []string) []string {
	result := append([]string(nil), base...)
	index := make(map[string]int, len(result))
	for position, entry := range result {
		if name, _, ok := strings.Cut(entry, "="); ok {
			index[name] = position
		}
	}
	for _, entry := range overrides {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if position, exists := index[name]; exists {
			result[position] = entry
			continue
		}
		index[name] = len(result)
		result = append(result, entry)
	}
	return result
}
