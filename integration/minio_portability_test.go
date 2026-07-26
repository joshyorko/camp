package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/objectstore"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

const portabilityBucket = "camp-portability"

func TestS3TwoWriterConflict(t *testing.T) {
	fixture := startMinIO(t)
	fixture.createBucket(t, portabilityBucket)

	runMinIOTwoWriterScenario(t, fixture)
}

type controllerResult struct {
	Session          string                 `json:"session"`
	Generation       domain.GenerationRef   `json:"generation"`
	Published        bool                   `json:"published"`
	PointerChanged   bool                   `json:"pointerChanged"`
	Pointer          domain.GenerationRef   `json:"pointer"`
	History          []domain.GenerationRef `json:"history,omitempty"`
	DownloadedSHA256 string                 `json:"downloadedSha256,omitempty"`
	DownloadedSize   int64                  `json:"downloadedSize,omitempty"`
}

type controllerProcess struct {
	cmd    *exec.Cmd
	result string
	ready  string
	output *bytes.Buffer
}

func runMinIOTwoWriterScenario(t *testing.T, fixture *minioFixture) {
	t.Helper()
	store := portabilityStore(t, fixture.endpoint, fixture.signer.accessKey, fixture.signer.secretKey)
	generations := coordination.NewGenerationRepository(store)
	pointers := coordination.NewPointerRepository(store)
	lineage := domain.Lineage{Branch: "main"}
	initialBody := []byte("initial verified generation")
	initial := generationMetadata(t, "controller-a", lineage, 1, nil, initialBody)
	if _, err := generations.PutAndVerify(context.Background(), initial, bytesSource(initialBody)); err != nil {
		t.Fatal(err)
	}
	baseline, err := pointers.Create(context.Background(), pointerFor(initial))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	start := filepath.Join(root, "start")
	processes := make([]controllerProcess, 0, 2)
	for _, session := range []string{"controller-a", "controller-b"} {
		controllerRoot := filepath.Join(root, session)
		if err := os.MkdirAll(controllerRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		resultPath := filepath.Join(controllerRoot, "result.json")
		readyPath := filepath.Join(controllerRoot, "ready")
		environment := []string{
			"CAMP_MINIO_HELPER=writer", "CAMP_MINIO_ENDPOINT=" + fixture.endpoint,
			"CAMP_MINIO_ACCESS=" + fixture.signer.accessKey, "CAMP_MINIO_SECRET=" + fixture.signer.secretKey,
			"CAMP_CONTROLLER_SESSION=" + session, "CAMP_CONTROLLER_RESULT=" + resultPath,
			"CAMP_CONTROLLER_READY=" + readyPath, "CAMP_CONTROLLER_START=" + start,
			"XDG_CONFIG_HOME=" + filepath.Join(controllerRoot, "config"),
			"XDG_DATA_HOME=" + filepath.Join(controllerRoot, "data"),
			"XDG_STATE_HOME=" + filepath.Join(controllerRoot, "state"),
			"XDG_CACHE_HOME=" + filepath.Join(controllerRoot, "cache"),
		}
		processes = append(processes, startController(t, environment, resultPath, readyPath))
	}
	waitForFiles(t, processes[0].ready, processes[1].ready)
	if err := os.WriteFile(start, []byte("race\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := make([]controllerResult, 0, 2)
	for _, process := range processes {
		if err := process.cmd.Wait(); err != nil {
			t.Fatalf("controller process: %v: %s", err, process.output.Bytes())
		}
		results = append(results, readControllerResult(t, process.result))
	}
	winners, losers := splitResults(results)
	if len(winners) != 1 || len(losers) != 1 {
		t.Fatalf("writer results = %#v, want one winner and one typed CAS loser", results)
	}
	loser := losers[0]
	if _, _, err := generations.ReadMetadata(context.Background(), "brain", lineage, loser.Generation); err != nil {
		t.Fatalf("losing generation sidecar was not retained: %v", err)
	}
	reader, archive, err := store.Get(context.Background(), mustGenerationKey(t, lineage, loser.Generation))
	if err != nil {
		t.Fatalf("losing generation archive was not retained: %v", err)
	}
	loserBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read losing generation archive: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close losing generation archive: %v", err)
	}
	if archive.Size != int64(len(loserBytes)) || int64(len(loserBytes)) != int64(len(writerBody(loser.Session))) || sha256Hex(loserBytes) != loser.Generation.ArchiveSHA256 {
		t.Fatalf("losing archive integrity: metadata=%#v size=%d sha256=%s", archive, len(loserBytes), sha256Hex(loserBytes))
	}
	branch := domain.Lineage{Branch: "conflict-" + loser.Session}
	branchMetadata := generationMetadata(t, loser.Session, branch, loser.Generation.Generation, &baseline.Pointer.Generation, loserBytes)
	if _, err := generations.PutAndVerify(context.Background(), branchMetadata, bytesSource(loserBytes)); err != nil {
		t.Fatalf("publish retained loser branch metadata: %v", err)
	}
	if _, err := pointers.Create(context.Background(), pointerFor(branchMetadata)); err != nil {
		t.Fatalf("publish retained loser branch: %v", err)
	}

	freshRoot := filepath.Join(root, "fresh-controller")
	if err := os.MkdirAll(freshRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	freshResult := filepath.Join(freshRoot, "result.json")
	fresh := runFreshController(t, fixture, freshRoot, freshResult, "main")
	if fresh.Pointer != winners[0].Generation || fresh.DownloadedSHA256 != winners[0].Generation.ArchiveSHA256 || fresh.DownloadedSize != int64(len(writerBody(winners[0].Session))) || len(fresh.History) != 3 {
		t.Fatalf("fresh controller evidence = %#v, winner = %#v", fresh, winners[0])
	}
	branchRoot := filepath.Join(root, "fresh-branch-controller")
	if err := os.MkdirAll(branchRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	branchResult := runFreshController(t, fixture, branchRoot, filepath.Join(branchRoot, "result.json"), branch.Branch)
	if branchResult.Pointer != loser.Generation || branchResult.DownloadedSHA256 != loser.Generation.ArchiveSHA256 || branchResult.DownloadedSize != int64(len(loserBytes)) || len(branchResult.History) != 1 || branchResult.History[0] != loser.Generation {
		t.Fatalf("fresh branch evidence = %#v, loser = %#v", branchResult, loser)
	}
	if uploads := fixture.listUploads(t, portabilityBucket, "brain/"); len(uploads) != 0 {
		t.Fatalf("multipart cleanup = %v", uploads)
	}
}

func runFreshController(t *testing.T, fixture *minioFixture, root, result, branch string) controllerResult {
	t.Helper()
	process := startController(t, []string{"CAMP_MINIO_HELPER=reopen", "CAMP_MINIO_ENDPOINT=" + fixture.endpoint,
		"CAMP_MINIO_ACCESS=" + fixture.signer.accessKey, "CAMP_MINIO_SECRET=" + fixture.signer.secretKey,
		"CAMP_CONTROLLER_RESULT=" + result, "CAMP_CONTROLLER_BRANCH=" + branch,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache")}, result, "")
	if err := process.cmd.Wait(); err != nil {
		t.Fatalf("fresh controller: %v: %s", err, process.output.Bytes())
	}
	return readControllerResult(t, result)
}

func startController(t *testing.T, environment []string, result, ready string) controllerProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMinIOControllerHelperProcess$")
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

func TestMinIOControllerHelperProcess(t *testing.T) {
	action := os.Getenv("CAMP_MINIO_HELPER")
	if action == "" {
		return
	}
	store := portabilityStore(t, os.Getenv("CAMP_MINIO_ENDPOINT"), os.Getenv("CAMP_MINIO_ACCESS"), os.Getenv("CAMP_MINIO_SECRET"))
	branch := os.Getenv("CAMP_CONTROLLER_BRANCH")
	if branch == "" {
		branch = "main"
	}
	lineage := domain.Lineage{Branch: branch}
	pointers := coordination.NewPointerRepository(store)
	generations := coordination.NewGenerationRepository(store)
	result := controllerResult{}
	if action == "writer" {
		result.Session = os.Getenv("CAMP_CONTROLLER_SESSION")
		baseline, err := pointers.Read(context.Background(), "brain", lineage)
		if err != nil {
			t.Fatal(err)
		}
		body := writerBody(result.Session)
		metadata := generationMetadata(t, result.Session, lineage, 2, &baseline.Pointer.Generation, body)
		if _, err := generations.PutAndVerify(context.Background(), metadata, bytesSource(body)); err != nil {
			t.Fatal(err)
		}
		result.Generation = metadata.Generation
		if err := os.WriteFile(os.Getenv("CAMP_CONTROLLER_READY"), []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForFiles(t, os.Getenv("CAMP_CONTROLLER_START"))
		_, err = pointers.CompareAndSwap(context.Background(), baseline, pointerFor(metadata))
		result.Published = err == nil
		result.PointerChanged = errors.Is(err, coordination.ErrPointerChanged)
		if err != nil && !result.PointerChanged {
			t.Fatal(err)
		}
	} else {
		pointer, err := pointers.Read(context.Background(), "brain", lineage)
		if err != nil {
			t.Fatal(err)
		}
		history, err := generations.List(context.Background(), "brain", lineage)
		if err != nil {
			t.Fatal(err)
		}
		reader, _, err := store.Get(context.Background(), pointer.Pointer.ObjectKey)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		result.Pointer = pointer.Pointer.Generation
		result.DownloadedSHA256 = sha256Hex(body)
		result.DownloadedSize = int64(len(body))
		for _, item := range history {
			result.History = append(result.History, item.Generation)
		}
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CAMP_CONTROLLER_RESULT"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func portabilityStore(t *testing.T, endpoint, access, secret string) ports.ObjectStore {
	t.Helper()
	backend, err := config.ResolveBackend("s3://"+portabilityBucket, config.S3Values{
		Endpoint: endpoint, Region: "us-east-1", PathStyle: true, Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.NewWriter(context.Background(), backend, objectstore.Options{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		Signer:     &minioSigner{accessKey: access, secretKey: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func generationMetadata(t *testing.T, session string, lineage domain.Lineage, number uint64, parent *domain.GenerationRef, body []byte) domain.GenerationMetadata {
	t.Helper()
	ref := domain.GenerationRef{Generation: number, ArchiveSHA256: sha256Hex(body)}
	objectKey := mustGenerationKey(t, lineage, ref)
	metadataKey, err := coordination.GenerationMetadataKey("brain", lineage, ref)
	if err != nil {
		t.Fatal(err)
	}
	return domain.GenerationMetadata{SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, Generation: ref, Parent: parent, ObjectKey: objectKey, MetadataKey: metadataKey, Size: int64(len(body)), CreatedAt: time.Unix(100+int64(number), 0).UTC(), SessionID: session, Verified: domain.Verification{LocalHaulLoadable: true}}
}

func pointerFor(metadata domain.GenerationMetadata) domain.LatestPointer {
	return domain.LatestPointer{SchemaVersion: domain.SchemaVersion, Capsule: metadata.Capsule, Lineage: metadata.Lineage, Generation: metadata.Generation, Parent: metadata.Parent, ObjectKey: metadata.ObjectKey, Size: metadata.Size, CreatedAt: metadata.CreatedAt, SessionID: metadata.SessionID}
}

func mustGenerationKey(t *testing.T, lineage domain.Lineage, ref domain.GenerationRef) string {
	t.Helper()
	key, err := coordination.GenerationObjectKey("brain", lineage, ref)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func writerBody(session string) []byte { return []byte("verified generation from " + session) }
func waitForFiles(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		all := true
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				all = false
				break
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %v", paths)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func readControllerResult(t *testing.T, path string) controllerResult {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result controllerResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func splitResults(results []controllerResult) (winners, losers []controllerResult) {
	for _, result := range results {
		if result.Published {
			winners = append(winners, result)
		}
		if result.PointerChanged {
			losers = append(losers, result)
		}
	}
	return
}
