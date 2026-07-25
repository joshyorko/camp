package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
)

func TestMountedFileBackendParity(t *testing.T) {
	root := t.TempDir()
	backendRoot := filepath.Join(root, "backend")
	store, err := filebackend.New(backendRoot)
	if err != nil {
		t.Fatal(err)
	}
	generations := coordination.NewGenerationRepository(store)
	pointers := coordination.NewPointerRepository(store)
	lineage := domain.Lineage{Branch: "main"}
	initialBody := []byte("initial verified generation")
	initial := generationMetadata(t, "controller-initial", lineage, 1, nil, initialBody)
	if _, err := generations.PutAndVerify(context.Background(), initial, bytesSource(initialBody)); err != nil {
		t.Fatal(err)
	}
	if _, err := pointers.Create(context.Background(), pointerFor(initial)); err != nil {
		t.Fatal(err)
	}

	start := filepath.Join(root, "start")
	processes := make([]controllerProcess, 0, 2)
	for _, session := range []string{"controller-a", "controller-b"} {
		controllerRoot := filepath.Join(root, session)
		if err := os.MkdirAll(controllerRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		resultPath := filepath.Join(controllerRoot, "result.json")
		readyPath := filepath.Join(controllerRoot, "ready")
		processes = append(processes, startFileController(t, []string{
			"CAMP_FILE_PARITY_HELPER=writer",
			"CAMP_FILE_PARITY_ROOT=" + backendRoot,
			"CAMP_CONTROLLER_SESSION=" + session,
			"CAMP_CONTROLLER_RESULT=" + resultPath,
			"CAMP_CONTROLLER_READY=" + readyPath,
			"CAMP_CONTROLLER_START=" + start,
		}, resultPath, readyPath))
	}
	waitForFiles(t, processes[0].ready, processes[1].ready)
	if err := os.WriteFile(start, []byte("race\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := make([]controllerResult, 0, 2)
	for _, process := range processes {
		if err := process.cmd.Wait(); err != nil {
			t.Fatalf("file controller process: %v: %s", err, process.output.Bytes())
		}
		results = append(results, readControllerResult(t, process.result))
	}
	winners, losers := splitResults(results)
	if len(winners) != 1 || len(losers) != 1 {
		t.Fatalf("file writer results = %#v, want one winner and one typed CAS loser", results)
	}
	if _, _, err := generations.ReadMetadata(context.Background(), "brain", lineage, losers[0].Generation); err != nil {
		t.Fatalf("file backend did not retain losing generation: %v", err)
	}

	freshStore, err := filebackend.New(backendRoot)
	if err != nil {
		t.Fatal(err)
	}
	freshPointer, err := coordination.NewPointerRepository(freshStore).Read(context.Background(), "brain", lineage)
	if err != nil {
		t.Fatal(err)
	}
	if freshPointer.Pointer.Generation != winners[0].Generation {
		t.Fatalf("fresh file controller pointer = %#v, winner = %#v", freshPointer.Pointer.Generation, winners[0].Generation)
	}
}

func startFileController(t *testing.T, environment []string, result, ready string) controllerProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMountedFileBackendControllerHelperProcess$")
	output := new(bytes.Buffer)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append(os.Environ(), environment...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return controllerProcess{cmd: cmd, result: result, ready: ready, output: output}
}

func TestMountedFileBackendControllerHelperProcess(t *testing.T) {
	if os.Getenv("CAMP_FILE_PARITY_HELPER") != "writer" {
		return
	}
	store, err := filebackend.New(os.Getenv("CAMP_FILE_PARITY_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	lineage := domain.Lineage{Branch: "main"}
	pointers := coordination.NewPointerRepository(store)
	generations := coordination.NewGenerationRepository(store)
	baseline, err := pointers.Read(context.Background(), "brain", lineage)
	if err != nil {
		t.Fatal(err)
	}
	session := os.Getenv("CAMP_CONTROLLER_SESSION")
	body := writerBody(session)
	metadata := generationMetadata(t, session, lineage, 2, &baseline.Pointer.Generation, body)
	if _, err := generations.PutAndVerify(context.Background(), metadata, bytesSource(body)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CAMP_CONTROLLER_READY"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFiles(t, os.Getenv("CAMP_CONTROLLER_START"))
	_, err = pointers.CompareAndSwap(context.Background(), baseline, pointerFor(metadata))
	result := controllerResult{
		Session: session, Generation: metadata.Generation,
		Published: err == nil, PointerChanged: errors.Is(err, coordination.ErrPointerChanged),
	}
	if err != nil && !result.PointerChanged {
		t.Fatal(err)
	}
	bodyJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CAMP_CONTROLLER_RESULT"), bodyJSON, 0o600); err != nil {
		t.Fatal(err)
	}
}
