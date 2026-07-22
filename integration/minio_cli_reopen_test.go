package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestMinIOCLIFreshControllerReopen(t *testing.T) {
	if os.Getenv("CAMP_TEST_REAL_MINIO_REOPEN") != "1" {
		t.Skip("set CAMP_TEST_REAL_MINIO_REOPEN=1 to run the real MinIO lifecycle")
	}
	for _, name := range []string{"go", "devpod", "hauler", "pasta", "docker"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required executable %q: %v", name, err)
		}
	}
	assertNoDevPodWorkspaces(t, context.Background())

	fixture := startMinIO(t)
	fixture.createBucket(t, portabilityBucket)

	root := t.TempDir()
	source := filepath.Join(root, "mock Second Brain")
	controllerA := filepath.Join(root, "controller-a")
	controllerB := filepath.Join(root, "controller-b")
	bin := filepath.Join(root, "camp")
	registryPort, fileserverPort := reserveLoopbackPort(t), reserveLoopbackPort(t)
	writeLifecycleFixture(t, source)
	build := exec.Command("go", "build", "-o", bin, "../cmd/camp")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build camp: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	t.Cleanup(cancel)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()
		workspaces, _ := listDevPodWorkspaces(cleanupCtx)
		for _, workspace := range workspaces {
			_, _ = runLifecycleCommand(cleanupCtx, nil, "devpod", "delete", "--context", "default", "--ignore-not-found", workspace)
		}
	})

	envA := minioLifecycleEnvironment(controllerA, source, fixture.endpoint, fixture.signer.accessKey, fixture.signer.secretKey, registryPort, fileserverPort)
	t.Log("initialize adopted fixture against real MinIO")
	mustRunLifecycle(t, ctx, envA, bin, "--json", "init", source)
	t.Log("open adopted fixture through real DevPod")
	recovered := decodeOpenResult(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "open", "Projects/Unicode space"))
	workspaceID := recovered.WorkspaceID
	if workspaceID == "" || recovered.SessionID == "" || recovered.Target != "Projects/Unicode space" {
		t.Fatalf("open = %#v", recovered)
	}
	workspaceRoot := "/workspaces/" + workspaceID
	t.Log("mutate workspace and publish an explicit checkpoint")
	mutate := fmt.Sprintf("set -eux; cd %s; printf 'after-open\\n' >> 'Projects/Unicode space/λ-note.txt'; chmod 600 'Projects/Unicode space/λ-note.txt'; test \"$CAMP_REGISTRY\" = %s; test \"$CAMP_FILESERVER\" = %s; wget -qO- \"http://$CAMP_REGISTRY/v2/\" >/dev/null; wget -qO- \"http://$CAMP_FILESERVER/\" >/dev/null", shellQuote(workspaceRoot), shellQuote(loopbackEndpoint(registryPort)), shellQuote(loopbackEndpoint(fileserverPort)))
	mustRunLifecycle(t, ctx, nil, "devpod", "ssh", "--context", "default", workspaceID, "--command", mutate)

	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "sync")); generation != 1 {
		t.Fatalf("sync generation = %d, want 1", generation)
	}
	edit := fmt.Sprintf("printf 'after-sync\\n' >> %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "Projects/Unicode space/λ-note.txt"))))
	mustRunLifecycle(t, ctx, nil, "devpod", "ssh", "--context", "default", workspaceID, "--command", edit)
	t.Log("close adopted workspace after writing to MinIO")
	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "close")); generation != 2 {
		t.Fatalf("close generation = %d, want 2", generation)
	}
	assertNoDevPodWorkspaces(t, ctx)
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("adopted source did not survive: %v", err)
	}

	envB := minioLifecycleEnvironment(controllerB, "", fixture.endpoint, fixture.signer.accessKey, fixture.signer.secretKey, registryPort, fileserverPort)
	t.Log("reopen from MinIO with a fresh XDG controller")
	reopened := decodeOpenResult(t, mustRunLifecycle(t, ctx, envB, bin, "--json", "reopen"))
	if reopened.Generation != 2 || reopened.WorkspaceID == "" || reopened.Materialization == "" {
		t.Fatalf("fresh-controller reopen = %#v", reopened)
	}
	workspaceRoot = "/workspaces/" + reopened.WorkspaceID
	verify := fmt.Sprintf("set -eux; grep -q before-open %s; grep -q after-open %s; grep -q after-sync %s; stat -c %%a %s | grep -qx 600; stat -c %%s %s | grep -qx %d", shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "Projects/Unicode space/λ-note.txt"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "Projects/Unicode space/λ-note.txt"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "Projects/Unicode space/λ-note.txt"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "Projects/Unicode space/λ-note.txt"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "large.bin"))), lifecycleLargeSize)
	mustRunLifecycle(t, ctx, nil, "devpod", "ssh", "--context", "default", reopened.WorkspaceID, "--command", verify)
	t.Log("close fresh controller and verify teardown")
	mustRunLifecycle(t, ctx, envB, bin, "--json", "close")
	assertNoDevPodWorkspaces(t, ctx)
	if _, err := os.Stat(reopened.Materialization); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned materialization still exists: %v", err)
	}
	assertLoopbackPortClosed(t, registryPort)
	assertLoopbackPortClosed(t, fileserverPort)
}

func minioLifecycleEnvironment(controller, source, endpoint, access, secret string, registryPort, fileserverPort int) []string {
	env := []string{
		"XDG_CONFIG_HOME=" + filepath.Join(controller, "config"),
		"XDG_DATA_HOME=" + filepath.Join(controller, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(controller, "cache"),
		"CAMP_BACKEND=s3://" + portabilityBucket + "/camp",
		"CAMP_CAPSULE=default",
		"CAMP_DEVPOD_PROVIDER=room-of-requirement",
		"CAMP_S3_ENDPOINT=" + endpoint,
		"CAMP_S3_REGION=us-east-1",
		"CAMP_S3_PATH_STYLE=true",
		"CAMP_S3_INSECURE=true",
		"AWS_ACCESS_KEY_ID=" + access,
		"AWS_SECRET_ACCESS_KEY=" + secret,
		"AWS_EC2_METADATA_DISABLED=true",
		"CAMP_REGISTRY_PORT=" + strconv.Itoa(registryPort),
		"CAMP_FILESERVER_PORT=" + strconv.Itoa(fileserverPort),
	}
	if source != "" {
		env = append(env, "CAMP_SOURCE="+source)
	}
	return env
}
