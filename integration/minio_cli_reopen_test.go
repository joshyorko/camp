package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMinIOLifecycleVertical(t *testing.T) {
	if os.Getenv("CAMP_TEST_REAL_MINIO_REOPEN") != "1" {
		t.Skip("set CAMP_TEST_REAL_MINIO_REOPEN=1 to run the real MinIO lifecycle")
	}
	for _, name := range []string{"go", "devpod", "hauler", "pasta", "docker"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required executable %q: %v", name, err)
		}
	}
	fixture := startMinIO(t)
	fixture.createBucket(t, portabilityBucket)

	root := t.TempDir()
	source := filepath.Join(root, "mock Second Brain")
	controllerA := filepath.Join(root, "controller-a")
	controllerB := filepath.Join(root, "controller-b")
	devPod := newDevPodTestIsolation(root)
	bin := candidateBinary(t)
	writeLifecycleFixture(t, source)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	t.Cleanup(cancel)
	scenario := newLifecycleScenario(t, root, devPod, controllerA, controllerB)
	t.Cleanup(func() {
		scenario.Cleanup(t, bin)
	})

	envA := minioLifecycleEnvironment(controllerA, fixture.endpoint, fixture.signer.accessKey, fixture.signer.secretKey, devPod)
	scenario.RegisterController(controllerA, envA)
	t.Log("bootstrap the Docker provider inside the private DevPod context")
	mustBootstrapDevPodDockerProvider(t, ctx, devPod)
	scenario.CreateUnrelatedWorkspace(t, ctx)
	t.Log("initialize adopted fixture against real MinIO")
	mustRunLifecycle(t, ctx, envA, bin, "--json", "init", source, "--name", "minio-lifecycle")
	t.Log("open adopted fixture through real DevPod")
	recovered := decodeOpenResult(t, mustRunLifecycleAt(t, ctx, envA, source, bin, "--json", "open"))
	workspaceID := recovered.WorkspaceID
	scenario.TrackController(t, controllerA)
	if workspaceID == "" || recovered.SessionID == "" || recovered.Target != "." {
		t.Fatalf("open = %#v", recovered)
	}
	workspaceRoot := "/workspaces/" + workspaceID
	endpoints := scenario.Endpoints(t, controllerA, recovered.SessionID)
	t.Log("mutate workspace and publish a named image through CAMP_REGISTRY")
	mutate := fmt.Sprintf("set -eu; cd %s; printf 'after-open\\n' >> 'Projects/Unicode space/λ-note.txt'; chmod 600 'Projects/Unicode space/λ-note.txt'; test \"$CAMP_REGISTRY\" = %s; test \"$CAMP_FILESERVER\" = %s; wget -qO- \"http://$CAMP_REGISTRY/v2/\" >/dev/null; wget -qO- \"http://$CAMP_FILESERVER/\" >/dev/null; engine=; attempts=0; while test -z \"$engine\"; do for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; attempts=$((attempts+1)); test $attempts -lt 60; sleep 1; done; \"$engine\" pull alpine:3.20; image_id=$(\"$engine\" create alpine:3.20); \"$engine\" commit \"$image_id\" %s/camp/acceptance:named; \"$engine\" rm \"$image_id\"; \"$engine\" push %s/camp/acceptance:named", shellQuote(workspaceRoot), shellQuote(endpoints.Registry), shellQuote(endpoints.Fileserver), endpoints.Registry, endpoints.Registry)
	mustRunDevPod(t, ctx, devPod, "ssh", workspaceID, "--command", mutate)
	namedImageDigest := registryManifestDigest(t, ctx, endpoints.Registry, "camp/acceptance", "named")

	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "sync", "--camp", "minio-lifecycle")); generation != 1 {
		t.Fatalf("sync generation = %d, want 1", generation)
	}
	edit := fmt.Sprintf("printf 'after-sync\\n' >> %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "Projects/Unicode space/λ-note.txt"))))
	mustRunDevPod(t, ctx, devPod, "ssh", workspaceID, "--command", edit)
	t.Log("close adopted workspace after writing to MinIO")
	if generation := decodeGeneration(t, mustRunLifecycle(t, ctx, envA, bin, "--json", "close", "--camp", "minio-lifecycle")); generation != 2 {
		t.Fatalf("close generation = %d, want 2", generation)
	}
	assertDevPodWorkspacesAbsent(t, ctx, devPod, workspaceID)
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("adopted source did not survive: %v", err)
	}

	envB := minioLifecycleEnvironment(controllerB, fixture.endpoint, fixture.signer.accessKey, fixture.signer.secretKey, devPod)
	scenario.RegisterController(controllerB, envB)
	t.Log("reopen from MinIO with a fresh XDG controller")
	reopened := decodeOpenResult(t, mustRunLifecycleAt(t, ctx, envB, source, bin, "--json", "reopen"))
	scenario.TrackController(t, controllerB)
	if reopened.Generation != 2 || reopened.WorkspaceID == "" || reopened.Materialization == "" {
		t.Fatalf("fresh-controller reopen = %#v", reopened)
	}
	workspaceRoot = "/workspaces/" + reopened.WorkspaceID
	endpoints = scenario.Endpoints(t, controllerB, reopened.SessionID)
	note := shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "Projects/Unicode space/λ-note.txt")))
	verify := fmt.Sprintf("set -eux; grep -q before-open %s; grep -q after-open %s; grep -q after-sync %s; stat -c %%a %s | grep -qx 600; stat -c %%s %s | grep -qx %d; readlink %s | grep -qx README.md; find %s -xdev -samefile %s | grep -q README-hardlink.md; engine=; for candidate in docker podman nerdctl; do if command -v \"$candidate\" >/dev/null 2>&1 && \"$candidate\" info >/dev/null 2>&1; then engine=$candidate; break; fi; done; test -n \"$engine\"; %s", note, note, note, note, shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "large.bin"))), lifecycleLargeSize, shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "README-link.md"))), shellQuote(workspaceRoot), shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "README.md"))), namedImageReopenProofCommand(namedImageDigest))
	mustRunDevPod(t, ctx, devPod, "ssh", reopened.WorkspaceID, "--command", verify)
	preserved := fmt.Sprintf("set -eu; stat -c %%a %s | grep -qx 755; stat -c %%a %s | grep -qx 600; grep -qx 'user-owned agent state' %s", shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, "bin/camp-fixture"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, ".claude/fixture.md"))), shellQuote(filepath.ToSlash(filepath.Join(workspaceRoot, ".claude/fixture.md"))))
	mustRunDevPod(t, ctx, devPod, "ssh", reopened.WorkspaceID, "--command", preserved)
	t.Log("close fresh controller and verify teardown")
	mustRunLifecycle(t, ctx, envB, bin, "--json", "close", "--camp", "minio-lifecycle")
	assertDevPodWorkspacesAbsent(t, ctx, devPod, workspaceID, reopened.WorkspaceID)
	if _, err := os.Stat(reopened.Materialization); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned materialization still exists: %v", err)
	}
	scenario.AssertEndpointsClosed(t)
}

