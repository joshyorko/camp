package releasepipeline

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenovateManagedDependencyContract(t *testing.T) {
	root := filepath.Clean("..")
	body, err := os.ReadFile(filepath.Join(root, "renovate.json"))
	if err != nil {
		t.Fatalf("read renovate.json: %v", err)
	}
	var config struct {
		CustomManagers []struct {
			DepNameTemplate string   `json:"depNameTemplate"`
			Datasource      string   `json:"datasourceTemplate"`
			CurrentValue    string   `json:"currentValueTemplate"`
			ManagerPatterns []string `json:"managerFilePatterns"`
		} `json:"customManagers"`
		PostUpgradeTasks struct {
			Commands      []string `json:"commands"`
			ExecutionMode string   `json:"executionMode"`
			FileFilters   []string `json:"fileFilters"`
		} `json:"postUpgradeTasks"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatalf("parse renovate.json: %v", err)
	}

	wantReleases := map[string]bool{
		"skevetter/devpod":              false,
		"hauler-dev/hauler":             false,
		"joshyorko/room-of-requirement": false,
		"joshyorko/rcc":                 false,
	}
	foundLifecycle := false
	for _, manager := range config.CustomManagers {
		if _, ok := wantReleases[manager.DepNameTemplate]; ok && manager.Datasource == "github-releases" {
			wantReleases[manager.DepNameTemplate] = true
		}
		if manager.Datasource == "docker" && manager.CurrentValue == "latest" {
			foundLifecycle = true
		}
	}
	for dependency, found := range wantReleases {
		if !found {
			t.Errorf("missing GitHub release manager for %s", dependency)
		}
	}
	if !foundLifecycle {
		t.Error("missing lifecycle Docker digest manager")
	}
	if strings.Join(config.PostUpgradeTasks.Commands, "\n") != "node developer/update_dependency_locks.mjs" {
		t.Fatalf("unsafe or missing post-upgrade command: %q", config.PostUpgradeTasks.Commands)
	}
	if config.PostUpgradeTasks.ExecutionMode != "update" {
		t.Fatalf("post-upgrade execution mode = %q, want update", config.PostUpgradeTasks.ExecutionMode)
	}
	for _, required := range []string{"developer/rcc.lock.yaml", "tools.lock.yaml"} {
		if !containsString(config.PostUpgradeTasks.FileFilters, required) {
			t.Errorf("post-upgrade file filters omit %s", required)
		}
	}
}

func TestRenovateLockUpdater(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed; Renovate supplies Node.js when running the updater")
	}
	command := exec.Command(node, "--test", "developer/update_dependency_locks_test.mjs")
	command.Dir = filepath.Clean("..")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("lock updater tests failed: %v\n%s", err, output)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