func namedImageReopenProofCommand(expectedDigest string) string {
	return fmt.Sprintf("reference=\"$CAMP_REGISTRY/camp/acceptance:named\"; expected_digest=%s; image_id=$(\"$engine\" image inspect --format '{{.Id}}' \"$reference\"); \"$engine\" image rm -f \"$image_id\"; if \"$engine\" image inspect \"$reference\" >/dev/null 2>&1; then exit 1; fi; digest_reference=\"$CAMP_REGISTRY/camp/acceptance@$expected_digest\"; \"$engine\" pull \"$digest_reference\"; repo_digests=$(\"$engine\" image inspect --format '{{json .RepoDigests}}' \"$digest_reference\"); case \"$repo_digests\" in *\"\\\"$digest_reference\\\"\"*) ;; *) exit 1 ;; esac; \"$engine\" run --rm \"$digest_reference\" true", shellQuote(expectedDigest))
}

func registryManifestDigest(t *testing.T, ctx context.Context, endpoint, repository, tag string) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/v2/%s/manifests/%s", endpoint, repository, tag), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read pre-close named image manifest: %v", err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("drain pre-close named image manifest: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pre-close named image manifest status = %s", response.Status)
	}
	digest, err := parseWorkspaceImageDigest([]byte(response.Header.Get("Docker-Content-Digest")))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func minioLifecycleEnvironment(controller, endpoint, access, secret string, devPod devPodTestIsolation) []string {
	env := []string{
		"XDG_CONFIG_HOME=" + filepath.Join(controller, "config"),
		"XDG_DATA_HOME=" + filepath.Join(controller, "data"),
		"XDG_STATE_HOME=" + filepath.Join(controller, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(controller, "cache"),
		"CAMP_BACKEND=s3://" + portabilityBucket + "/camp",
		"CAMP_DEVPOD_PROVIDER=docker",
		"CAMP_S3_ENDPOINT=" + endpoint,
		"CAMP_S3_REGION=us-east-1",
		"CAMP_S3_PATH_STYLE=true",
		"CAMP_S3_INSECURE=true",
		"AWS_ACCESS_KEY_ID=" + access,
		"AWS_SECRET_ACCESS_KEY=" + secret,
		"AWS_EC2_METADATA_DISABLED=true",
	}
	env = append(env, devPod.Environment()...)
	return env
}
